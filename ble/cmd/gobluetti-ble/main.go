// The gobluetti-ble command finds legacy BLUETTI power stations nearby and
// reads or switches them over Bluetooth Low Energy, without the cloud.
//
// Usage:
//
//	gobluetti-ble [-timeout d] scan
//	gobluetti-ble [-timeout d] state <name>
//	gobluetti-ble [-timeout d] pack <name> <n>
//	gobluetti-ble [-timeout d] set <name> ac|dc on|off
//
// <name> is the station's advertised name as scan prints it, such as
// AC200M2235000000001. Every command stops after -timeout (30s by default) or
// on Ctrl-C; scan then prints the stations it saw.
//
// pack reads battery pack n (1 to 3). It selects that pack on the station
// first, which switches no power, and takes about 10 seconds.
//
// set switches real outputs of the station: whatever is plugged in loses or
// gains power. It sends exactly one command.
//
// On macOS the terminal needs Bluetooth access: System Settings → Privacy &
// Security → Bluetooth.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/Niord/gobluetti/ble"
)

const usage = "usage: gobluetti-ble [-timeout d] scan | state <name> | pack <name> <n> | set <name> ac|dc on|off"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// command is one parsed invocation.
type command struct {
	name    string // scan, state, pack, or set
	station string
	pack    int
	output  string // ac or dc
	on      bool
	timeout time.Duration
}

// parse checks the arguments completely, so that a mistake never reaches
// Bluetooth.
func parse(args []string, stderr io.Writer) (command, error) {
	flags := flag.NewFlagSet("gobluetti-ble", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 30*time.Second, "stop the command after `duration`")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), usage) // stderr is the only place to report to
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return command{}, err
	}
	a := flags.Args()
	cmd := command{timeout: *timeout}
	if len(a) > 0 {
		cmd.name = a[0]
	}
	if len(a) > 1 {
		cmd.station = a[1]
	}
	switch {
	case *timeout <= 0:
	case cmd.name == "scan" && len(a) == 1:
		return cmd, nil
	case cmd.name == "state" && len(a) == 2:
		return cmd, nil
	case cmd.name == "pack" && len(a) == 3:
		n, err := strconv.Atoi(a[2])
		if err == nil && n >= 1 && n <= 3 {
			cmd.pack = n
			return cmd, nil
		}
	case cmd.name == "set" && len(a) == 4 && (a[2] == "ac" || a[2] == "dc") && (a[3] == "on" || a[3] == "off"):
		cmd.output, cmd.on = a[2], a[3] == "on"
		return cmd, nil
	}
	return command{}, errors.New(usage)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.timeout)
	defer cancel()

	if cmd.name == "scan" {
		stations, err := ble.Scan(ctx)
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, s := range stations {
			fmt.Fprintf(&b, "%s\tmodel %s\tserial %s\n", s.Name, s.Model, s.Serial)
		}
		_, err = io.WriteString(stdout, b.String())
		return err
	}

	conn, err := ble.Connect(ctx, cmd.station)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }() // the command's own result is what matters

	var out string
	switch cmd.name {
	case "state":
		st, err := conn.State(ctx)
		if err != nil {
			return err
		}
		out = formatState(st)
	case "pack":
		p, err := conn.Pack(ctx, cmd.pack)
		if err != nil {
			return err
		}
		out = formatPack(p)
	case "set":
		set := conn.SetDCOutput
		if cmd.output == "ac" {
			set = conn.SetACOutput
		}
		if err := set(ctx, cmd.on); err != nil {
			return err
		}
		out = fmt.Sprintf("%s: %s output %s\n", cmd.station, strings.ToUpper(cmd.output), onOff(cmd.on))
	}
	_, err = io.WriteString(stdout, out)
	return err
}

func formatState(s ble.State) string {
	return fmt.Sprintf(`device type: %s
serial: %d
dc input: %d W
ac input: %d W
ac output: %d W
dc output: %d W
battery: %d %%
packs: %d
ac output on: %s
dc output on: %s
`, s.DeviceType, s.Serial, s.DCInputW, s.ACInputW, s.ACOutputW, s.DCOutputW,
		s.BatteryPercent, s.PackCount, onOff(s.ACOutputOn), onOff(s.DCOutputOn))
}

func formatPack(p ble.Pack) string {
	cells := make([]string, len(p.CellVoltages))
	for i, v := range p.CellVoltages {
		cells[i] = strconv.FormatFloat(v, 'f', 2, 64)
	}
	return fmt.Sprintf("pack: %d\nvoltage: %.2f V\nbattery: %d %%\ncells: %s V\n",
		p.Number, p.Voltage, p.Percent, strings.Join(cells, " "))
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
