# Proposal

## Why

Some BLUETTI stations cannot be controlled through the cloud at all. The AC200MAX is one
example: it has no "Cloud Control" in the upstream support table. These older stations speak
plain Modbus RTU over Bluetooth Low Energy, with no encryption, and that protocol is documented by
an MIT-licensed community project (warhammerkid/bluetti_mqtt). Over BLE they also report
per-battery-pack data (voltage, charge, cell voltages), which the cloud API does not expose.

The project's scope rule was "cloud only, no BLE". The reasons were the closed native crypto
library and the keys extracted from the app, and neither applies to these stations. On 2026-11-09
the user narrowed the rule to allow plaintext BLE for legacy stations.

## What Changes

- New Go module `github.com/Niord/gobluetti/ble`, nested in this repository with its own `go.mod`.
  Consumers of the cloud library do not inherit its dependencies.
- Discover legacy stations by their BLE advertised name (`AC200M<serial>` covers both the AC200M
  and the AC200MAX).
- Connect to a station and read its state: input/output power, battery percent, AC/DC output
  switches, and the number of battery packs.
- Read one battery pack: voltage, percent, and 16 cell voltages.
- Switch the AC output and the DC output on or off.
- New CLI `gobluetti-ble`: `scan | state <name> | pack <name> <n> | set <name> ac|dc on|off`.
- README (both languages): replace "Why no Bluetooth" with what is supported over BLE and why
  encrypted models are not. `openspec/config.yaml` context: drop "Bluetooth is out of scope".
- `LICENSE`: add the bluetti_mqtt MIT notice (Copyright 2022 Stephen Augenstein).

Not included: encrypted BLE models (Apex, AC180, Elite, and others), the station power-off and
auto-sleep controls, models other than the AC200M family, and cloud/BLE unification.

## Capabilities

### New Capabilities

- `ble-legacy`: discovering, reading, and switching legacy BLUETTI stations over plaintext
  Modbus RTU on BLE.
- `ble-cli`: the `gobluetti-ble` command-line tool built on `ble-legacy`.

### Modified Capabilities

None. The cloud library's requirements do not change.

## Impact

- New module directory `ble/` (library, `cmd/gobluetti-ble`, tests). Its dependencies are
  `tinygo.org/x/bluetooth` and its transitive tree (BlueZ D-Bus on Linux, CoreBluetooth via cgo
  on macOS, WinRT on Windows).
- The root module `github.com/Niord/gobluetti`: no code or dependency change.
- CI: build, vet, test, and lint the `ble` module as well.
- Release: the BLE module is tagged separately (`ble/v0.x.y`).
