package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Niord/gobluetti/ble"
)

func TestParse(t *testing.T) {
	tests := []struct {
		args string
		want command
	}{
		{"scan", command{name: "scan", timeout: 30 * time.Second}},
		{"-timeout 10s scan", command{name: "scan", timeout: 10 * time.Second}},
		{"state AC200M1", command{name: "state", station: "AC200M1", timeout: 30 * time.Second}},
		{"pack AC200M1 2", command{name: "pack", station: "AC200M1", pack: 2, timeout: 30 * time.Second}},
		{"set AC200M1 ac on", command{name: "set", station: "AC200M1", output: "ac", on: true, timeout: 30 * time.Second}},
		{"set AC200M1 dc off", command{name: "set", station: "AC200M1", output: "dc", timeout: 30 * time.Second}},
		{"set AC200M1 power off", command{name: "set", station: "AC200M1", output: "power", timeout: 30 * time.Second}},
		{"set AC200M1 sleep never", command{name: "set", station: "AC200M1", output: "sleep", sleep: ble.SleepNever, timeout: 30 * time.Second}},
	}
	for _, tt := range tests {
		got, err := parse(strings.Fields(tt.args), io.Discard)
		if err != nil {
			t.Errorf("parse(%q) error = %v", tt.args, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parse(%q) = %+v, want %+v", tt.args, got, tt.want)
		}
	}
}

// Invalid arguments fail in run before any Bluetooth call: these cases would
// otherwise scan or hang, since no adapter or station is present.
func TestRunUsage(t *testing.T) {
	for _, args := range []string{
		"",
		"devices",
		"scan extra",
		"state",
		"pack AC200M1 x",
		"pack AC200M1 4",
		"set AC200M1 ac maybe",
		"set AC200M1 xx on",
		"set AC200M1 ac",
		"set AC200M1 power on",
		"set AC200M1 sleep 2m",
		"-timeout 0s scan",
	} {
		err := run(t.Context(), strings.Fields(args), io.Discard, io.Discard)
		if err == nil || !strings.HasPrefix(err.Error(), "usage:") {
			t.Errorf("run(%q) error = %v, want the usage", args, err)
		}
	}
}

func TestFormatState(t *testing.T) {
	got := formatState(ble.State{
		DeviceType: "AC200M", Serial: 2235000000001,
		DCInputW: 120, ACInputW: 500, ACOutputW: 300, DCOutputW: 45,
		PowerGenerationKWh: 10,
		BatteryPercent:     85, BatteryVoltage: 53.24, PackSlots: 2,
		ARMVersion: "4008.05", DSPVersion: "4005.08",
		ACMode: ble.ACInverter, ACVoltageV: 230, ACCurrentA: 2, ACInverterW: 300, ACFrequencyHz: 50,
		DCInputVoltageV: 48, DCInputMeasuredW: 120, DCInputCurrentA: 2.5,
		AutoSleep: ble.SleepNever, ACOutputOn: true,
	})
	want := `device type: AC200M
serial: 2235000000001
arm version: 4008.05
dsp version: 4005.08
dc input: 120 W
ac input: 500 W
ac output: 300 W
dc output: 45 W
power generation: 10.0 kWh
battery: 85 %
battery voltage: 53.24 V
pack slots: 2
ac mode: inverter
ac voltage: 230 V
ac current: 2.0 A
ac inverter: 300 W
ac frequency: 50.0 Hz
dc input voltage: 48 V
dc input measured: 120.0 W
dc input current: 2.50 A
auto sleep: never
ac output on: on
dc output on: off
`
	if got != want {
		t.Errorf("formatState() =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPack(t *testing.T) {
	got := formatPack(ble.Pack{Number: 2, Percent: 76, Voltage: 53.12, CellVoltages: []float64{3.3, 3.31}})
	want := "pack: 2\nvoltage: 53.12 V\nbattery: 76 %\ncells: 3.30 3.31 V\n"
	if got != want {
		t.Errorf("formatPack() = %q, want %q", got, want)
	}
}
