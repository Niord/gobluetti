// The gobluetti command lists, reads, commands, and watches BLUETTI power
// stations through the BLUETTI cloud, using a token file from gobluetti-login.
//
// Usage:
//
//	gobluetti [-token file] devices
//	gobluetti [-token file] device <sn>
//	gobluetti [-token file] set <sn> <code> <value>
//	gobluetti [-token file] watch
//	gobluetti [-token file] bind <sn>...
//
// bind authorizes cloud control of the devices, once, as the official
// integration does during setup; until then set fails with msgCode 403.
// set switches real outputs of the station; it sends exactly one command.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Niord/gobluetti"
	"github.com/Niord/gobluetti/internal/tokenfile"
)

const usage = "usage: gobluetti [-token file] devices | device <sn> | set <sn> <code> <value> | watch | bind <sn> [<sn>]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, &gobluetti.Auth{}, "", os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // library errors already start with "gobluetti:"
		os.Exit(1)
	}
}

func run(ctx context.Context, auth *gobluetti.Auth, gatewayURL string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gobluetti", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultPath, _ := tokenfile.DefaultPath() // without a home directory, -token is required
	tokenPath := flags.String("token", defaultPath, "token `file` written by gobluetti-login")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), usage) // stderr is the only place to report to
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	command, cmdArgs := flags.Arg(0), flags.Args()[min(1, flags.NArg()):]
	n, known := map[string]int{"devices": 0, "device": 1, "set": 3, "watch": 0, "bind": -1}[command] // -1: one or more
	if !known || (n >= 0 && len(cmdArgs) != n) || (n < 0 && len(cmdArgs) == 0) {
		return errors.New(usage)
	}

	tok, err := tokenfile.Load(*tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no token file at %s; run gobluetti-login -o %s first", *tokenPath, *tokenPath)
	}
	if err != nil {
		return err
	}
	client := gobluetti.NewClient(auth, tok)
	client.GatewayURL = gatewayURL
	client.OnToken = func(tok *gobluetti.Token) {
		if err := tokenfile.Save(*tokenPath, tok); err != nil {
			_, _ = fmt.Fprintf(stderr, "gobluetti: could not save the renewed token: %v\n", err) // the command itself can still succeed
		}
	}

	err = execute(ctx, client, command, cmdArgs, stdout)
	if errors.Is(err, gobluetti.ErrLoginRequired) {
		return fmt.Errorf("%w; run gobluetti-login -o %s", err, *tokenPath)
	}
	return err
}

// execute runs one subcommand; run has checked its argument count.
func execute(ctx context.Context, client *gobluetti.Client, command string, args []string, stdout io.Writer) error {
	switch command {
	case "devices":
		return listDevices(ctx, client, stdout)
	case "device":
		return showDevice(ctx, client, args[0], stdout)
	case "set":
		if err := client.Set(ctx, args[0], args[1], args[2]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "accepted: %s=%s on %s\n", args[1], args[2], args[0])
		return err
	case "bind":
		if err := client.Bind(ctx, args...); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "bound: %s\n", strings.Join(args, " "))
		return err
	}
	err := client.Watch(ctx, func(sn string) {
		_, _ = fmt.Fprintf(stdout, "%s %s changed\n", time.Now().Format(time.RFC3339), sn) // a lost line is not worth stopping for
	})
	if ctx.Err() != nil {
		return nil // Ctrl-C is the normal way to stop watching
	}
	return err
}

func listDevices(ctx context.Context, client *gobluetti.Client, stdout io.Writer) error {
	devs, err := client.Devices(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, d := range devs {
		online, bound := "offline", "unbound"
		if d.Online {
			online = "online"
		}
		if d.Bound {
			bound = "bound"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.SN, d.Model, d.Name, online, bound) // Flush reports write errors
	}
	return w.Flush()
}

func showDevice(ctx context.Context, client *gobluetti.Client, sn string, stdout io.Writer) error {
	dev, err := client.Device(ctx, sn)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, s := range dev.States {
		options := make([]string, len(s.Options))
		for i, o := range s.Options {
			options[i] = o.Code + "=" + o.Name
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Code, s.Type, strings.TrimSpace(s.Value+" "+s.Unit), strings.Join(options, " ")) // Flush reports write errors
	}
	return w.Flush()
}
