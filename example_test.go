package gobluetti_test

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"maps"
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

func ExampleClient() {
	var tok *gobluetti.Token // loaded from where OnToken stored it last time
	c := gobluetti.NewClient(nil, tok)
	c.OnToken = func(tok *gobluetti.Token) {
		// Persist every new token: SSO rotates refresh tokens.
	}

	ctx := context.Background()
	devs, err := c.Devices(ctx)
	if err != nil {
		log.Fatal(err) // errors.Is(err, gobluetti.ErrLoginRequired): log in again
	}
	for _, d := range devs {
		soc, _ := d.State(gobluetti.FnSOC)
		fmt.Println(d.Model, d.Online, soc.Value)
	}

	// Turn the AC output off. This switches the real station.
	if err := c.Set(ctx, devs[0].SN, gobluetti.FnACOutput, "0"); err != nil {
		log.Fatal(err)
	}
}

// The callback hands each push to its own goroutine and returns at once: the
// cloud's REST API can hang while pushes keep arriving.
func ExampleClient_WatchUpdates() {
	var c *gobluetti.Client // from NewClient

	updates := make(chan gobluetti.Update, 16)
	go func() {
		last := map[string]map[string]string{} // sn → function code → value
		for u := range updates {
			if last[u.SN] == nil {
				last[u.SN] = map[string]string{}
			}
			maps.Copy(last[u.SN], u.Values) // a push carries only some readings
			fmt.Println(u.SN, "SOC", last[u.SN][gobluetti.FnSOC], "at", u.Time)
		}
	}()

	err := c.WatchUpdates(context.Background(), func(u gobluetti.Update) {
		select {
		case updates <- u:
		default: // the consumer is behind; the next push brings fresh readings
		}
	})
	log.Fatal(err) // ErrLoginRequired, or ctx ended
}

// Watch reports only which station changed. Read its state outside the
// callback, so a hanging REST call does not hold up later notifications.
func ExampleClient_Watch() {
	var c *gobluetti.Client // from NewClient

	ctx := context.Background()
	changed := make(chan string, 16)
	go func() {
		for sn := range changed {
			dev, err := c.Device(ctx, sn)
			if err != nil {
				log.Print(err)
				continue
			}
			soc, _ := dev.State(gobluetti.FnSOC)
			fmt.Println(sn, "SOC", soc.Value)
		}
	}()

	err := c.Watch(ctx, func(sn string) {
		select {
		case changed <- sn:
		default: // the reader is 16 changes behind; skip this one
		}
	})
	log.Fatal(err) // ErrLoginRequired, or ctx ended
}
