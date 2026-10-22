package gobluetti

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const (
	watchPath       = "/api/edgeiotgw/ws-coordination/websocket"
	connectTimeout  = 30 * time.Second // dial plus waiting for CONNECTED
	silentTimeout   = 10 * time.Minute // read timeout when the server announces no heartbeats (the gateway sends heart-beat:0,0)
	keepAliveSteps  = 6                // without a requested interval, send heartbeats every 6x the floor (60s, as upstream)
	maxBackoffSteps = 30               // the reconnect delay grows to 30x the first one
)

// Watch reports device changes in real time. It calls onChange with the
// serial number of every device the cloud says has changed; read the new state
// with [Client.Device]. onChange runs on one goroutine, one call at a time, and
// a slow onChange delays later notifications.
//
// Watch keeps a websocket subscription open and reconnects with backoff after
// any failure. It returns only when ctx is done, or with an error matching
// [ErrLoginRequired] when the token cannot be renewed.
func (c *Client) Watch(ctx context.Context, onChange func(sn string)) error {
	delay := c.backoff
	for {
		tok := c.freshToken(ctx)
		connected, authFailed := c.watchSession(ctx, tok, onChange)
		if ctx.Err() != nil {
			return fmt.Errorf("gobluetti: watch: %w", ctx.Err())
		}
		if authFailed {
			if _, err := c.renew(ctx, tok); err != nil {
				return err
			}
			continue // reconnect at once with the new token
		}
		if connected {
			delay = c.backoff
		}
		// shortcut: session errors are dropped, add an error callback when callers need to see why Watch reconnects.
		select {
		case <-ctx.Done():
			return fmt.Errorf("gobluetti: watch: %w", ctx.Err())
		case <-time.After(delay):
		}
		delay = min(delay*2, maxBackoffSteps*c.backoff)
	}
}

// watchSession runs one websocket connection until it fails or ctx is done. It
// reports whether the server accepted the session (CONNECTED) and whether it
// ended with an authentication error.
func (c *Client) watchSession(ctx context.Context, tok *Token, onChange func(sn string)) (connected, authFailed bool) {
	conn, err := c.dialWatch(ctx, tok)
	if err != nil {
		return false, false
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()                                             // runs last: the heartbeat goroutine has stopped
	defer func() { _ = conn.Close() }()                         // nothing to report for a connection being dropped
	defer cancel()                                              // runs first
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() }) // unblocks Receive on cancel
	defer stop()

	connect := stompEncode("CONNECT",
		"accept-version", "1.0,1.1,2.0",
		"heart-beat", fmt.Sprintf("%d,%d", c.heartbeat.Milliseconds(), c.heartbeat.Milliseconds()),
		"host", conn.Config().Location.Hostname(),
		"Authorization", tok.AccessToken,
		"x-os", "open",
		"x-app-key", appKey,
		"x-app-ver", appVersion,
	)
	if websocket.Message.Send(conn, connect) != nil {
		return false, false
	}

	readTimeout := connectTimeout
	for {
		if conn.SetReadDeadline(time.Now().Add(readTimeout)) != nil {
			return connected, false
		}
		var msg string
		if websocket.Message.Receive(conn, &msg) != nil {
			return connected, false
		}
		for _, f := range parseFrames(msg) {
			switch f.command {
			case "CONNECTED":
				var ok bool
				if readTimeout, ok = c.subscribe(sessionCtx, conn, f, &wg); !ok {
					return false, false
				}
				connected = true
			case "MESSAGE":
				if sn := changedDevice(f.body); sn != "" {
					onChange(sn)
				}
			case "ERROR":
				return connected, isAuthError(f.headers["message"])
			}
		}
	}
}

// subscribe answers CONNECTED: it subscribes to the user's notifications and
// starts the heartbeat sender on wg, which stops with ctx. It returns the read
// timeout the server's heartbeat allows, and false if the session cannot go on.
func (c *Client) subscribe(ctx context.Context, conn *websocket.Conn, connected stompFrame, wg *sync.WaitGroup) (time.Duration, bool) {
	user := connected.headers["user-name"]
	if user == "" || websocket.Message.Send(conn, stompEncode("SUBSCRIBE",
		"id", "0", "destination", "/ws-subscribe/user/"+user+"/notify", "ack", "auto")) != nil {
		return 0, false
	}
	serverSend, serverReceive := parseHeartBeat(connected.headers["heart-beat"])
	every := max(serverReceive, c.heartbeat)
	if serverReceive == 0 {
		every = keepAliveSteps * c.heartbeat // keeps NATs and proxies from dropping a quiet connection
	}
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
				if websocket.Message.Send(conn, "\n") != nil {
					return
				}
			}
		}
	})
	if serverSend == 0 {
		return silentTimeout, true
	}
	return 2 * max(serverSend, c.heartbeat), true
}

// dialWatch opens the websocket for tok's gateway.
func (c *Client) dialWatch(ctx context.Context, tok *Token) (*websocket.Conn, error) {
	base := c.apiBase(tok)
	wsURL, err := watchURL(base)
	if err != nil {
		return nil, err
	}
	cfg, err := websocket.NewConfig(wsURL, base)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return cfg.DialContext(ctx)
}

// changedDevice returns the serial number in a notification body, or "" when it has none.
func changedDevice(body string) string {
	var doc struct {
		Data struct {
			Message struct {
				DeviceSN string `json:"deviceSn"`
			} `json:"message"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &doc) // a malformed body has no serial number
	return doc.Data.Message.DeviceSN
}

// isAuthError reports whether a STOMP ERROR message header carries a msgCode that
// upstream treats as a token problem.
func isAuthError(message string) bool {
	var doc struct {
		MsgCode int `json:"msgCode"`
	}
	_ = json.Unmarshal([]byte(message), &doc) // no msgCode: not an auth error
	switch doc.MsgCode {
	case 400, 403, 600, msgCodeTokenExpired:
		return true
	}
	return false
}

// watchURL maps the gateway URL to its websocket endpoint.
func watchURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("gobluetti: gateway URL %q is not http(s)", base)
	}
	u.Path = watchPath
	return u.String(), nil
}

// parseHeartBeat reads a STOMP heart-beat header "send,receive" in milliseconds.
func parseHeartBeat(h string) (send, receive time.Duration) {
	s, r, _ := strings.Cut(h, ",")
	ms := func(v string) time.Duration {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return 0
		}
		return time.Duration(n) * time.Millisecond
	}
	return ms(s), ms(r)
}

// stompFrame is one received STOMP frame.
type stompFrame struct {
	command string
	headers map[string]string
	body    string
}

var stompUnescape = strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\c`, ":", `\\`, `\`)

// parseFrames splits a websocket message into STOMP frames, skipping heartbeats.
func parseFrames(msg string) []stompFrame {
	var frames []stompFrame
	for chunk := range strings.SplitSeq(msg, "\x00") {
		chunk = strings.TrimLeft(chunk, "\r\n")
		if chunk == "" {
			continue
		}
		head, body, _ := strings.Cut(strings.ReplaceAll(chunk, "\r\n", "\n"), "\n\n")
		command, headerLines, _ := strings.Cut(head, "\n")
		f := stompFrame{command: command, headers: map[string]string{}, body: body}
		for line := range strings.SplitSeq(headerLines, "\n") {
			k, v, found := strings.Cut(line, ":")
			if _, seen := f.headers[k]; found && !seen { // STOMP 1.2: the first occurrence wins
				f.headers[k] = stompUnescape.Replace(v)
			}
		}
		frames = append(frames, f)
	}
	return frames
}

// stompEncode builds a frame from a command and header name/value pairs. Values
// are sent as is: CONNECT must not be escaped, and the others need no escaping.
func stompEncode(command string, headers ...string) string {
	var b strings.Builder
	b.WriteString(command + "\n")
	for i := 0; i+1 < len(headers); i += 2 {
		b.WriteString(headers[i] + ":" + headers[i+1] + "\n")
	}
	b.WriteString("\n\x00")
	return b.String()
}
