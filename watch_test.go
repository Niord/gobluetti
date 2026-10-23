package gobluetti

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestParseFrames(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []stompFrame
	}{
		{name: "heartbeat", msg: "\n"},
		{
			name: "connected",
			msg:  "CONNECTED\nversion:1.2\nuser-name:u42\n\n\x00",
			want: []stompFrame{{command: "CONNECTED", headers: map[string]string{"version": "1.2", "user-name": "u42"}}},
		},
		{
			name: "upstream ERROR with escaped colons",
			msg:  "ERROR\nmessage:{\"msgCode\"\\c805,\"message\"\\c\"token expired\"}\n\n\x00",
			want: []stompFrame{{command: "ERROR", headers: map[string]string{"message": `{"msgCode":805,"message":"token expired"}`}}},
		},
		{
			name: "two frames, CRLF, repeated header keeps the first",
			msg:  "\nMESSAGE\r\na:1\r\na:2\r\n\r\nbody1\x00\nMESSAGE\n\nbody2\x00",
			want: []stompFrame{
				{command: "MESSAGE", headers: map[string]string{"a": "1"}, body: "body1"},
				{command: "MESSAGE", headers: map[string]string{}, body: "body2"},
			},
		},
		{name: "backslash escapes", msg: "MESSAGE\nk:a\\\\nb\\nc\n\n\x00", want: []stompFrame{{command: "MESSAGE", headers: map[string]string{"k": "a\\nb\nc"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFrames(tt.msg)
			if len(got) != len(tt.want) {
				t.Fatalf("parseFrames(%q) = %+v, want %+v", tt.msg, got, tt.want)
			}
			for i := range got {
				g, w := got[i], tt.want[i]
				if g.command != w.command || g.body != w.body || len(g.headers) != len(w.headers) {
					t.Errorf("parseFrames(%q)[%d] = %+v, want %+v", tt.msg, i, g, w)
				}
				for k, v := range w.headers {
					if g.headers[k] != v {
						t.Errorf("parseFrames(%q)[%d] header %q = %q, want %q", tt.msg, i, k, g.headers[k], v)
					}
				}
			}
		})
	}
}

// stompPeer is the server side of one test websocket connection.
type stompPeer struct {
	t  *testing.T
	ws *websocket.Conn
}

func (p stompPeer) next() stompFrame {
	for {
		var msg string
		if err := websocket.Message.Receive(p.ws, &msg); err != nil {
			return stompFrame{command: "EOF"}
		}
		if frames := parseFrames(msg); len(frames) > 0 {
			return frames[0]
		}
	}
}

func (p stompPeer) send(frame string) {
	if err := websocket.Message.Send(p.ws, frame); err != nil {
		p.t.Logf("fake gateway send: %v", err) // the client may already be gone
	}
}

// fakeWatchGateway runs session for every websocket connection; fail(n) can refuse
// attempt n (1-based) with HTTP 503 before the upgrade.
func fakeWatchGateway(t *testing.T, fail func(n int) bool, session func(n int, p stompPeer)) (url string, attempts func() []time.Time) {
	t.Helper()
	var mu sync.Mutex
	var times []time.Time
	ws := websocket.Handler(func(ws *websocket.Conn) {
		mu.Lock()
		n := len(times)
		mu.Unlock()
		session(n, stompPeer{t: t, ws: ws})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/edgeiotgw/ws-coordination/websocket" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		times = append(times, time.Now())
		n := len(times)
		mu.Unlock()
		if fail != nil && fail(n) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		ws.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(times)
	}
}

func newWatchClient(gatewayURL string, auth *Auth, tok Token) *Client {
	c := NewClient(auth, &tok)
	c.GatewayURL = gatewayURL
	c.backoff = 20 * time.Millisecond
	c.heartbeat = 20 * time.Millisecond
	return c
}

const connected = "CONNECTED\nversion:1.2\nheart-beat:0,20\nuser-name:u42\n\n\x00"

func notify(sn string) string {
	return "MESSAGE\ndestination:/ws-subscribe/user/u42/notify\n\n{\"data\":{\"message\":{\"deviceSn\":\"" + sn + "\"}}}\x00"
}

func TestWatchSession(t *testing.T) {
	gwURL, _ := fakeWatchGateway(t, nil, func(_ int, p stompPeer) {
		c := p.next()
		want := map[string]string{
			"accept-version": "1.0,1.1,2.0", "Authorization": "acc", "x-os": "open",
			"x-app-key": appKey, "x-app-ver": appVersion, "host": "127.0.0.1",
		}
		for k, v := range want {
			if c.headers[k] != v {
				t.Errorf("CONNECT header %q = %q, want %q", k, c.headers[k], v)
			}
		}
		if c.command != "CONNECT" || c.headers["heart-beat"] == "" {
			t.Errorf("first frame = %+v, want CONNECT with heart-beat", c)
		}
		p.send(connected)
		if s := p.next(); s.command != "SUBSCRIBE" || s.headers["destination"] != "/ws-subscribe/user/u42/notify" || s.headers["ack"] != "auto" {
			t.Errorf("after CONNECTED got %+v, want SUBSCRIBE to /ws-subscribe/user/u42/notify with ack:auto", s)
		}
		var hb string
		if err := websocket.Message.Receive(p.ws, &hb); err != nil || hb != "\n" {
			t.Errorf("heartbeat = %q, %v, want \"\\n\"", hb, err)
		}
		p.send(notify("SN1"))
		p.send("MESSAGE\n\nnot json\x00")
		p.send(notify("SN2"))
		p.next() // wait until the client goes away
	})

	ctx, cancel := context.WithCancel(t.Context())
	var got []string
	err := newWatchClient(gwURL, nil, Token{AccessToken: "acc"}).Watch(ctx, func(sn string) {
		got = append(got, sn)
		if sn == "SN2" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Watch() after cancel error = %v, want context.Canceled", err)
	}
	if !slices.Equal(got, []string{"SN1", "SN2"}) {
		t.Errorf("Watch() callbacks = %v, want [SN1 SN2]", got)
	}
}

func TestWatchReconnects(t *testing.T) {
	tests := []struct {
		name    string
		session func(n int, p stompPeer)
	}{
		{
			name: "CONNECTED without user-name",
			session: func(n int, p stompPeer) {
				p.next()
				if n == 1 {
					p.send("CONNECTED\nversion:1.2\n\n\x00")
					p.next()
					return
				}
				p.send(connected)
				p.next()
				p.send(notify("SN1"))
				p.next()
			},
		},
		{
			name: "silent server",
			session: func(n int, p stompPeer) {
				p.next()
				if n == 1 {
					p.send("CONNECTED\nheart-beat:20,0\nuser-name:u42\n\n\x00") // promises heartbeats, sends none
					p.next()
					time.Sleep(time.Second)
					return
				}
				p.send(connected)
				p.next()
				p.send(notify("SN1"))
				p.next()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwURL, attempts := fakeWatchGateway(t, nil, tt.session)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := newWatchClient(gwURL, nil, Token{AccessToken: "acc"}).Watch(ctx, func(string) { cancel() })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Watch() error = %v, want context.Canceled after the notification", err)
			}
			if n := len(attempts()); n < 2 {
				t.Errorf("Watch() made %d connections, want a reconnect", n)
			}
		})
	}
}

func TestWatchKeepAlive(t *testing.T) {
	got := make(chan string, 1)
	gwURL, _ := fakeWatchGateway(t, nil, func(_ int, p stompPeer) {
		p.next()
		p.send("CONNECTED\nheart-beat:0,0\nuser-name:u42\n\n\x00") // what the BLUETTI gateway sends
		p.next()
		var msg string
		_ = websocket.Message.Receive(p.ws, &msg) // the test checks what arrived
		got <- msg
		p.next()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-got
		cancel()
	}()
	start := time.Now()
	_ = newWatchClient(gwURL, nil, Token{AccessToken: "acc"}).Watch(ctx, func(string) {}) // ends with ctx
	if elapsed := time.Since(start); elapsed < keepAliveSteps*20*time.Millisecond {
		t.Errorf("keep-alive after heart-beat:0,0 came after %v, want about %v", elapsed, keepAliveSteps*20*time.Millisecond)
	}
}

func TestWatchBackoff(t *testing.T) {
	gwURL, attempts := fakeWatchGateway(t, func(n int) bool { return n <= 2 || n == 4 }, func(n int, p stompPeer) {
		p.next()
		p.send(connected)
		p.next()
		if n == 5 {
			p.send(notify("SN1"))
		}
		// attempt 3 drops right after CONNECTED and SUBSCRIBE
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c := newWatchClient(gwURL, nil, Token{AccessToken: "acc"})
	c.backoff = 100 * time.Millisecond
	if err := c.Watch(ctx, func(string) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch() error = %v, want context.Canceled", err)
	}
	at := attempts()
	if len(at) != 5 {
		t.Fatalf("Watch() made %d attempts, want 5", len(at))
	}
	gaps := make([]time.Duration, 4)
	for i := range gaps {
		gaps[i] = at[i+1].Sub(at[i])
	}
	// failed, failed: 1x then 2x; then a session that reached CONNECTED resets to 1x.
	if gaps[0] < 100*time.Millisecond || gaps[1] < 200*time.Millisecond {
		t.Errorf("gaps after two failed dials = %v, %v, want >= 100ms, >= 200ms", gaps[0], gaps[1])
	}
	if gaps[2] < 100*time.Millisecond || gaps[2] >= 200*time.Millisecond {
		t.Errorf("gap after a CONNECTED session = %v, want reset to about 100ms", gaps[2])
	}
}

func TestWatchAuthError(t *testing.T) {
	const tokenError = "ERROR\nmessage:{\"msgCode\"\\c805,\"message\"\\c\"token expired\"}\n\n\x00"
	tests := []struct {
		name      string
		ssoStatus int
		ssoBody   string
		wantLogin bool
	}{
		{name: "renewed", ssoStatus: http.StatusOK, ssoBody: `{"access_token":"new","expires_in":3600}`},
		{name: "refresh rejected", ssoStatus: http.StatusBadRequest, ssoBody: `{"error":"invalid_grant"}`, wantLogin: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.ssoStatus)
				_, _ = w.Write([]byte(tt.ssoBody)) // the test reads Watch's result
			}))
			defer sso.Close()
			var auths []string
			var mu sync.Mutex
			gwURL, _ := fakeWatchGateway(t, nil, func(n int, p stompPeer) {
				c := p.next()
				mu.Lock()
				auths = append(auths, c.headers["Authorization"])
				mu.Unlock()
				if n == 1 {
					p.send(tokenError)
					p.next()
					return
				}
				p.send(connected)
				p.next()
				p.send(notify("SN1"))
				p.next()
			})
			c := newWatchClient(gwURL, &Auth{SSOURL: sso.URL}, Token{AccessToken: "old", RefreshToken: "ref"})
			var saved atomic.Int32
			c.OnToken = func(*Token) { saved.Add(1) }
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := c.Watch(ctx, func(string) { cancel() })

			if tt.wantLogin {
				if !errors.Is(err, ErrLoginRequired) {
					t.Errorf("Watch() error = %v, want ErrLoginRequired", err)
				}
				return
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Watch() error = %v, want context.Canceled after the notification", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(auths, []string{"old", "new"}) || saved.Load() != 1 {
				t.Errorf("CONNECT tokens = %v, OnToken calls = %d, want [old new] and 1", auths, saved.Load())
			}
		})
	}
}

func TestWatchURL(t *testing.T) {
	tests := []struct {
		base, want string
	}{
		{"https://gw.bluettipower.com", "wss://gw.bluettipower.com/api/edgeiotgw/ws-coordination/websocket"},
		{"http://127.0.0.1:8080/", "ws://127.0.0.1:8080/api/edgeiotgw/ws-coordination/websocket"},
	}
	for _, tt := range tests {
		if got, err := watchURL(tt.base); err != nil || got != tt.want {
			t.Errorf("watchURL(%q) = %q, %v, want %q", tt.base, got, err, tt.want)
		}
	}
	c := NewClient(nil, &Token{})
	if got := c.apiBase(c.tok); !strings.HasPrefix(got, "https://gw.bluettipower.com") {
		t.Errorf("apiBase(no host, no gateway) = %q, want https://gw.bluettipower.com", got)
	}
}

func TestWatchUpdatesWithoutREST(t *testing.T) {
	push := readPush(t, "realtime-main")
	var rest atomic.Int32
	ws := websocket.Handler(func(ws *websocket.Conn) {
		p := stompPeer{t: t, ws: ws}
		p.next()
		p.send(connected)
		p.next()
		p.send("MESSAGE\ndestination:/ws-subscribe/user/u42/notify\n\n" + push + "\x00")
		p.next()
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/edgeiotgw/ws-coordination/websocket" {
			ws.ServeHTTP(w, r)
			return
		}
		rest.Add(1)
		<-r.Context().Done() // the REST API hangs
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var got Update
	err := newWatchClient(srv.URL, nil, Token{AccessToken: "acc"}).WatchUpdates(ctx, func(u Update) {
		got = u
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("WatchUpdates() error = %v, want context.Canceled after the push", err)
	}
	if got.SN != "SN1" || got.Values[FnSOC] != "80" || got.Values[FnACLoadPower] != "480" {
		t.Errorf("WatchUpdates() update = %+v, want SN1 with SOC 80 and AC load 480", got)
	}
	if n := rest.Load(); n != 0 {
		t.Errorf("WatchUpdates() made %d REST requests, want 0", n)
	}
}

func TestWatchSilentSession(t *testing.T) {
	gwURL, attempts := fakeWatchGateway(t, nil, func(n int, p stompPeer) {
		p.next()
		if n == 1 {
			p.send("CONNECTED\nheart-beat:0,0\nuser-name:u42\n\n\x00") // then nothing, as a dead gateway session
			p.next()
			p.next() // reads keep-alives until the client drops the session
			return
		}
		p.send(connected)
		p.next()
		p.send(notify("SN1"))
		p.next()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := newWatchClient(gwURL, nil, Token{AccessToken: "acc"}).Watch(ctx, func(string) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch() error = %v, want context.Canceled after the notification", err)
	}
	at := attempts()
	if len(at) < 2 {
		t.Fatalf("Watch() made %d connections, want a reconnect after the silence", len(at))
	}
	if gap, want := at[1].Sub(at[0]), silentSteps*20*time.Millisecond; gap < want || gap > 3*want {
		t.Errorf("reconnect after a silent heart-beat:0,0 session came after %v, want about %v", gap, want)
	}
}
