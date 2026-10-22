package gobluetti_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// visit plays the browser: it follows the authorize URL's redirect_uri with the given query
// and reports the callback page.
func visit(t *testing.T, authorizeURL string, query func(state string) url.Values) (redirect *url.URL, page string) {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL %q: %v", authorizeURL, err)
	}
	redirect, err = url.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("redirect_uri in %q: %v", authorizeURL, err)
	}
	callback := *redirect
	callback.RawQuery = query(u.Query().Get("state")).Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, callback.String(), nil)
	if err != nil {
		t.Fatalf("callback request %s: %v", callback.Redacted(), err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", callback.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }() // read in full below
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read callback page: %v", err)
	}
	return redirect, string(body)
}

func assertClosed(t *testing.T, redirect *url.URL) {
	t.Helper()
	if redirect == nil {
		t.Fatal("callback URL was never seen")
	}
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", redirect.Host); err == nil {
		_ = conn.Close() // the test already failed; the close result adds nothing
		t.Errorf("LoginLocal() left %s listening", redirect.Host)
	}
}

func TestLoginLocal(t *testing.T) {
	sso, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"acc","refresh_token":"ref","expires_in":60}`)
	var redirect *url.URL
	var page string
	tok, err := auth.LoginLocal(t.Context(), func(authorizeURL string) error {
		redirect, page = visit(t, authorizeURL, func(state string) url.Values {
			return url.Values{"code": {testCode}, "state": {state}}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("LoginLocal() error = %v", err)
	}
	if tok.AccessToken != "acc" || tok.RefreshToken != "ref" {
		t.Errorf("LoginLocal() = %+v, want access acc, refresh ref", tok)
	}
	if host := redirect.Hostname(); host != "127.0.0.1" {
		t.Errorf("LoginLocal() redirect host = %q, want 127.0.0.1", host)
	}
	if redirect.Path != "/callback" || redirect.Port() == "" {
		t.Errorf("LoginLocal() redirect = %s, want http://127.0.0.1:<port>/callback", redirect)
	}
	if !strings.Contains(page, "close this window") {
		t.Errorf("LoginLocal() callback page = %q, want a close-this-window message", page)
	}
	forms := sso.requests()
	if len(forms) != 1 || forms[0].Get("code") != testCode || forms[0].Get("redirect_uri") != redirect.String() {
		t.Errorf("LoginLocal() token requests = %v, want one exchange of %q with redirect_uri %s", forms, testCode, redirect)
	}
	assertClosed(t, redirect)
}

func TestLoginLocalFailures(t *testing.T) {
	tests := []struct {
		name    string
		query   func(state string) url.Values
		wantErr string
	}{
		{
			name:    "mismatched state",
			query:   func(string) url.Values { return url.Values{"code": {testCode}, "state": {"forged"}} },
			wantErr: "state",
		},
		{
			name:    "SSO error",
			query:   func(string) url.Values { return url.Values{"error": {"access_denied"}} },
			wantErr: "access_denied",
		},
		{
			name:    "missing code",
			query:   func(state string) url.Values { return url.Values{"state": {state}} },
			wantErr: "code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sso, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"acc"}`)
			var redirect *url.URL
			_, err := auth.LoginLocal(t.Context(), func(authorizeURL string) error {
				redirect, _ = visit(t, authorizeURL, tt.query)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("LoginLocal() error = %v, want error mentioning %q", err, tt.wantErr)
			}
			if n := len(sso.requests()); n != 0 {
				t.Errorf("LoginLocal() sent %d token requests, want 0", n)
			}
			assertClosed(t, redirect)
		})
	}
}

func TestLoginLocalTimeout(t *testing.T) {
	_, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"acc"}`)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	var redirect *url.URL
	_, err := auth.LoginLocal(ctx, func(authorizeURL string) error {
		u, _ := url.Parse(authorizeURL) // AuthCodeURL output always parses
		redirect, _ = url.Parse(u.Query().Get("redirect_uri"))
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("LoginLocal(50ms deadline, no callback) error = %v, want context.DeadlineExceeded", err)
	}
	assertClosed(t, redirect)
}

func TestLoginLocalOpenURLError(t *testing.T) {
	_, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"acc"}`)
	errNoBrowser := errors.New("no browser")
	var redirect *url.URL
	_, err := auth.LoginLocal(t.Context(), func(authorizeURL string) error {
		u, _ := url.Parse(authorizeURL) // AuthCodeURL output always parses
		redirect, _ = url.Parse(u.Query().Get("redirect_uri"))
		return errNoBrowser
	})
	if !errors.Is(err, errNoBrowser) {
		t.Errorf("LoginLocal(failing openURL) error = %v, want %v", err, errNoBrowser)
	}
	assertClosed(t, redirect)
}
