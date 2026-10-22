package gobluetti

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Upstream application identity and gateway, from profile/application.yaml and
// manifest.json of the BLUETTI Home Assistant integration.
const (
	defaultGatewayURL = "https://gw.bluettipower.com"
	appKey            = "19974C15BCCBC91B21A8BA71BF5"
	appVersion        = "1.0.5"
)

const (
	maxAPIResponse      = 1 << 20
	msgCodeTokenExpired = 805
	refreshWindow       = 7 * 24 * time.Hour // upstream refreshes when less than this is left
	refreshInterval     = time.Hour          // and not more often than this
)

var (
	// ErrLoginRequired means the token cannot be renewed: the user has to log
	// in again, for example with [Auth.LoginLocal].
	ErrLoginRequired = errors.New("login required")

	// ErrDeviceNotFound means the cloud returned no device with the requested
	// serial number.
	ErrDeviceNotFound = errors.New("device not found")
)

// APIError is returned when the BLUETTI gateway rejects a request: either with
// a non-2xx HTTP status (StatusCode set) or with a non-zero msgCode in its
// response (MsgCode, MsgID and Message set).
//
// MsgCode 403 with a Message like "device does not belong to the current user"
// means the device is not bound for cloud control yet; see [Client.Bind].
type APIError struct {
	StatusCode int
	MsgCode    int
	MsgID      string
	Message    string // the gateway's explanation, often in Chinese
}

func (e *APIError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("gobluetti: api: HTTP %d", e.StatusCode)
	}
	msg := fmt.Sprintf("gobluetti: api: msgCode %d", e.MsgCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg + " (msgId " + e.MsgID + ")"
}

// Client calls the BLUETTI cloud API on behalf of one account.
//
// The client renews its token when needed and reports every new token to
// OnToken. Persist it there: SSO rotates refresh tokens, so an old token file
// stops working after the first renewal. Set the optional fields before the
// first call. A Client is safe for concurrent use.
type Client struct {
	// OnToken is called with every new token, one call at a time, before the
	// call that renewed it returns. It must not call methods of the Client.
	OnToken func(*Token)

	GatewayURL string       // optional; default: https://gw.bluettipower.com; a token Host wins
	HTTPClient *http.Client // optional; default: a client with a 30s timeout

	auth *Auth

	mu          sync.Mutex
	tok         *Token // replaced, never modified
	lastRefresh time.Time

	backoff   time.Duration // first Watch reconnect delay
	heartbeat time.Duration // shortest STOMP heartbeat interval Watch uses
}

// NewClient returns a client that uses tok and renews it with auth. A nil auth
// means the zero [Auth].
func NewClient(auth *Auth, tok *Token) *Client {
	t := *tok
	return &Client{auth: cmp.Or(auth, &Auth{}), tok: &t, backoff: time.Second, heartbeat: 10 * time.Second}
}

// Token returns a copy of the client's current token.
func (c *Client) Token() *Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := *c.tok
	return &t
}

// apiBase returns the gateway URL for tok.
func (c *Client) apiBase(tok *Token) string {
	return strings.TrimSuffix(cmp.Or(tok.Host, c.GatewayURL, defaultGatewayURL), "/")
}

// freshToken returns the token to use, refreshing it first when it expires
// within refreshWindow and no refresh was tried within refreshInterval.
func (c *Client) freshToken(ctx context.Context) *Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok.Expiry.IsZero() || time.Until(c.tok.Expiry) >= refreshWindow || time.Since(c.lastRefresh) < refreshInterval {
		return c.tok
	}
	c.lastRefresh = time.Now()
	// A failure is fine: the current token still works or the gateway answers 805.
	if tok, err := c.auth.Refresh(ctx, c.tok); err == nil {
		c.setToken(tok)
	}
	return c.tok
}

// renew replaces used after the cloud rejected it. When another call already
// replaced it, renew returns that token instead of refreshing again.
func (c *Client) renew(ctx context.Context, used *Token) (*Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != used {
		return c.tok, nil
	}
	c.lastRefresh = time.Now()
	tok, err := c.auth.Refresh(ctx, used)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("gobluetti: %w: %w", ErrLoginRequired, err)
	}
	c.setToken(tok)
	return tok, nil
}

// setToken must be called with c.mu held.
func (c *Client) setToken(tok *Token) {
	c.tok = tok
	if c.OnToken != nil {
		t := *tok
		c.OnToken(&t)
	}
}

// call sends one API request and decodes the response data into out. On msgCode
// 805 it renews the token and retries once; the gateway rejected the first
// attempt before acting on it, so even a command is safe to replay.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	tok := c.freshToken(ctx)
	expired, err := c.send(ctx, tok, method, path, query, body, out)
	if err != nil || !expired {
		return err
	}
	if tok, err = c.renew(ctx, tok); err != nil {
		return err
	}
	if expired, err = c.send(ctx, tok, method, path, query, body, out); err == nil && expired {
		return fmt.Errorf("gobluetti: %w: the renewed token was rejected", ErrLoginRequired)
	}
	return err
}

// send makes one request with tok. It reports msgCode 805 as expired instead of an error.
func (c *Client) send(ctx context.Context, tok *Token, method, path string, query url.Values, body, out any) (expired bool, err error) {
	target := c.apiBase(tok) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reqBody io.Reader
	if body != nil {
		data, _ := json.Marshal(body) // request documents are maps of strings and always marshal
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return false, fmt.Errorf("gobluetti: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", tok.AccessToken) // the raw token, no "Bearer"
	req.Header.Set("x-os", "open")
	req.Header.Set("x-app-key", appKey)
	req.Header.Set("x-app-ver", appVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := cmp.Or(c.HTTPClient, defaultHTTPClient).Do(req)
	if err != nil {
		return false, fmt.Errorf("gobluetti: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is fully read below; Close has nothing to report
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, &APIError{StatusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return false, fmt.Errorf("gobluetti: %s %s: read response: %w", method, path, err)
	}
	if len(data) > maxAPIResponse {
		return false, fmt.Errorf("gobluetti: %s %s: response exceeds %d bytes", method, path, maxAPIResponse)
	}

	expired, err = decodeEnvelope(data, out)
	if _, isAPI := errors.AsType[*APIError](err); err != nil && !isAPI {
		return false, fmt.Errorf("gobluetti: %s %s: %w", method, path, err)
	}
	return expired, err
}

// decodeEnvelope unwraps {msgId, msgCode, data} into out. It reports msgCode 805
// as expired, any other non-zero msgCode as an [*APIError].
func decodeEnvelope(data []byte, out any) (expired bool, err error) {
	var envelope struct {
		MsgID   string          `json:"msgId"`
		MsgCode int             `json:"msgCode"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return false, errors.New("response is not a JSON envelope")
	}
	switch {
	case envelope.MsgCode == msgCodeTokenExpired:
		return true, nil
	case envelope.MsgCode != 0:
		return false, &APIError{MsgCode: envelope.MsgCode, MsgID: envelope.MsgID, Message: envelope.Message}
	case out == nil || len(envelope.Data) == 0:
		return false, nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return false, fmt.Errorf("decode data: %w", err)
	}
	return false, nil
}
