package gobluetti_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Niord/gobluetti"
)

const (
	upstreamClientSecret = "SG9tZUFzc2lzdGFudA==" //nolint:gosec // public value published by upstream
	testCode             = "code-7f3a"
	testRefreshToken     = "refresh-91bc"
)

// fakeSSO answers /oauth2/token with a fixed status and body and records every form it receives.
type fakeSSO struct {
	status int
	body   string

	mu    sync.Mutex
	forms []url.Values
}

func newFakeSSO(t *testing.T, status int, body string) (*fakeSSO, *gobluetti.Auth) {
	t.Helper()
	f := &fakeSSO{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("fake SSO: ParseForm() error = %v", err)
		}
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body)) // the test reads the client's result, not this write
	}))
	t.Cleanup(srv.Close)
	return f, &gobluetti.Auth{SSOURL: srv.URL, HTTPClient: srv.Client()}
}

func (f *fakeSSO) requests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms
}

func TestAuthCodeURL(t *testing.T) {
	tests := []struct {
		name         string
		auth         gobluetti.Auth
		wantBase     string
		wantClientID string
	}{
		{name: "zero value", wantBase: "https://sso.bluettipower.com/oauth2/grant", wantClientID: "HomeAssistant"},
		{
			name:         "custom SSO only",
			auth:         gobluetti.Auth{SSOURL: "http://127.0.0.1:9000"},
			wantBase:     "http://127.0.0.1:9000/oauth2/grant",
			wantClientID: "HomeAssistant",
		},
		{
			name:         "custom client ID only",
			auth:         gobluetti.Auth{ClientID: "my-app"},
			wantBase:     "https://sso.bluettipower.com/oauth2/grant",
			wantClientID: "my-app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.auth.AuthCodeURL("http://127.0.0.1:8765/callback", "abc")
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("AuthCodeURL() = %q, not a URL: %v", raw, err)
			}
			if got := u.Scheme + "://" + u.Host + u.Path; got != tt.wantBase {
				t.Errorf("AuthCodeURL() base = %q, want %q", got, tt.wantBase)
			}
			want := url.Values{
				"response_type": {"code"},
				"client_id":     {tt.wantClientID},
				"redirect_uri":  {"http://127.0.0.1:8765/callback"},
				"state":         {"abc"},
			}
			if got := u.Query(); got.Encode() != want.Encode() {
				t.Errorf("AuthCodeURL() query = %v, want %v", got, want)
			}
			if !strings.Contains(raw, "redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcallback") {
				t.Errorf("AuthCodeURL() = %q, want URL-encoded redirect_uri", raw)
			}
		})
	}
}

func TestExchange(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantExpiry time.Duration // 0 means a zero Expiry
		wantErr    bool
	}{
		{
			name:       "numeric expires_in",
			body:       `{"access_token":"acc","refresh_token":"ref","expires_in":3600,"host":"https://eu.example"}`,
			wantExpiry: time.Hour,
		},
		{
			name:       "string expires_in",
			body:       `{"access_token":"acc","refresh_token":"ref","expires_in":"3600","host":"https://eu.example"}`,
			wantExpiry: time.Hour,
		},
		{
			name: "missing expires_in",
			body: `{"access_token":"acc","refresh_token":"ref","host":"https://eu.example"}`,
		},
		{name: "missing access_token", body: `{"refresh_token":"ref","expires_in":3600}`, wantErr: true},
		{name: "negative expires_in", body: `{"access_token":"acc","expires_in":-1}`, wantErr: true},
		{name: "not JSON", body: `<html>ok</html>`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sso, auth := newFakeSSO(t, http.StatusOK, tt.body)
			before := time.Now()
			tok, err := auth.Exchange(t.Context(), testCode, "http://127.0.0.1:8765/callback")
			after := time.Now()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Exchange() with body %s = %+v, want error", tt.body, tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("Exchange() with body %s error = %v", tt.body, err)
			}

			forms := sso.requests()
			if len(forms) != 1 {
				t.Fatalf("Exchange() sent %d token requests, want 1", len(forms))
			}
			wantForm := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {testCode},
				"redirect_uri":  {"http://127.0.0.1:8765/callback"},
				"client_id":     {"HomeAssistant"},
				"client_secret": {upstreamClientSecret},
			}
			if forms[0].Encode() != wantForm.Encode() {
				t.Errorf("Exchange() form = %v, want %v", forms[0], wantForm)
			}

			if tok.AccessToken != "acc" || tok.RefreshToken != "ref" || tok.Host != "https://eu.example" {
				t.Errorf("Exchange() = %+v, want access acc, refresh ref, host https://eu.example", tok)
			}
			if tt.wantExpiry == 0 {
				if !tok.Expiry.IsZero() {
					t.Errorf("Exchange() Expiry = %v, want zero", tok.Expiry)
				}
				return
			}
			if tok.Expiry.Before(before.Add(tt.wantExpiry)) || tok.Expiry.After(after.Add(tt.wantExpiry)) {
				t.Errorf("Exchange() Expiry = %v, want receive time + %v", tok.Expiry, tt.wantExpiry)
			}
		})
	}
}

func TestRefresh(t *testing.T) {
	old := &gobluetti.Token{
		AccessToken:  "old-acc",
		RefreshToken: testRefreshToken,
		Expiry:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Host:         "https://eu.example",
	}
	tests := []struct {
		name           string
		body           string
		wantRefresh    string
		wantHost       string
		wantZeroExpiry bool
	}{
		{
			name:        "rotated refresh token",
			body:        `{"access_token":"new-acc","refresh_token":"new-ref","expires_in":7200,"host":"https://us.example"}`,
			wantRefresh: "new-ref",
			wantHost:    "https://us.example",
		},
		{
			name:        "no refresh_token and no host",
			body:        `{"access_token":"new-acc","expires_in":7200}`,
			wantRefresh: testRefreshToken,
			wantHost:    "https://eu.example",
		},
		{
			name:           "no expires_in",
			body:           `{"access_token":"new-acc"}`,
			wantRefresh:    testRefreshToken,
			wantHost:       "https://eu.example",
			wantZeroExpiry: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sso, auth := newFakeSSO(t, http.StatusOK, tt.body)
			before := time.Now()
			tok, err := auth.Refresh(t.Context(), old)
			if err != nil {
				t.Fatalf("Refresh() with body %s error = %v", tt.body, err)
			}
			forms := sso.requests()
			if len(forms) != 1 {
				t.Fatalf("Refresh() sent %d token requests, want 1", len(forms))
			}
			wantForm := url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {testRefreshToken},
				"client_id":     {"HomeAssistant"},
				"client_secret": {upstreamClientSecret},
			}
			if forms[0].Encode() != wantForm.Encode() {
				t.Errorf("Refresh() form = %v, want %v", forms[0], wantForm)
			}
			if tok.AccessToken != "new-acc" || tok.RefreshToken != tt.wantRefresh || tok.Host != tt.wantHost {
				t.Errorf("Refresh() = %+v, want access new-acc, refresh %s, host %s", tok, tt.wantRefresh, tt.wantHost)
			}
			if tt.wantZeroExpiry != tok.Expiry.IsZero() || !tt.wantZeroExpiry && tok.Expiry.Before(before.Add(2*time.Hour)) {
				t.Errorf("Refresh() Expiry = %v, want zero = %t, otherwise about now + 2h", tok.Expiry, tt.wantZeroExpiry)
			}
			if old.AccessToken != "old-acc" {
				t.Errorf("Refresh() modified its argument: %+v", old)
			}
		})
	}
}

func TestRefreshWithoutRefreshToken(t *testing.T) {
	sso, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"new-acc"}`)
	if _, err := auth.Refresh(t.Context(), &gobluetti.Token{AccessToken: "acc"}); err == nil {
		t.Error("Refresh(token without refresh token) error = nil, want error")
	}
	if n := len(sso.requests()); n != 0 {
		t.Errorf("Refresh(token without refresh token) sent %d requests, want 0", n)
	}
}

func TestTokenErrors(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		body            string
		wantCode        string
		wantDescription string
	}{
		{name: "invalid_grant", status: 400, body: `{"error":"invalid_grant"}`, wantCode: "invalid_grant"},
		{
			name:            "error with description",
			status:          401,
			body:            `{"error":"invalid_client","error_description":"unknown client"}`,
			wantCode:        "invalid_client",
			wantDescription: "unknown client",
		},
		{name: "HTML body", status: 500, body: `<html><body>Internal Server Error</body></html>`},
		{name: "oversized body", status: 502, body: `{"error":"` + strings.Repeat("x", 70<<10) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, auth := newFakeSSO(t, tt.status, tt.body)
			for op, call := range map[string]func() error{
				"Exchange": func() error { _, err := auth.Exchange(t.Context(), testCode, "http://127.0.0.1/cb"); return err },
				"Refresh": func() error {
					_, err := auth.Refresh(t.Context(), &gobluetti.Token{RefreshToken: testRefreshToken})
					return err
				},
			} {
				err := call()
				oauthErr, ok := errors.AsType[*gobluetti.OAuthError](err)
				if !ok {
					t.Fatalf("%s() error = %v, want *OAuthError", op, err)
				}
				if oauthErr.StatusCode != tt.status || oauthErr.Code != tt.wantCode || oauthErr.Description != tt.wantDescription {
					t.Errorf("%s() error = %+v, want status %d, code %q, description %q",
						op, oauthErr, tt.status, tt.wantCode, tt.wantDescription)
				}
				for _, secret := range []string{testCode, testRefreshToken, upstreamClientSecret} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("%s() error %q contains secret %q", op, err, secret)
					}
				}
			}
		})
	}
}

func TestTokenRequestCanceled(t *testing.T) {
	_, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"acc"}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := auth.Exchange(ctx, testCode, "http://127.0.0.1/cb"); !errors.Is(err, context.Canceled) {
		t.Errorf("Exchange(canceled ctx) error = %v, want context.Canceled", err)
	}
	if _, err := auth.Refresh(ctx, &gobluetti.Token{RefreshToken: testRefreshToken}); !errors.Is(err, context.Canceled) {
		t.Errorf("Refresh(canceled ctx) error = %v, want context.Canceled", err)
	}
}

func TestTokenJSON(t *testing.T) {
	want := gobluetti.Token{
		AccessToken:  "acc",
		RefreshToken: "ref",
		Expiry:       time.Date(2026, 10, 22, 12, 30, 0, 0, time.UTC),
		Host:         "https://eu.example",
	}
	data, err := json.Marshal(want) //nolint:gosec // serializing the token is the contract under test
	if err != nil {
		t.Fatalf("json.Marshal(%+v) error = %v", want, err)
	}
	if !strings.Contains(string(data), `"expiry":"2026-10-22T12:30:00Z"`) {
		t.Errorf("json.Marshal(%+v) = %s, want expiry in RFC 3339", want, data)
	}
	var got gobluetti.Token
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", data, err)
	}
	if got != want {
		t.Errorf("JSON round trip = %+v, want %+v", got, want)
	}
}
