# ble-legacy Specification

## Purpose
Lets Go programs find legacy BLUETTI power stations nearby (AC200M family, including the AC200MAX),
read their state and per-battery-pack data, and switch their AC and DC outputs locally, over plain
Modbus RTU on Bluetooth Low Energy, without the cloud.

## Requirements

### Requirement: Supported stations
The library SHALL treat a BLE peripheral as a supported station only when its advertised local
name matches `AC200M` followed by one or more decimal digits. The digits are the station's
serial number, and the model is `AC200M`. Other peripherals SHALL be ignored.

#### Scenario: AC200MAX advertisement
- **WHEN** a peripheral advertises the name `AC200M2235000000001`
- **THEN** it is reported as a station with model `AC200M` and serial `2235000000001`

#### Scenario: Encrypted or unknown model
- **WHEN** a peripheral advertises the name `AP3002235000000001` or has no name
- **THEN** it is not reported

### Requirement: Discovery
Scanning SHALL report each supported station once, with its advertised name, model, and serial,
and SHALL stop when the caller's context is done. A scan that runs until the context is done and
finds no station SHALL return an empty result, not an error.

#### Scenario: Two advertisements from one station
- **WHEN** the same station advertises twice during a scan
- **THEN** the scan result lists it once

### Requirement: Connection
Connecting by advertised name SHALL scan until a peripheral with exactly that name is seen, then
connect, find GATT characteristic `0000ff02-0000-1000-8000-00805f9b34fb` for writes and
`0000ff01-0000-1000-8000-00805f9b34fb` for notifications, and subscribe to notifications. When
the context ends before the station is found, or either characteristic is missing, connecting
SHALL fail with an error and leave no open connection. No encryption handshake SHALL be performed.

#### Scenario: Station not in range
- **WHEN** no peripheral with the requested name advertises before the context deadline
- **THEN** connecting returns an error

#### Scenario: Characteristic missing
- **WHEN** the connected peripheral has no `ff01` characteristic
- **THEN** connecting returns an error and the BLE connection is closed

### Requirement: Request framing
Each request SHALL be one Modbus RTU frame written to `ff02`: slave address `0x01`, the function
code, big-endian fields, and the CRC-16/MODBUS of the preceding bytes appended low byte first.
Reads SHALL use function `0x03` (read holding registers). Writes SHALL use function `0x06` (write
single register).

#### Scenario: Read frame
- **WHEN** the library reads 40 registers starting at register 10
- **THEN** it writes the bytes `01 03 00 0A 00 28` followed by their CRC-16/MODBUS, low byte first

### Requirement: Response assembly
The library SHALL concatenate notifications from `ff01` until the expected response length
is reached (`5 + 2 × count` bytes for a read, 8 bytes for a write), and SHALL check its CRC. A
response whose function byte is the request function plus `0x80` SHALL be returned as an error
exposing the Modbus exception code. A CRC mismatch SHALL be returned as an error.

#### Scenario: Response split across notifications
- **WHEN** a 85-byte read response arrives as notifications of 20, 20, 20, 20, and 5 bytes
- **THEN** the read returns the 80 data bytes

#### Scenario: Modbus exception
- **WHEN** the station answers a read with `01 83 02` and a valid CRC
- **THEN** the read returns an error exposing exception code 2

#### Scenario: Corrupted response
- **WHEN** the response CRC does not match its bytes
- **THEN** the read returns an error

### Requirement: Request discipline
The library SHALL send one request at a time per connection, SHALL return an error when a
response does not complete within 5 seconds, and SHALL NOT retry a failed request.

#### Scenario: No retry after failure
- **WHEN** a read fails with a CRC mismatch
- **THEN** the library does not write the request again

#### Scenario: No response
- **WHEN** no complete response arrives within 5 seconds
- **THEN** the read returns a timeout error

### Requirement: Station state
Reading the state SHALL read registers 10–49 and register 91, and SHALL decode:
device type (registers 10–15, ASCII, trailing NUL bytes dropped), serial number (registers 17–20,
least significant word first), DC input power W (36), AC input power W (37), AC output power W
(38), DC output power W (39), battery percent (43), AC output on (48, value 1), DC output on (49,
value 1), and the pack slot count (91). Every register is an unsigned
16-bit big-endian value.

#### Scenario: Decoding a state
- **WHEN** register 37 is `0x01F4`, register 43 is `0x0055`, register 48 is `1`, and register 49 is `0`
- **THEN** the state has AC input 500 W, battery 85 %, AC output on, and DC output off

#### Scenario: Pack slots are not connected packs
- **WHEN** an AC200MAX with only its internal pack reports register 91 as `3`
- **THEN** the state has 3 pack slots, and packs 2 and 3 read as empty slots

### Requirement: Battery pack selection
Reading pack `n` SHALL accept `n` from 1 to 3 and reject other values without sending anything.
It SHALL write `n` to register 3006 and wait 10 seconds before reading, because the station
needs that long to switch the pack data. Context cancellation SHALL end the wait early with the
context's error.

#### Scenario: Pack out of range
- **WHEN** pack 4 is requested
- **THEN** an error is returned and no frame is written

#### Scenario: Cancelled during the wait
- **WHEN** the context is cancelled 3 seconds after the pack selector was written
- **THEN** the call returns the context's error and reads nothing

### Requirement: Battery pack data
After the wait, reading pack `n` SHALL read registers 91–127 and decode: pack number (96), pack
voltage V (98, value / 100), pack percent (99), and 16 cell voltages V (105–120, each value /
100). When register 96 does not equal `n`, the read SHALL return an error instead of another
pack's data. When register 98 is `0`, no pack is connected in that slot, and the read SHALL
return an error that wraps `ErrNoPack` instead of zero readings.

#### Scenario: Pack 2 selected
- **WHEN** pack 2 is read and register 96 is `2`, register 98 is `5312`, and register 99 is `76`
- **THEN** the result is pack 2 with voltage 53.12 V and 76 %

#### Scenario: Empty pack slot
- **WHEN** pack 2 is read, register 96 is `2`, and register 98 is `0`
- **THEN** the read returns an error that wraps `ErrNoPack`

#### Scenario: Pack switch not applied
- **WHEN** pack 2 is read and register 96 is still `1` after the wait
- **THEN** the read returns an error

### Requirement: Output switches
Switching the AC output SHALL write register 3007, and switching the DC output SHALL write
register 3008: value `1` for on and `0` for off, as exactly one write request. The write SHALL
succeed only when the response echoes the register and value. The library SHALL NOT write any
register other than 3006, 3007, and 3008.

#### Scenario: AC output off
- **WHEN** the caller switches the AC output off
- **THEN** the library writes `01 06 0B BF 00 00` followed by its CRC, once

#### Scenario: Echo mismatch
- **WHEN** the station answers a DC-on write with an echo of value `0`
- **THEN** the call returns an error

### Requirement: Closing
Closing a connection SHALL disconnect from the station. Requests on a closed connection SHALL
return an error without writing anything.

#### Scenario: Read after close
- **WHEN** the caller reads the state after closing the connection
- **THEN** an error is returned and no frame is written
