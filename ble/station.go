package ble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"tinygo.org/x/bluetooth"
)

// A Station is a supported station seen advertising nearby.
type Station struct {
	Name   string // advertised BLE name, such as "AC200M2235000000001"; Connect takes it
	Model  string // "AC200M" for both the AC200M and the AC200MAX
	Serial string
}

// parseName recognizes the advertised name of a supported station: "AC200M"
// followed by the serial number's digits.
func parseName(name string) (Station, bool) {
	serial, ok := strings.CutPrefix(name, "AC200M")
	if !ok || serial == "" || strings.Trim(serial, "0123456789") != "" {
		return Station{}, false
	}
	return Station{Name: name, Model: "AC200M", Serial: serial}, true
}

var (
	adapter = bluetooth.DefaultAdapter
	// The adapter is enabled once per process; enabling it again fails on
	// some platforms.
	enableAdapter = sync.OnceValue(adapter.Enable)

	// GATT characteristics of legacy stations (bluetti_mqtt bluetooth/client.py).
	notifyUUID = bluetooth.New16BitUUID(0xFF01)
	writeUUID  = bluetooth.New16BitUUID(0xFF02)
)

// scan listens for advertisements until match returns true or ctx is done.
func scan(ctx context.Context, match func(bluetooth.ScanResult) bool) error {
	if err := enableAdapter(); err != nil {
		return fmt.Errorf("ble: enable the Bluetooth adapter: %w", err)
	}
	done := make(chan error, 1) // the scanning goroutine never blocks on it
	go func() {
		done <- adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			if match(r) {
				_ = a.StopScan() // fails only when the scan already stopped
			}
		})
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = adapter.StopScan() // fails only when match stopped the scan first
		return <-done
	}
}

// Scan listens for supported stations until ctx is done and returns each one
// it saw, once. Finding none is not an error. One Scan or Connect may run at a
// time, since they share the system's Bluetooth adapter.
func Scan(ctx context.Context) ([]Station, error) {
	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		found []Station
	)
	err := scan(ctx, func(r bluetooth.ScanResult) bool {
		st, ok := parseName(r.LocalName())
		if !ok {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if !seen[st.Name] {
			seen[st.Name] = true
			found = append(found, st)
		}
		return false
	})
	if err != nil {
		return nil, fmt.Errorf("ble: scan: %w", err)
	}
	return found, nil
}

// Connect scans until a station advertises exactly the given name, then
// connects to it. It fails when ctx ends before the station is seen, or when
// the station lacks the legacy GATT characteristics; an encrypted model is
// not detected here and fails on its first request instead. The caller closes
// the returned Conn.
func Connect(ctx context.Context, name string) (*Conn, error) {
	var (
		addr  bluetooth.Address
		found bool
	)
	err := scan(ctx, func(r bluetooth.ScanResult) bool {
		if r.LocalName() != name {
			return false
		}
		addr, found = r.Address, true
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("ble: scan for %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("ble: station %q not found: %w", name, context.Cause(ctx))
	}

	dev, err := adapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("ble: connect to %q: %w", name, err)
	}
	c, err := attach(dev)
	if err != nil {
		_ = dev.Disconnect() // the attach error is the one to report
		return nil, fmt.Errorf("ble: %q: %w", name, err)
	}
	return c, nil
}

// attach finds the legacy characteristics on dev and subscribes to
// notifications.
func attach(dev bluetooth.Device) (*Conn, error) {
	services, err := dev.DiscoverServices(nil)
	if err != nil {
		return nil, fmt.Errorf("discover services: %w", err)
	}
	var notify, write *bluetooth.DeviceCharacteristic
	for _, s := range services {
		chars, err := s.DiscoverCharacteristics(nil)
		if err != nil {
			return nil, fmt.Errorf("discover characteristics: %w", err)
		}
		for i := range chars {
			switch chars[i].UUID() {
			case notifyUUID:
				notify = &chars[i]
			case writeUUID:
				write = &chars[i]
			}
		}
	}
	if notify == nil || write == nil {
		return nil, errors.New("no ff01/ff02 characteristics: not a legacy BLUETTI station")
	}
	c := newConn(gattLink{dev: dev, char: *write})
	if err := notify.EnableNotifications(c.onNotify); err != nil {
		return nil, fmt.Errorf("subscribe to ff01: %w", err)
	}
	return c, nil
}

// gattLink is the link of a real connection.
type gattLink struct {
	dev  bluetooth.Device
	char bluetooth.DeviceCharacteristic
}

func (l gattLink) write(p []byte) error {
	_, err := l.char.WriteWithoutResponse(p)
	return err
}

func (l gattLink) close() error { return l.dev.Disconnect() }
