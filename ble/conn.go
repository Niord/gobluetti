package ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// AC200M registers, from bluetti_mqtt core/devices/ac200m.py at ffe9b96.
const (
	regCoreFirst  = 10 // registers 10-49: device type, serial, power, switches
	regCoreCount  = 40
	regPackFirst  = 91 // registers 91-127: pack slots and the selected pack
	regPackCount  = 37
	regPackSelect = 3006
	regACOutput   = 3007
	regDCOutput   = 3008
)

const (
	requestTimeout = 5 * time.Second
	// The station needs this long after a pack switch before registers
	// 91-127 describe the new pack (bluetti_mqtt device_handler.py).
	packSwitchDelay = 10 * time.Second
)

var (
	errTimeout = errors.New("ble: no response from the station within 5s")
	errClosed  = errors.New("ble: connection closed")
)

// ErrNoPack reports that no battery pack is connected in the slot Pack read:
// the station selects the slot but reports 0 V for it.
var ErrNoPack = errors.New("no battery pack connected")

// link is the GATT connection under a Conn: write sends one frame to ff02,
// and the owner of the link feeds ff01 notifications to Conn.onNotify.
type link interface {
	write(p []byte) error
	close() error
}

// A Conn is a connection to one station, made by Connect. Its methods may be
// called concurrently; requests go to the station one at a time, and a failed
// request is never retried.
type Conn struct {
	link link

	reqMu  sync.Mutex // held for a whole request; guards closed
	closed bool

	bufMu   sync.Mutex
	buf     []byte        // notification bytes received for the current request
	arrived chan struct{} // capacity 1: bytes arrived since the last look at buf
}

func newConn(l link) *Conn {
	return &Conn{link: l, arrived: make(chan struct{}, 1)}
}

// onNotify receives one ff01 notification. It copies p, which the BLE stack
// may reuse after the call.
func (c *Conn) onNotify(p []byte) {
	c.bufMu.Lock()
	c.buf = append(c.buf, p...)
	c.bufMu.Unlock()
	select {
	case c.arrived <- struct{}{}:
	default: // a signal is already pending
	}
}

// request writes req and waits up to requestTimeout for an answer whose
// normal length is size bytes. It returns the answer's payload.
func (c *Conn) request(ctx context.Context, req []byte, size int) ([]byte, error) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	if c.closed {
		return nil, errClosed
	}

	c.bufMu.Lock()
	c.buf = nil
	c.bufMu.Unlock()
	select {
	case <-c.arrived:
	default:
	}

	ctx, cancel := context.WithTimeoutCause(ctx, requestTimeout, errTimeout)
	defer cancel()
	if err := c.link.write(req); err != nil {
		return nil, fmt.Errorf("ble: write to the station: %w", err)
	}
	for {
		c.bufMu.Lock()
		var resp []byte
		if n := answerLen(c.buf, req[1], size); n > 0 {
			resp = bytes.Clone(c.buf[:n])
		}
		c.bufMu.Unlock()
		if resp != nil {
			return checkResponse(req, resp)
		}
		select {
		case <-c.arrived:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// answerLen returns the length of the complete answer at the start of buf,
// or 0 while it is still arriving. An exception answer is 5 bytes long.
func answerLen(buf []byte, fn byte, size int) int {
	switch {
	case len(buf) >= 5 && buf[1] == fn|0x80:
		return 5
	case len(buf) >= size:
		return size
	}
	return 0
}

func (c *Conn) read(ctx context.Context, first, count uint16) ([]byte, error) {
	return c.request(ctx, frame(readHolding, first, count), 5+2*int(count))
}

func (c *Conn) writeRegister(ctx context.Context, r, val uint16) error {
	_, err := c.request(ctx, frame(writeRegister, r, val), 8)
	return err
}

// register returns register r from data, a read that started at first.
func register(data []byte, first, r uint16) uint16 {
	return binary.BigEndian.Uint16(data[2*(r-first):])
}

// State is a snapshot of a station's main readings.
type State struct {
	DeviceType string // model as the station reports it, such as "AC200M"
	Serial     uint64

	DCInputW  int // solar or car input, W
	ACInputW  int // grid or generator input, W
	ACOutputW int // W
	DCOutputW int // W

	BatteryPercent int // charge of all packs together, %
	PackSlots      int // battery pack slots, 3 on the AC200MAX; not how many packs are connected

	ACOutputOn bool
	DCOutputOn bool
}

// State reads the station's main readings.
func (c *Conn) State(ctx context.Context) (State, error) {
	core, err := c.read(ctx, regCoreFirst, regCoreCount)
	if err != nil {
		return State{}, err
	}
	packs, err := c.read(ctx, regPackFirst, 1)
	if err != nil {
		return State{}, err
	}
	reg := func(r uint16) int { return int(register(core, regCoreFirst, r)) }
	var serial uint64
	for i := range uint16(4) {
		serial |= uint64(register(core, regCoreFirst, 17+i)) << (16 * i)
	}
	return State{
		DeviceType:     string(bytes.TrimRight(core[:12], "\x00")), // registers 10-15
		Serial:         serial,
		DCInputW:       reg(36),
		ACInputW:       reg(37),
		ACOutputW:      reg(38),
		DCOutputW:      reg(39),
		BatteryPercent: reg(43),
		PackSlots:      int(register(packs, regPackFirst, 91)),
		ACOutputOn:     reg(48) == 1,
		DCOutputOn:     reg(49) == 1,
	}, nil
}

// Pack is the data of one battery pack.
type Pack struct {
	Number       int
	Percent      int       // %
	Voltage      float64   // V
	CellVoltages []float64 // V, 16 cells
}

// Pack reads battery pack n, from 1 to 3. The station shows one pack at a
// time, so Pack first selects pack n on the station (a register write that
// does not switch any power) and then waits 10 seconds for the pack data to
// follow, before reading it. Cancelling ctx ends the wait. When no pack is
// connected in slot n, Pack returns an error that wraps ErrNoPack.
func (c *Conn) Pack(ctx context.Context, n int) (Pack, error) {
	if n < 1 || n > 3 {
		return Pack{}, fmt.Errorf("ble: pack %d is out of range 1-3", n)
	}
	if err := c.writeRegister(ctx, regPackSelect, uint16(n)); err != nil {
		return Pack{}, err
	}
	wait := time.NewTimer(packSwitchDelay)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return Pack{}, ctx.Err()
	case <-wait.C:
	}

	data, err := c.read(ctx, regPackFirst, regPackCount)
	if err != nil {
		return Pack{}, err
	}
	reg := func(r uint16) int { return int(register(data, regPackFirst, r)) }
	if got := reg(96); got != n {
		return Pack{}, fmt.Errorf("ble: station still shows pack %d after selecting pack %d", got, n)
	}
	if reg(98) == 0 {
		return Pack{}, fmt.Errorf("ble: pack %d: %w", n, ErrNoPack)
	}
	cells := make([]float64, 16)
	for i := range cells {
		cells[i] = float64(reg(105+uint16(i))) / 100
	}
	return Pack{
		Number:       n,
		Percent:      reg(99),
		Voltage:      float64(reg(98)) / 100,
		CellVoltages: cells,
	}, nil
}

// SetACOutput switches the AC output on or off with a single command. It
// switches real power: whatever is plugged into the AC sockets is affected.
func (c *Conn) SetACOutput(ctx context.Context, on bool) error {
	return c.setSwitch(ctx, regACOutput, on)
}

// SetDCOutput switches the DC output on or off with a single command. It
// switches real power: whatever is plugged into the DC sockets is affected.
func (c *Conn) SetDCOutput(ctx context.Context, on bool) error {
	return c.setSwitch(ctx, regDCOutput, on)
}

func (c *Conn) setSwitch(ctx context.Context, r uint16, on bool) error {
	var val uint16
	if on {
		val = 1
	}
	return c.writeRegister(ctx, r, val)
}

// Close disconnects from the station, after a request in progress ends.
// Calls after Close return an error without sending anything.
func (c *Conn) Close() error {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.link.close()
}
