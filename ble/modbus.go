package ble

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Modbus RTU as legacy stations speak it over BLE: slave address 1,
// big-endian fields, CRC-16/MODBUS appended low byte first.
const (
	slaveAddr     = 0x01
	readHolding   = 0x03
	writeRegister = 0x06
)

// A ModbusError is an exception response from the station: it understood the
// request and refused it.
type ModbusError struct {
	Function byte // the function code of the refused request
	Code     byte // the Modbus exception code
}

func (e *ModbusError) Error() string {
	return fmt.Sprintf("ble: station refused function %#02x: modbus exception %d", e.Function, e.Code)
}

// crc16 is CRC-16/MODBUS: reflected polynomial 0xA001, initial value 0xFFFF.
func crc16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, x := range b {
		crc ^= uint16(x)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// frame builds a request: function fn with two big-endian fields (a register
// address and a count or value) and the CRC.
func frame(fn byte, addr, val uint16) []byte {
	b := []byte{slaveAddr, fn}
	b = binary.BigEndian.AppendUint16(b, addr)
	b = binary.BigEndian.AppendUint16(b, val)
	return binary.LittleEndian.AppendUint16(b, crc16(b))
}

// checkResponse validates a complete answer to req and returns its payload:
// the register bytes of a read, or the echoed value of a write.
func checkResponse(req, resp []byte) ([]byte, error) {
	fn := req[1]
	if len(resp) >= 5 && resp[1] == fn|0x80 {
		if crc16(resp[:3]) != binary.LittleEndian.Uint16(resp[3:5]) {
			return nil, errors.New("ble: exception response with a bad CRC")
		}
		return nil, &ModbusError{Function: fn, Code: resp[2]}
	}
	body, sum := resp[:len(resp)-2], resp[len(resp)-2:]
	if crc16(body) != binary.LittleEndian.Uint16(sum) {
		return nil, fmt.Errorf("ble: response % x has a bad CRC", resp)
	}
	if body[0] != slaveAddr || body[1] != fn {
		return nil, fmt.Errorf("ble: response % x does not answer function %#02x", resp, fn)
	}
	if fn == readHolding {
		count := binary.BigEndian.Uint16(req[4:6])
		if int(body[2]) != 2*int(count) {
			return nil, fmt.Errorf("ble: response carries %d bytes, want %d", body[2], 2*count)
		}
		return body[3:], nil
	}
	if !bytes.Equal(body, req[:6]) {
		return nil, fmt.Errorf("ble: write echo % x does not match request % x", body, req[:6])
	}
	return body[4:6], nil
}
