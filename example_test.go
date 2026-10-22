package gobluetti_test

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Niord/gobluetti"
)

// The browser must run on the same machine as the program.
func ExampleAuth_LoginLocal() {
	var auth gobluetti.Auth // zero value: BLUETTI SSO with the default client identity

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tok, err := auth.LoginLocal(ctx, func(url string) error {
		fmt.Println("Open this URL to log in:", url)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Store tok (it marshals to JSON) and renew it before it expires:
	tok, err = auth.Refresh(ctx, tok)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("expires:", tok.Expiry)
}

// A web application uses its own redirect URL instead of LoginLocal.
func ExampleAuth_Exchange() {
	var auth gobluetti.Auth
	const redirectURI = "https://app.example.com/bluetti/callback"
	state := rand.Text() // per login; keep it in the user's session

	http.HandleFunc("GET /bluetti/login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, auth.AuthCodeURL(redirectURI, state), http.StatusFound)
	})
	http.HandleFunc("GET /bluetti/callback", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		tok, err := auth.Exchange(r.Context(), r.URL.Query().Get("code"), redirectURI)
		if err != nil {
			http.Error(w, "login failed", http.StatusBadGateway)
			return
		}
		_ = tok // store the token for this user
	})
}
