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
	keepAliveSteps  = 6                // without a requested interval, send heartbeats every 6x the floor (60s, as upstream)
	silentSteps     = 18               // without announced heartbeats, a session silent for 18x the floor (3 min) is dead
	maxBackoffSteps = 30               // the reconnect delay grows to 30x the first one
)

// MessageRealtimeStatus is the [Update.Type] of the cloud's periodic device
// reports, which carry readings.
const MessageRealtimeStatus = "DEVICE_REALTIME_STATUS"

// Update is one device notification pushed by the cloud. A push carries only
// some readings: the device alternates between reports of its main readings
// and of its battery pack, so a field missing from Values or Fields is not in
// this push, not zero. Merge updates into the last known state.
type Update struct {
	SN   string
	Type string    // the cloud's message type, for example [MessageRealtimeStatus]; may be empty
	Time time.Time // when the device reported, from the push; zero if it has none

	// Values holds readings by function code, for example [FnSOC]. Only a
	// [MessageRealtimeStatus] push has them, and only for the codes whose push
	// fields are known: SOC, the grid, AC, PV and DC powers, the AC output
	// switch, and the battery time left.
	Values map[string]string

	// Fields holds every reading of the push as text, under the cloud's own
	// field names, for example "batteryVoltage". The names are the cloud's and
	// may change with it.
	Fields map[string]string
}

// pushCodes maps realtime push fields to the function codes whose
// values they matched in a capture from an AP300.
var pushCodes = map[string]string{
	"batterySoc":   FnSOC,
	"powerGridIn":  FnGridPower,
	"powerAcOut":   FnACLoadPower,
	"powerPvIn":    FnPVPower,
	"powerDcOut":   FnDCLoadPower,
	"acSwitch":     FnACOutput,
	"dsgEmptyTime": FnDischargeTime,
}

// pushTimeZone is the zone of payload.timestamp, which carries none: China
// Standard Time, without daylight saving.
var pushTimeZone = time.FixedZone("UTC+8", 8*60*60)

// Watch reports device changes in real time. It calls onChange with the
// serial number of every device the cloud says has changed. It works like
// [Client.WatchUpdates] and drops what the push says about the new state.
func (c *Client) Watch(ctx context.Context, onChange func(sn string)) error {
	return c.WatchUpdates(ctx, func(u Update) { onChange(u.SN) })
}

// WatchUpdates reports device state in real time, from the cloud's pushes
// alone: it makes no REST request. The cloud's REST API can be down for hours
// while pushes keep arriving about once a minute.
//
// onUpdate runs on the goroutine that reads the websocket, one call at a time,
// in arrival order. Keep it fast: copy what you need and return, and do REST
// calls or other slow work elsewhere. A slow onUpdate delays later pushes, and
// one that blocks for minutes makes the session look dead.
//
// WatchUpdates keeps a websocket subscription open and reconnects with backoff
// after any failure. It returns only when ctx is done, or with an error
// matching [ErrLoginRequired] when the token cannot be renewed.
func (c *Client) WatchUpdates(ctx context.Context, onUpdate func(Update)) error {
	delay := c.backoff
	for {
		tok := c.freshToken(ctx)
		connected, authFailed := c.watchSession(ctx, tok, onUpdate)
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
func (c *Client) watchSession(ctx context.Context, tok *Token, onUpdate func(Update)) (connected, authFailed bool) {
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
				if u, ok := parseUpdate(f.body); ok {
					onUpdate(u)
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
		return silentSteps * c.heartbeat, true
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

// parseUpdate reads a notification body. It reports false when the body has
// no serial number. Each part is decoded on its own, so a malformed part loses
// only itself.
func parseUpdate(body string) (Update, bool) {
	var doc struct {
		Data struct {
			Message map[string]json.RawMessage `json:"message"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &doc) // a malformed body has no message
	var u Update
	if json.Unmarshal(doc.Data.Message["deviceSn"], &u.SN) != nil || u.SN == "" {
		return Update{}, false
	}
	_ = json.Unmarshal(doc.Data.Message["messageType"], &u.Type) // no type: ""

	var payload map[string]json.RawMessage
	_ = json.Unmarshal(doc.Data.Message["payload"], &payload) // not an object: no readings
	var stamp string
	if json.Unmarshal(payload["timestamp"], &stamp) == nil {
		u.Time, _ = time.ParseInLocation(time.DateTime, stamp, pushTimeZone) // unparseable: zero
	}
	for name, raw := range payload {
		var value flexString
		if name == "allFieldIsNull" || name == "timestamp" || string(raw) == "null" ||
			raw[0] == '{' || raw[0] == '[' || json.Unmarshal(raw, &value) != nil {
			continue
		}
		if u.Fields == nil {
			u.Fields = map[string]string{}
		}
		u.Fields[name] = string(value)
		if code, known := pushCodes[name]; known && u.Type == MessageRealtimeStatus {
			if u.Values == nil {
				u.Values = map[string]string{}
			}
			u.Values[code] = string(value)
		}
	}
	return u, true
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
