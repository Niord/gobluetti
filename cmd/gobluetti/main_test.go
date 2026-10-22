package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/Niord/gobluetti"
	"github.com/Niord/gobluetti/internal/tokenfile"
)

const (
	sn1 = `{"sn":"SN1","name":"Garage","model":"AC300","online":"1","stateList":[
   {"fnCode":"SOC","fnValue":"87","fnType":"SENSOR","sensorInfo":{"unit":"%"}},
   {"fnCode":"SetCtrlWorkMode","fnValue":"1","fnType":"SELECT","supportModeValues":[{"code":"1","name":"Backup"},{"code":"2","name":"Self-consumption"}]}
 ]}`
	sn2 = `{"sn":"SN2","name":"Cabin","model":"AP300","online":"0","stateList":[]}`
)

// fakeCloud serves the gateway REST API, the notification websocket, and SSO.
// The gateway answers 805 to the access token "expired".
func fakeCloud(t *testing.T, setMsgCode int, ssoStatus int) (*gobluetti.Auth, string) {
	t.Helper()
	ws := websocket.Handler(func(ws *websocket.Conn) {
		var msg string
		_ = websocket.Message.Receive(ws, &msg) // CONNECT
		_ = websocket.Message.Send(ws, "CONNECTED\nuser-name:u1\n\n\x00")
		_ = websocket.Message.Receive(ws, &msg) // SUBSCRIBE
		_ = websocket.Message.Send(ws, "MESSAGE\n\n{\"data\":{\"message\":{\"deviceSn\":\"SN1\"}}}\x00")
		_ = websocket.Message.Receive(ws, &msg) // blocks until the client leaves
	})
	mux := http.NewServeMux()
	mux.Handle("/api/edgeiotgw/ws-coordination/websocket", ws)
	mux.HandleFunc("/api/bluiotdata/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := `{"msgId":"m","msgCode":0,"data":[` + sn1 + `,` + sn2 + `]}`
		switch {
		case r.Header.Get("Authorization") == "expired":
			reply = `{"msgId":"m","msgCode":805}`
		case r.URL.Path == "/api/bluiotdata/ha/v1/fulfillment" && setMsgCode != 0:
			reply = `{"msgId":"m9","msgCode":1001}`
		}
		_, _ = w.Write([]byte(reply)) // the test reads run's output
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(ssoStatus)
		_, _ = w.Write([]byte(`{"access_token":"renewed","refresh_token":"ref2","expires_in":3600,"error":"invalid_grant"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &gobluetti.Auth{SSOURL: srv.URL, HTTPClient: srv.Client()}, srv.URL
}

func tokenFile(t *testing.T, access string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.json")
	if err := tokenfile.Save(path, &gobluetti.Token{AccessToken: access, RefreshToken: "ref"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		setMsgCode int
		wantOut    []string
		wantErr    string
	}{
		{name: "devices", args: []string{"devices"}, wantOut: []string{"SN1", "AC300", "Garage", "online", "SN2", "AP300", "Cabin", "offline", "bound"}},
		{name: "device", args: []string{"device", "SN1"}, wantOut: []string{"SOC", "87 %", "SetCtrlWorkMode", "1=Backup 2=Self-consumption"}},
		{name: "set accepted", args: []string{"set", "SN1", "SetACECO", "1"}, wantOut: []string{"accepted"}},
		{name: "set rejected", args: []string{"set", "SN1", "SetACECO", "1"}, setMsgCode: 1001, wantErr: "1001"},
		{name: "bind", args: []string{"bind", "SN1", "SN2"}, wantOut: []string{"bound: SN1 SN2"}},
		{name: "bind without devices", args: []string{"bind"}, wantErr: "usage"},
		{name: "unknown command", args: []string{"reboot"}, wantErr: "usage"},
		{name: "missing argument", args: []string{"device"}, wantErr: "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, gw := fakeCloud(t, tt.setMsgCode, http.StatusOK)
			var stdout, stderr bytes.Buffer
			args := append([]string{"-token", tokenFile(t, "acc")}, tt.args...)
			err := run(t.Context(), auth, gw, args, &stdout, &stderr)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr) {
					t.Errorf("run(%v) error = %v, want an error mentioning %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("run(%v) error = %v, stderr %q", tt.args, err, stderr.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("run(%v) stdout = %q, want it to contain %q", tt.args, stdout.String(), want)
				}
			}
		})
	}
}

func TestRunTokenFile(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		auth, gw := fakeCloud(t, 0, http.StatusOK)
		err := run(t.Context(), auth, gw, []string{"-token", filepath.Join(t.TempDir(), "none.json"), "devices"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "gobluetti-login") {
			t.Errorf("run(missing token file) error = %v, want a hint to run gobluetti-login", err)
		}
	})
	t.Run("renewed token saved", func(t *testing.T) {
		auth, gw := fakeCloud(t, 0, http.StatusOK)
		path := tokenFile(t, "expired")
		if err := run(t.Context(), auth, gw, []string{"-token", path, "devices"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("run(devices, expired token) error = %v", err)
		}
		tok, err := tokenfile.Load(path)
		if err != nil || tok.AccessToken != "renewed" || tok.RefreshToken != "ref2" {
			t.Errorf("token file after renewal = %+v, %v, want the renewed token", tok, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("token file mode = %v, %v, want 0600", info.Mode().Perm(), err)
		}
	})
	t.Run("login required", func(t *testing.T) {
		auth, gw := fakeCloud(t, 0, http.StatusBadRequest)
		err := run(t.Context(), auth, gw, []string{"-token", tokenFile(t, "expired"), "devices"}, &bytes.Buffer{}, &bytes.Buffer{})
		if !errors.Is(err, gobluetti.ErrLoginRequired) || !strings.Contains(err.Error(), "gobluetti-login") {
			t.Errorf("run(devices, rejected refresh) error = %v, want ErrLoginRequired with a gobluetti-login hint", err)
		}
	})
}

// signalWriter calls onWrite after each write.
type signalWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	onWrite func(string)
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, _ := w.buf.Write(p) // bytes.Buffer.Write never fails
	s := w.buf.String()
	w.mu.Unlock()
	w.onWrite(s)
	return n, nil
}

func TestRunWatch(t *testing.T) {
	auth, gw := fakeCloud(t, 0, http.StatusOK)
	ctx, cancel := context.WithCancel(t.Context())
	out := &signalWriter{onWrite: func(s string) {
		if strings.Contains(s, "SN1") {
			cancel()
		}
	}}
	if err := run(ctx, auth, gw, []string{"-token", tokenFile(t, "acc"), "watch"}, out, &bytes.Buffer{}); err != nil {
		t.Errorf("run(watch) after Ctrl-C error = %v, want nil", err)
	}
	if !strings.Contains(out.buf.String(), "SN1") {
		t.Errorf("run(watch) stdout = %q, want a line with SN1", out.buf.String())
	}
}
