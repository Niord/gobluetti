package gobluetti

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Upstream identity, from profile/application.yaml of the BLUETTI Home Assistant integration.
const (
	defaultSSOURL       = "https://sso.bluettipower.com"
	defaultClientID     = "HomeAssistant"
	defaultClientSecret = "SG9tZUFzc2lzdGFudA==" //nolint:gosec // public value published by upstream; sent as is, not base64-decoded
)

const maxTokenResponse = 64 << 10

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Auth obtains and renews OAuth2 tokens from BLUETTI SSO.
//
// The zero value is ready to use and identifies itself with the client ID and
// secret that the official BLUETTI Home Assistant integration publishes.
// Set a field to override that default. An Auth is safe for concurrent use.
type Auth struct {
	SSOURL       string       // optional; default: https://sso.bluettipower.com
	ClientID     string       // optional; default: HomeAssistant
	ClientSecret string       // optional; default: the integration's published secret
	HTTPClient   *http.Client // optional; default: a client with a 30s timeout
}

// Token is an OAuth2 token for a BLUETTI cloud account. It marshals to JSON
// so it can be stored and loaded back.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`

	// Expiry is when the access token expires; zero means unknown.
	Expiry time.Time `json:"expiry,omitzero"`

	// Host is the account's cloud data-center URL as returned by SSO; empty if
	// SSO did not return one.
	Host string `json:"host,omitempty"`
}

// OAuthError is returned when SSO rejects a token request.
// A Code of "invalid_grant" means the code or refresh token is no longer
// valid and the user has to log in again.
type OAuthError struct {
	StatusCode  int    // HTTP status of the response
	Code        string // OAuth2 "error" value; empty if the body was not OAuth2 JSON
	Description string // OAuth2 "error_description" value
}

func (e *OAuthError) Error() string {
	msg := fmt.Sprintf("gobluetti: oauth: HTTP %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// AuthCodeURL returns the SSO page that the user opens in a browser to log in.
// After login SSO redirects the browser to redirectURI with "code" and the
// given state in the query.
func (a *Auth) AuthCodeURL(redirectURI, state string) string {
	return cmp.Or(a.SSOURL, defaultSSOURL) + "/oauth2/grant?" + url.Values{
		"response_type": {"code"},
		"client_id":     {cmp.Or(a.ClientID, defaultClientID)},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}.Encode()
}

// Exchange trades an authorization code for a token. redirectURI must be the
// one passed to [Auth.AuthCodeURL]. A rejection by SSO is an [*OAuthError].
func (a *Auth) Exchange(ctx context.Context, code, redirectURI string) (*Token, error) {
	return a.requestToken(ctx, &Token{}, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
}

// Refresh returns a renewed copy of t. The refresh token and host are kept
// from t when SSO does not return new ones. A rejection by SSO is an
// [*OAuthError].
func (a *Auth) Refresh(ctx context.Context, t *Token) (*Token, error) {
	if t.RefreshToken == "" {
		return nil, errors.New("gobluetti: refresh: token has no refresh token")
	}
	return a.requestToken(ctx, t, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
}

// requestToken posts form to the token endpoint and returns prev updated with the response.
func (a *Auth) requestToken(ctx context.Context, prev *Token, form url.Values) (*Token, error) {
	form.Set("client_id", cmp.Or(a.ClientID, defaultClientID))
	form.Set("client_secret", cmp.Or(a.ClientSecret, defaultClientSecret))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cmp.Or(a.SSOURL, defaultSSOURL)+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("gobluetti: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := cmp.Or(a.HTTPClient, defaultHTTPClient).Do(req)
	if err != nil {
		return nil, fmt.Errorf("gobluetti: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is fully read below; Close has nothing to report
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse+1))
	if err != nil {
		return nil, fmt.Errorf("gobluetti: read token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		oauthErr := &OAuthError{StatusCode: resp.StatusCode}
		var doc struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &doc) == nil {
			oauthErr.Code, oauthErr.Description = doc.Error, doc.Description
		}
		return nil, oauthErr
	}
	if len(body) > maxTokenResponse {
		return nil, fmt.Errorf("gobluetti: token response exceeds %d bytes", maxTokenResponse)
	}

	// The response body holds the tokens, so decode errors never quote it.
	var doc struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		ExpiresIn    json.Number `json:"expires_in"` // also accepts "3600" as a string
		Host         string      `json:"host"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("gobluetti: token response is not valid JSON")
	}
	if doc.AccessToken == "" {
		return nil, errors.New("gobluetti: token response has no access_token")
	}
	tok := *prev
	tok.AccessToken = doc.AccessToken
	tok.RefreshToken = cmp.Or(doc.RefreshToken, tok.RefreshToken)
	tok.Host = cmp.Or(doc.Host, tok.Host)
	tok.Expiry = time.Time{}
	if doc.ExpiresIn != "" {
		secs, err := doc.ExpiresIn.Int64()
		if err != nil || secs < 0 || secs > math.MaxInt64/int64(time.Second) {
			return nil, fmt.Errorf("gobluetti: token response has invalid expires_in %q", doc.ExpiresIn)
		}
		tok.Expiry = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return &tok, nil
}
