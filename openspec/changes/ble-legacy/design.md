# Design

## Context

See proposal.md for motivation. The behavior is ported from
[warhammerkid/bluetti_mqtt](https://github.com/warhammerkid/bluetti_mqtt) (MIT) at commit
`ffe9b96d94672e2f1d26f569351e44d1fd98880f` (2023-05-13), which talks to these stations without
encryption:

- `bluetti_mqtt/bluetooth/client.py`: GATT UUIDs (`ff02` write, `ff01` notify), assembling
  notifications up to the expected size, CRC check, exception detection, 5 s response timeout.
- `bluetti_mqtt/core/commands.py`: Modbus RTU frames (`0x03`, `0x06`), CRC-16/MODBUS, little-endian CRC.
- `bluetti_mqtt/core/devices/ac200m.py` and `struct.py`: AC200M register map and field decoding.
- `bluetti_mqtt/device_handler.py`: pack selection via register 3006 followed by a 10 s wait.

The BLUETTI copy of the same project in `bluetti-official/bluetti-bluetooth-lib` forces the
closed encryption module. We do not use that copy.

The root module `github.com/Niord/gobluetti` has a single dependency, `golang.org/x/net`. The
cloud and BLE paths share no code: the cloud speaks JSON function codes, BLE speaks Modbus
registers.

## Goals / Non-Goals

**Goals:**
- Read state and per-pack data, and switch AC/DC outputs, on AC200M-family stations, from
  Linux, macOS, and Windows.
- Protocol logic (framing, CRC, reassembly, decoding) is fully testable without a radio.
- Cloud-only consumers see no change in their dependency graph.

**Non-Goals:**
- A common interface over cloud and BLE stations. Their data models differ, and no consumer needs it yet.
- Automatic reconnects, background polling, or watch/notify streams. The caller polls.
- Other legacy models (AC300, EP500, EB3A). The register maps exist upstream, but no one here
  can test them. They are a follow-up change.

## Decisions

### 1. Nested module `ble/` with its own `go.mod`

`tinygo.org/x/bluetooth` brings godbus, winrt-go, go-ole, cbgo (cgo on macOS), and TinyGo driver
modules. A subpackage of the root module would list those in the root `go.mod`, and every cloud
consumer would inherit them in `go.sum` and in govulncheck reports. A nested module keeps them
out. The module path is `github.com/Niord/gobluetti/ble`, tagged `ble/vX.Y.Z`, with `go 1.26.0`
like the root module.

- Alternative: a separate repository. That means separate CI, issues, and README, for a module
  this small.
- Alternative: a subpackage of the root module. Rejected because of the dependency leak above.

The BLE module does not import the root module, so no `replace` directive is needed.

### 2. BLE stack: `tinygo.org/x/bluetooth` v0.17.0

Justification for `design.md`: it is the only maintained Go BLE central library that works on
Linux (BlueZ over D-Bus, no root, no cgo), macOS (CoreBluetooth), and Windows (WinRT). The
alternatives are `go-ble/ble` (unmaintained, raw HCI needs root on Linux, macOS broken),
`paypal/gatt` (archived), and `muka/go-bluetooth` (Linux only).

macOS: building needs cgo (the default there), and the terminal running the tool needs Bluetooth
permission in System Settings → Privacy & Security → Bluetooth. CoreBluetooth hides MAC
addresses, so stations are addressed by advertised name, which works the same on every OS.

### 3. Radio behind an unexported seam

```
Conn ──► modbus (frames, CRC, reassembly, decode)   pure, table-tested
  │
  └──► link interface { write([]byte) error; notifications; close }
          ├── tinygo adapter (scan, connect, GATT)   thin, built in CI, not unit-tested
          └── fakeLink in tests                       scripted notifications
```

Every requirement except discovery and connection is tested through `fakeLink` with frames built
from the spec's byte sequences. The CRC is checked against the standard CRC-16/MODBUS test
vector (`"123456789"` → `0x4B37`) and the read-frame scenario. The `link` interface stays
unexported, so it is not part of the API contract.

### 4. Exported API (typed, not function-code strings)

```go
type Station struct{ Name, Model, Serial string }
func Scan(ctx context.Context) ([]Station, error)
func Connect(ctx context.Context, name string) (*Conn, error)

func (c *Conn) State(ctx context.Context) (State, error)
func (c *Conn) Pack(ctx context.Context, n int) (Pack, error)
func (c *Conn) SetACOutput(ctx context.Context, on bool) error
func (c *Conn) SetDCOutput(ctx context.Context, on bool) error
func (c *Conn) Close() error

type State struct {
    DeviceType                                string
    Serial                                    uint64
    DCInputW, ACInputW, ACOutputW, DCOutputW  int
    BatteryPercent, PackCount                 int
    ACOutputOn, DCOutputOn                    bool
}
type Pack struct {
    Number, Percent int
    Voltage         float64   // V
    CellVoltages    []float64 // V, 16 cells
}
type ModbusError struct{ Function, Code byte } // exception response
```

The cloud API passes strings keyed by function code because the server defines the set. Here we
decode fixed registers ourselves, so typed fields with units in their names are clearer. Writes
are two named methods instead of a generic `Set(register, value)`. That way the library cannot
write a register outside the three allowed by the spec.

### 5. No retries, one request at a time

A mutex serializes requests on a `Conn`. Failed requests are returned, not retried. Upstream
retries up to 5 times, which is fine for reads but could repeat a switch command. The caller
decides. Timeouts use 5 s per request, bounded further by the caller's context.

### 6. Pack selection: fixed 10 s wait, then verify

Upstream writes 3006 and sleeps 10 s, because the pack data lags the selection. We keep the wait
and add the register-96 check, so a slow switch returns an error instead of the previous pack's
numbers. We rejected polling 96 in a loop: 96 may update before the rest of the block does, and
upstream never relied on it.

### 7. CLI inside the BLE module

`ble/cmd/gobluetti-ble`, following the root module's `cmd/<binary>` layout. Install with
`go install github.com/Niord/gobluetti/ble/cmd/gobluetti-ble@latest`.

### 8. CI

The workflow runs the existing steps a second time with `working-directory: ble`: gofmt, `go mod
tidy` check, vet, build, `test -race`, golangci-lint, and govulncheck. On the Linux runner the
tinygo adapter compiles without cgo (BlueZ over D-Bus). Tests never touch a radio.

## Risks / Trade-offs

- [The colleague's AC200MAX firmware may already require encryption] → The first real check is a
  single read-only `state` with the colleague present. If the station does not answer plain
  frames, we stop and report back. The spec stays valid for stations that do answer.
- [The register map is community reverse-engineering, and AC200MAX may differ from AC200M]
  → Upstream's only AC200MAX-specific fix concerns the internal DC input registers (86–88),
  which we do not read. The real-device check compares our values with the station's display.
- [Register 91's meaning is uncertain (upstream names it `pack_num_max`)] → It is exposed
  as reported (`PackCount`) and not used to limit `Pack(n)`. The valid range stays 1–3.
- [Reading a pack writes register 3006] → This is a selector, not a power switch, but it is still
  a write. The spec limits writes to 3006/3007/3008, and the CLI docs say that `pack` selects
  the pack on the station.
- [tinygo bluetooth is pre-1.0 and its dependency tree changes often] → It is isolated in the
  nested module and pinned. Upgrades are behavior-preserving work, verified by CI.
- [macOS Bluetooth permission prompt] → Documented in the README.

## Migration Plan

This is a new module, so nothing migrates. Release `ble/v0.1.0` after the read-only real-device
check. Release a switch-command version only after a confirmed real `set` with the station
owner. Rollback: stop tagging, because the cloud module is untouched.

## Open Questions

- Does register 91 report the number of connected packs or a fixed maximum? We find out on the
  real device. The API exposes it either way, so neither the spec nor the tasks change.
