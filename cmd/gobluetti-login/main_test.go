package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Niord/gobluetti"
)

const (
	newAccess  = "access-new-5d1e"
	newRefresh = "refresh-new-0a4c"
	oldToken   = `{"access_token":"access-old","refresh_token":"refresh-old","host":"https://eu.example"}`
)

func fakeSSO(t *testing.T, status int, body string) *gobluetti.Auth {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) // the test reads run's result, not this write
	}))
	t.Cleanup(srv.Close)
	return &gobluetti.Auth{SSOURL: srv.URL, HTTPClient: srv.Client()}
}

// browser returns an openBrowser that follows the authorize URL's redirect with a code,
// as SSO would after a successful login, then reports openErr.
func browser(t *testing.T, openErr error) func(string) error {
	return func(authorizeURL string) error {
		u, err := url.Parse(authorizeURL)
		if err != nil {
			t.Errorf("authorize URL %q: %v", authorizeURL, err)
			return err
		}
		callback := u.Query().Get("redirect_uri") + "?" + url.Values{
			"code":  {"code-1"},
			"state": {u.Query().Get("state")},
		}.Encode()
		// The login may finish and close the server before this request reads its response,
		// so errors here are not failures: run's result shows whether the callback arrived.
		go func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, callback, nil)
			if err != nil {
				return
			}
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return openErr
	}
}

func readToken(t *testing.T, data []byte) gobluetti.Token {
	t.Helper()
	var tok gobluetti.Token
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&tok); err != nil {
		t.Fatalf("decode token %q: %v", data, err)
	}
	if dec.More() {
		t.Errorf("token output %q has more than one JSON value", data)
	}
	return tok
}

func assertSummary(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "host https://eu.example") || !strings.Contains(stderr, "refresh token: yes") {
		t.Errorf("stderr = %q, want a summary with the host and \"refresh token: yes\"", stderr)
	}
	for _, secret := range []string{newAccess, newRefresh} {
		if strings.Contains(stderr, secret) {
			t.Errorf("stderr = %q, contains token value %q", stderr, secret)
		}
	}
}

// seed writes content with mode 0644, so a test can show that run replaces it with 0600.
func seed(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // 0644 on purpose, see above
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s mode = %v, want 0600", path, mode)
	}
}

const loginResponse = `{"access_token":"` + newAccess + `","refresh_token":"` + newRefresh +
	`","expires_in":3600,"host":"https://eu.example"}`

func TestRunLoginToStdout(t *testing.T) {
	for _, openErr := range []error{nil, errors.New("no browser")} {
		auth := fakeSSO(t, http.StatusOK, loginResponse)
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), auth, browser(t, openErr), nil, &stdout, &stderr); err != nil {
			t.Fatalf("run(no args, openBrowser error %v) error = %v, stderr %q", openErr, err, stderr.String())
		}
		if tok := readToken(t, stdout.Bytes()); tok.AccessToken != newAccess || tok.RefreshToken != newRefresh {
			t.Errorf("run(no args) stdout token = %+v, want access %s, refresh %s", tok, newAccess, newRefresh)
		}
		if !strings.Contains(stderr.String(), "/oauth2/grant?") {
			t.Errorf("run(no args) stderr = %q, want the authorize URL", stderr.String())
		}
		assertSummary(t, stderr.String())
	}
}

func TestRunLoginToFile(t *testing.T) {
	auth := fakeSSO(t, http.StatusOK, loginResponse)
	path := filepath.Join(t.TempDir(), "token.json")
	seed(t, path, "stale")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), auth, browser(t, nil), []string{"-o", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run(-o %s) error = %v", path, err)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(-o %s) stdout = %q, want empty", path, stdout.String())
	}
	data := read(t, path)
	if tok := readToken(t, data); tok.AccessToken != newAccess {
		t.Errorf("run(-o %s) file token = %+v, want access %s", path, tok, newAccess)
	}
	assertMode0600(t, path)
	assertSummary(t, stderr.String())
}

func TestRunRefresh(t *testing.T) {
	auth := fakeSSO(t, http.StatusOK, `{"access_token":"`+newAccess+`","expires_in":3600}`)
	path := filepath.Join(t.TempDir(), "token.json")
	seed(t, path, oldToken)
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), auth, nil, []string{"-refresh", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run(-refresh %s) error = %v", path, err)
	}
	data := read(t, path)
	if tok := readToken(t, data); tok.AccessToken != newAccess || tok.RefreshToken != "refresh-old" {
		t.Errorf("run(-refresh) file token = %+v, want access %s and the old refresh token", tok, newAccess)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(-refresh) stdout = %q, want empty", stdout.String())
	}
	assertMode0600(t, path)
	assertSummary(t, stderr.String())
}

func TestRunRefreshRejected(t *testing.T) {
	auth := fakeSSO(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
	path := filepath.Join(t.TempDir(), "token.json")
	seed(t, path, oldToken)
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), auth, nil, []string{"-refresh", path}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("run(-refresh, rejected) error = %v, want an error naming invalid_grant", err)
	}
	data := read(t, path)
	if string(data) != oldToken {
		t.Errorf("run(-refresh, rejected) changed the file to %q, want %q", data, oldToken)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("run(-refresh, rejected) left %d files in the directory, want 1", len(entries))
	}
}

func TestRunHelpAndBadArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), &gobluetti.Auth{}, nil, []string{"-h"}, &stdout, &stderr); err != nil {
		t.Errorf("run(-h) error = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "-refresh") {
		t.Errorf("run(-h) stderr = %q, want usage with -refresh", stderr.String())
	}
	if err := run(t.Context(), &gobluetti.Auth{}, nil, []string{"extra"}, &stdout, &stderr); err == nil {
		t.Error("run(extra) error = nil, want error for an unexpected argument")
	}
}
