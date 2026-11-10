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
	regCoreFirst   = 10 // registers 10-49: device type, serial, power, switches
	regCoreCount   = 40
	regPackFirst   = 91 // registers 91-127: pack slots and the selected pack
	regPackCount   = 37
	regDetailFirst = 70 // registers 70-90: AC path and the DC input sensors
	regDetailCount = 21
	regPackSelect  = 3006
	regACOutput    = 3007
	regDCOutput    = 3008
	regPowerOff    = 3060
	regAutoSleep   = 3061
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

// An ACMode is the AC output path the station reports in register 70.
type ACMode uint16

// ACStop means the AC output is stopped. The other constants are the paths
// register 70 can report.
const (
	ACStop ACMode = iota
	ACInverter
	ACBypassC
	ACBypassD
	ACLoadMatch
)

// String returns the path name, or the raw register value when it is none of these.
func (m ACMode) String() string {
	switch m {
	case ACStop:
		return "stop"
	case ACInverter:
		return "inverter"
	case ACBypassC:
		return "bypass-c"
	case ACBypassD:
		return "bypass-d"
	case ACLoadMatch:
		return "load-match"
	default:
		return fmt.Sprintf("%d", m)
	}
}

// An AutoSleep is how long the station waits before sleeping (register 3061).
// The station accepts only these four values.
type AutoSleep uint16

// Sleep30s is a 30-second wait before sleep. Sleep1m, Sleep5m, and SleepNever
// are the other values register 3061 accepts.
const (
	Sleep30s   AutoSleep = 2
	Sleep1m    AutoSleep = 3
	Sleep5m    AutoSleep = 4
	SleepNever AutoSleep = 5
)

// String returns the wait, or the raw register value when it is not one of the four.
func (s AutoSleep) String() string {
	switch s {
	case Sleep30s:
		return "30s"
	case Sleep1m:
		return "1m"
	case Sleep5m:
		return "5m"
	case SleepNever:
		return "never"
	default:
		return fmt.Sprintf("%d", s)
	}
}

// State is a snapshot of a station's main readings.
type State struct {
	DeviceType string // model as the station reports it, such as "AC200M"
	Serial     uint64

	DCInputW  int // solar or car input, W; register 36
	ACInputW  int // grid or generator input, W
	ACOutputW int // W
	DCOutputW int // W

	PowerGenerationKWh float64 // register 41, value / 10; an AC200MAX often reports 0

	BatteryPercent int     // charge of all packs together, %
	BatteryVoltage float64 // V, all packs together; register 92, value / 100
	PackSlots      int     // battery pack slots, 3 on the AC200MAX; not how many packs are connected

	ARMVersion string // firmware, such as "4008.05"; registers 23-24
	DSPVersion string // firmware, such as "4005.08"; registers 25-26

	ACMode        ACMode  // register 70
	ACVoltageV    int     // register 71
	ACCurrentA    float64 // register 72, value / 10
	ACInverterW   int     // register 73, the inverter's own power sensor
	ACFrequencyHz float64 // register 74, value / 10

	DCInputVoltageV  int     // register 86, V
	DCInputMeasuredW float64 // register 87, value / 10; finer than DCInputW
	DCInputCurrentA  float64 // register 88, value / 100

	AutoSleep AutoSleep // register 3061

	ACOutputOn bool
	DCOutputOn bool
}

// State reads the station's main readings.
func (c *Conn) State(ctx context.Context) (State, error) {
	core, err := c.read(ctx, regCoreFirst, regCoreCount)
	if err != nil {
		return State{}, err
	}
	detail, err := c.read(ctx, regDetailFirst, regDetailCount)
	if err != nil {
		return State{}, err
	}
	packs, err := c.read(ctx, regPackFirst, 2)
	if err != nil {
		return State{}, err
	}
	sleep, err := c.read(ctx, regAutoSleep, 1)
	if err != nil {
		return State{}, err
	}
	reg := func(r uint16) int { return int(register(core, regCoreFirst, r)) }
	dreg := func(r uint16) int { return int(register(detail, regDetailFirst, r)) }
	var serial uint64
	for i := range uint16(4) {
		serial |= uint64(register(core, regCoreFirst, 17+i)) << (16 * i)
	}
	return State{
		DeviceType:         string(bytes.TrimRight(core[:12], "\x00")), // registers 10-15
		Serial:             serial,
		DCInputW:           reg(36),
		ACInputW:           reg(37),
		ACOutputW:          reg(38),
		DCOutputW:          reg(39),
		PowerGenerationKWh: float64(reg(41)) / 10,
		BatteryPercent:     reg(43),
		BatteryVoltage:     float64(register(packs, regPackFirst, 92)) / 100,
		PackSlots:          int(register(packs, regPackFirst, 91)),
		ARMVersion:         version(register(core, regCoreFirst, 23), register(core, regCoreFirst, 24)),
		DSPVersion:         version(register(core, regCoreFirst, 25), register(core, regCoreFirst, 26)),
		ACMode:             ACMode(register(detail, regDetailFirst, 70)),
		ACVoltageV:         dreg(71),
		ACCurrentA:         float64(dreg(72)) / 10,
		ACInverterW:        dreg(73),
		ACFrequencyHz:      float64(dreg(74)) / 10,
		DCInputVoltageV:    dreg(86),
		DCInputMeasuredW:   float64(dreg(87)) / 10,
		DCInputCurrentA:    float64(dreg(88)) / 100,
		AutoSleep:          AutoSleep(register(sleep, regAutoSleep, regAutoSleep)),
		ACOutputOn:         reg(48) == 1,
		DCOutputOn:         reg(49) == 1,
	}, nil
}

// version decodes a firmware field: the first register is the low 16 bits,
// the second is the high 16 bits, and the 32-bit value is hundredths.
func version(lo, hi uint16) string {
	v := uint32(lo) | uint32(hi)<<16
	return fmt.Sprintf("%d.%02d", v/100, v%100)
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

// PowerOff shuts the station down with a single command. Whatever is plugged
// in loses power, and the station stops answering until someone switches it on.
func (c *Conn) PowerOff(ctx context.Context) error {
	return c.writeRegister(ctx, regPowerOff, 1)
}

// SetAutoSleep sets how long the station waits before sleeping. mode must be
// one of Sleep30s, Sleep1m, Sleep5m, or SleepNever; anything else is rejected
// without a write.
func (c *Conn) SetAutoSleep(ctx context.Context, mode AutoSleep) error {
	switch mode {
	case Sleep30s, Sleep1m, Sleep5m, SleepNever:
	default:
		return fmt.Errorf("ble: auto sleep %d is not 30s, 1m, 5m, or never", mode)
	}
	return c.writeRegister(ctx, regAutoSleep, uint16(mode))
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
