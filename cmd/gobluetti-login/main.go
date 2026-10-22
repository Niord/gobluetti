// The gobluetti-login command logs in to a BLUETTI cloud account in the
// browser and writes the OAuth2 token as JSON, or refreshes a stored token.
//
// Usage:
//
//	gobluetti-login [-o file]
//	gobluetti-login -refresh file [-o file]
//
// Without -o the token goes to stdout; with -o it goes to the file, created
// with mode 0600. The token grants access to the account: keep it private.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"

	"github.com/Niord/gobluetti"
	"github.com/Niord/gobluetti/internal/tokenfile"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	openBrowser := func(url string) error {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.CommandContext(ctx, "open", url) //nolint:gosec // url comes from Auth.AuthCodeURL and is one argv entry, no shell
		case "windows":
			cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // same as above
		default:
			cmd = exec.CommandContext(ctx, "xdg-open", url) //nolint:gosec // same as above
		}
		return cmd.Run()
	}
	err := run(ctx, &gobluetti.Auth{}, openBrowser, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gobluetti-login:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, auth *gobluetti.Auth, openBrowser func(url string) error,
	args []string, stdout, stderr io.Writer,
) error {
	fs := flag.NewFlagSet("gobluetti-login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the token to `file` (mode 0600) instead of stdout")
	refreshPath := fs.String("refresh", "", "refresh the token stored in `file` instead of logging in;\nthe result replaces it unless -o is given")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var tok *gobluetti.Token
	if *refreshPath != "" {
		old, err := tokenfile.Load(*refreshPath)
		if err != nil {
			return err
		}
		if tok, err = auth.Refresh(ctx, old); err != nil {
			return err
		}
		*out = cmp.Or(*out, *refreshPath)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		var err error
		tok, err = auth.LoginLocal(ctx, func(url string) error {
			_, _ = fmt.Fprintf(stderr, "Log in to BLUETTI in the browser. If it does not open, visit:\n\n  %s\n\n", url) // stderr is the only place to report to
			if err := openBrowser(url); err != nil {
				_, _ = fmt.Fprintf(stderr, "Could not open a browser (%v); waiting for the login anyway.\n", err) // same
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	if *out == "" {
		if _, err := stdout.Write(tokenfile.Encode(tok)); err != nil {
			return err
		}
	} else if err := tokenfile.Save(*out, tok); err != nil {
		return err
	}

	expiry := "unknown"
	if !tok.Expiry.IsZero() {
		expiry = tok.Expiry.Local().Format(time.RFC3339)
	}
	hasRefresh := "no"
	if tok.RefreshToken != "" {
		hasRefresh = "yes"
	}
	_, _ = fmt.Fprintf(stderr, "Token written: expires %s, host %s, refresh token: %s\n",
		expiry, cmp.Or(tok.Host, "unknown"), hasRefresh) // the token is already written
	return nil
}
