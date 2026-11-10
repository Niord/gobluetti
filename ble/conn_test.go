package ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeStation answers Modbus frames from a register map, as an AC200M does,
// and delivers each answer as 20-byte notifications.
type fakeStation struct {
	t    *testing.T
	conn *Conn

	mu         sync.Mutex
	regs       map[uint16]uint16
	frames     [][]byte // every frame written, in order
	written    []uint16 // registers written by function 0x06
	closed     bool
	pending    bool // an answer is still being delivered
	switchPack bool // writing 3006 updates register 96, as the station does
	silent     bool // never answer
	async      bool // answer from another goroutine
	answer     func(frame []byte) []byte
}

func newFakeStation(t *testing.T, regs map[uint16]uint16) *fakeStation {
	t.Helper()
	s := &fakeStation{t: t, regs: regs, switchPack: true}
	if s.regs == nil {
		s.regs = map[uint16]uint16{}
	}
	s.conn = newConn(s)
	return s
}

func (s *fakeStation) write(p []byte) error {
	s.mu.Lock()
	if s.pending {
		s.t.Errorf("frame % x written while the previous answer was still arriving", p)
	}
	s.frames = append(s.frames, bytes.Clone(p))
	var resp []byte
	switch {
	case s.silent:
	case s.answer != nil:
		resp = s.answer(p)
	default:
		resp = s.modbus(p)
	}
	s.pending = len(resp) > 0
	s.mu.Unlock()

	deliver := func() {
		chunks := slices.Collect(slices.Chunk(resp, 20))
		for i, c := range chunks {
			if i == len(chunks)-1 {
				s.mu.Lock()
				s.pending = false
				s.mu.Unlock()
			}
			s.conn.onNotify(c)
		}
	}
	if s.async {
		go deliver()
	} else {
		deliver()
	}
	return nil
}

func (s *fakeStation) modbus(p []byte) []byte {
	addr := binary.BigEndian.Uint16(p[2:4])
	val := binary.BigEndian.Uint16(p[4:6])
	switch p[1] {
	case 0x03:
		resp := []byte{0x01, 0x03, byte(2 * val)} //nolint:gosec // G115: the package reads at most 40 registers
		for r := addr; r < addr+val; r++ {
			resp = binary.BigEndian.AppendUint16(resp, s.regs[r])
		}
		return withCRC(resp)
	case 0x06:
		s.regs[addr] = val
		s.written = append(s.written, addr)
		if addr == 3006 && s.switchPack {
			s.regs[96] = val
		}
		return bytes.Clone(p)
	}
	s.t.Errorf("unexpected frame % x", p)
	return nil
}

func (s *fakeStation) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeStation) frameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func withCRC(b []byte) []byte {
	return binary.LittleEndian.AppendUint16(b, crc16(b))
}

func TestCRC16(t *testing.T) {
	if got := crc16([]byte("123456789")); got != 0x4B37 {
		t.Errorf(`crc16("123456789") = %#04x, want 0x4b37`, got)
	}
}

func TestStateDecoding(t *testing.T) {
	regs := map[uint16]uint16{
		// "AC200M" in registers 10-12, the rest NUL.
		10: 0x4143, 11: 0x3230, 12: 0x304D,
		// Serial 2235000000001 = 0x0000_0208_6061_8E01, least significant word first.
		17: 0x8E01, 18: 0x6061, 19: 0x0208, 20: 0x0000,
		23: 100, 24: 0, 25: 50, 26: 1,
		36: 120, 37: 0x01F4, 38: 300, 39: 45, 41: 100,
		43: 0x0055, 48: 1, 49: 0,
		70: 1, 71: 230, 72: 20, 73: 300, 74: 500,
		86: 48, 87: 1200, 88: 250,
		91: 2, 92: 5324,
		3061: 5,
	}
	s := newFakeStation(t, regs)

	got, err := s.conn.State(t.Context())
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	want := State{
		DeviceType: "AC200M", Serial: 2235000000001,
		DCInputW: 120, ACInputW: 500, ACOutputW: 300, DCOutputW: 45,
		PowerGenerationKWh: 10,
		BatteryPercent:     85, BatteryVoltage: 53.24, PackSlots: 2,
		ARMVersion: "1.00", DSPVersion: "655.86",
		ACMode: ACInverter, ACVoltageV: 230, ACCurrentA: 2, ACInverterW: 300, ACFrequencyHz: 50,
		DCInputVoltageV: 48, DCInputMeasuredW: 120, DCInputCurrentA: 2.5,
		AutoSleep:  SleepNever,
		ACOutputOn: true, DCOutputOn: false,
	}
	if got != want {
		t.Errorf("State() = %+v, want %+v", got, want)
	}
	wantFrames := [][]byte{
		{0x01, 0x03, 0x00, 0x0A, 0x00, 0x28, 0x65, 0xD6},    // 10-49, from an independent CRC
		withCRC([]byte{0x01, 0x03, 0x00, 0x46, 0x00, 0x15}), // 70-90
		withCRC([]byte{0x01, 0x03, 0x00, 0x5B, 0x00, 0x02}), // 91-92
		withCRC([]byte{0x01, 0x03, 0x0B, 0xF5, 0x00, 0x01}), // 3061
	}
	if !reflect.DeepEqual(s.frames, wantFrames) {
		t.Errorf("State() frames = % x, want % x", s.frames, wantFrames)
	}
}

func TestPack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		regs := map[uint16]uint16{96: 1, 98: 5312, 99: 76}
		for i := range uint16(16) {
			regs[105+i] = 330 + i
		}
		s := newFakeStation(t, regs)

		start := time.Now()
		got, err := s.conn.Pack(t.Context(), 2)
		if err != nil {
			t.Fatalf("Pack(2) error = %v", err)
		}
		if elapsed := time.Since(start); elapsed < 10*time.Second {
			t.Errorf("Pack(2) returned after %v, want at least 10s", elapsed)
		}
		cells := make([]float64, 16)
		for i := range cells {
			cells[i] = float64(330+i) / 100
		}
		want := Pack{Number: 2, Percent: 76, Voltage: 53.12, CellVoltages: cells}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Pack(2) = %+v, want %+v", got, want)
		}
		if want := []uint16{3006}; !slices.Equal(s.written, want) {
			t.Errorf("Pack(2) wrote registers %v, want %v", s.written, want)
		}
		if s.regs[3006] != 2 {
			t.Errorf("Pack(2) wrote %d to 3006, want 2", s.regs[3006])
		}
		if last := s.frames[len(s.frames)-1]; !bytes.Equal(last, withCRC([]byte{0x01, 0x03, 0x00, 0x5B, 0x00, 0x25})) {
			t.Errorf("Pack(2) last frame = % x, want a read of 91-127", last)
		}
	})
}

func TestPackStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFakeStation(t, map[uint16]uint16{96: 1})
		s.switchPack = false

		if _, err := s.conn.Pack(t.Context(), 2); err == nil {
			t.Error("Pack(2) with register 96 still 1: error = nil, want an error")
		}
	})
}

func TestPackNotConnected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFakeStation(t, map[uint16]uint16{96: 1}) // 98 (voltage) stays 0

		_, err := s.conn.Pack(t.Context(), 2)
		if !errors.Is(err, ErrNoPack) {
			t.Errorf("Pack(2) with 0 V: error = %v, want ErrNoPack", err)
		}
	})
}

func TestPackOutOfRange(t *testing.T) {
	for _, n := range []int{0, 4, -1} {
		s := newFakeStation(t, nil)
		if _, err := s.conn.Pack(t.Context(), n); err == nil {
			t.Errorf("Pack(%d) error = nil, want an error", n)
		}
		if got := s.frameCount(); got != 0 {
			t.Errorf("Pack(%d) wrote %d frames, want 0", n, got)
		}
	}
}

func TestPackCancelledDuringWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFakeStation(t, nil)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(3*time.Second, cancel)

		_, err := s.conn.Pack(ctx, 2)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Pack(2) cancelled during the wait: error = %v, want context.Canceled", err)
		}
		if got := s.frameCount(); got != 1 {
			t.Errorf("Pack(2) cancelled during the wait wrote %d frames, want 1 (the selector)", got)
		}
	})
}

func TestOutputs(t *testing.T) {
	tests := []struct {
		name      string
		set       func(c *Conn, ctx context.Context) error
		wantFrame []byte
		wantReg   uint16
		wantValue uint16
	}{
		{
			name:      "ac off",
			set:       func(c *Conn, ctx context.Context) error { return c.SetACOutput(ctx, false) },
			wantFrame: []byte{0x01, 0x06, 0x0B, 0xBF, 0x00, 0x00, 0xBA, 0x0A},
			wantReg:   3007, wantValue: 0,
		},
		{
			name:      "dc on",
			set:       func(c *Conn, ctx context.Context) error { return c.SetDCOutput(ctx, true) },
			wantFrame: []byte{0x01, 0x06, 0x0B, 0xC0, 0x00, 0x01, 0x4A, 0x12},
			wantReg:   3008, wantValue: 1,
		},
		{
			name:      "power off",
			set:       func(c *Conn, ctx context.Context) error { return c.PowerOff(ctx) },
			wantFrame: []byte{0x01, 0x06, 0x0B, 0xF4, 0x00, 0x01, 0x0B, 0xDC},
			wantReg:   3060, wantValue: 1,
		},
		{
			name:      "sleep 30s",
			set:       func(c *Conn, ctx context.Context) error { return c.SetAutoSleep(ctx, Sleep30s) },
			wantFrame: []byte{0x01, 0x06, 0x0B, 0xF5, 0x00, 0x02, 0x1A, 0x1D},
			wantReg:   3061, wantValue: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeStation(t, map[uint16]uint16{3007: 1, 3008: 0})
			if err := tt.set(s.conn, t.Context()); err != nil {
				t.Fatalf("set %s: error = %v", tt.name, err)
			}
			if want := [][]byte{tt.wantFrame}; !reflect.DeepEqual(s.frames, want) {
				t.Errorf("set %s: frames = % x, want % x", tt.name, s.frames, want)
			}
			if s.regs[tt.wantReg] != tt.wantValue {
				t.Errorf("set %s: register %d = %d, want %d", tt.name, tt.wantReg, s.regs[tt.wantReg], tt.wantValue)
			}
		})
	}
}

func TestAutoSleepRejected(t *testing.T) {
	s := newFakeStation(t, nil)
	if err := s.conn.SetAutoSleep(t.Context(), 0); err == nil {
		t.Error("SetAutoSleep(0) error = nil, want an error")
	}
	if got := s.frameCount(); got != 0 {
		t.Errorf("SetAutoSleep(0) wrote %d frames, want 0", got)
	}
}

func TestOutputEchoMismatch(t *testing.T) {
	s := newFakeStation(t, nil)
	s.answer = func(p []byte) []byte {
		echo := bytes.Clone(p[:6])
		echo[5] = 0 // the station reports value 0
		return withCRC(echo)
	}
	if err := s.conn.SetDCOutput(t.Context(), true); err == nil {
		t.Error("SetDCOutput(true) answered with value 0: error = nil, want an error")
	}
}

func TestModbusException(t *testing.T) {
	s := newFakeStation(t, nil)
	s.answer = func([]byte) []byte { return []byte{0x01, 0x83, 0x02, 0xC0, 0xF1} }

	_, err := s.conn.State(t.Context())
	merr, ok := errors.AsType[*ModbusError](err)
	if !ok {
		t.Fatalf("State() error = %v, want a *ModbusError", err)
	}
	if merr.Code != 2 || merr.Function != 0x03 {
		t.Errorf("State() ModbusError = %+v, want function 0x03, code 2", merr)
	}
}

func TestCorruptedResponse(t *testing.T) {
	s := newFakeStation(t, map[uint16]uint16{43: 85})
	s.answer = func(p []byte) []byte {
		resp := s.modbus(p)
		resp[len(resp)-1] ^= 0xFF
		return resp
	}
	if _, err := s.conn.State(t.Context()); err == nil {
		t.Error("State() with a bad CRC: error = nil, want an error")
	}
	if got := s.frameCount(); got != 1 {
		t.Errorf("State() with a bad CRC wrote %d frames, want 1 (no retry)", got)
	}
}

func TestNoResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFakeStation(t, nil)
		s.silent = true

		start := time.Now()
		if _, err := s.conn.State(t.Context()); err == nil {
			t.Error("State() with no answer: error = nil, want a timeout error")
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Errorf("State() with no answer returned after %v, want 5s", elapsed)
		}
		if got := s.frameCount(); got != 1 {
			t.Errorf("State() with no answer wrote %d frames, want 1 (no retry)", got)
		}
	})
}

func TestClosed(t *testing.T) {
	s := newFakeStation(t, nil)
	if err := s.conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !s.closed {
		t.Error("Close() did not close the link")
	}
	if _, err := s.conn.State(t.Context()); err == nil {
		t.Error("State() after Close: error = nil, want an error")
	}
	if err := s.conn.SetACOutput(t.Context(), true); err == nil {
		t.Error("SetACOutput() after Close: error = nil, want an error")
	}
	if got := s.frameCount(); got != 0 {
		t.Errorf("calls after Close wrote %d frames, want 0", got)
	}
}

func TestConcurrentRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFakeStation(t, map[uint16]uint16{43: 85})
		s.async = true

		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if _, err := s.conn.State(t.Context()); err != nil {
					t.Errorf("concurrent State() error = %v", err)
				}
			})
		}
		wg.Wait()
		if got := s.frameCount(); got != 16 {
			t.Errorf("4 concurrent State() calls wrote %d frames, want 16", got)
		}
	})
}

func TestParseName(t *testing.T) {
	tests := []struct {
		name   string
		want   Station
		wantOK bool
	}{
		{"AC200M2235000000001", Station{Name: "AC200M2235000000001", Model: "AC200M", Serial: "2235000000001"}, true},
		{"AP3002235000000001", Station{}, false},
		{"AC200M", Station{}, false},
		{"AC200Mx1", Station{}, false},
		{"", Station{}, false},
	}
	for _, tt := range tests {
		got, ok := parseName(tt.name)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("parseName(%q) = %+v, %t; want %+v, %t", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}
