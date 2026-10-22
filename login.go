package gobluetti

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// LoginLocal runs a browser login and returns the token.
//
// It listens on 127.0.0.1 on a free port, passes the SSO login URL to openURL
// (which typically opens it in a browser), waits for SSO to redirect back to
// http://127.0.0.1:<port>/callback, and exchanges the code. The browser must
// run on the same machine. LoginLocal returns when the login completes, when
// openURL fails, or when ctx is done; the port is closed in every case.
func (a *Auth) LoginLocal(ctx context.Context, openURL func(url string) error) (*Token, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("gobluetti: login: listen: %w", err)
	}
	redirectURI := "http://" + ln.Addr().String() + "/callback"
	state := rand.Text()

	type callback struct {
		code string
		err  error
	}
	result := make(chan callback, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var cb callback
		switch {
		case q.Get("error") != "":
			cb.err = fmt.Errorf("gobluetti: login: SSO returned error %q", q.Get("error"))
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			cb.err = errors.New("gobluetti: login: callback state does not match")
		case q.Get("code") == "":
			cb.err = errors.New("gobluetti: login: callback has no code")
		default:
			cb.code = q.Get("code")
		}
		page := "This login was already handled. You can close this window."
		once.Do(func() {
			result <- cb
			page = "Login received. You can close this window."
			if cb.err != nil {
				page = "Login failed. You can close this window."
			}
		})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(page + "\n")) // a failed write is the browser's problem, not the login's
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var wg sync.WaitGroup
	wg.Go(func() { _ = srv.Serve(ln) }) // returns http.ErrServerClosed after Close
	defer wg.Wait()
	defer func() { _ = srv.Close() }() // nothing to report once the login is decided

	if err := openURL(a.AuthCodeURL(redirectURI, state)); err != nil {
		return nil, fmt.Errorf("gobluetti: login: open URL: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("gobluetti: login: %w", ctx.Err())
	case cb := <-result:
		if cb.err != nil {
			return nil, cb.err
		}
		return a.Exchange(ctx, cb.code, redirectURI)
	}
}
