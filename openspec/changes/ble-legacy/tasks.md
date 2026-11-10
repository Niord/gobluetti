# Tasks

## 1. Module setup

- [x] 1.1 Create `ble/go.mod` (`module github.com/Niord/gobluetti/ble`, `go 1.26.0`) and a `doc.go` with the package comment; verify `cd ble && go build ./...` succeeds and the root `go.mod` is unchanged (`git diff go.mod go.sum` is empty)
- [x] 1.2 Add `tinygo.org/x/bluetooth v0.17.0` to `ble/go.mod`; verify `cd ble && go mod tidy && go build ./...` succeeds and `go list -m all` in the root module does not list it
- [x] 1.3 Add the bluetti_mqtt MIT notice (Copyright 2022 Stephen Augenstein) to `LICENSE`; verify the existing upstream BLUETTI notice is still there

## 2. Modbus framing (ble-legacy: Request framing, Response assembly)

- [x] 2.1 Implement CRC-16/MODBUS and the read (`0x03`) and write (`0x06`) frame builders; verify the tests pass for the standard vector `"123456789"` → `0x4B37`, the `01 03 00 0A 00 28` frame, and the `01 06 0B BF 00 00` frame
- [x] 2.2 Implement response reassembly, CRC check, exception detection (`ModbusError`), and write-echo check; verify the table tests pass for a split 85-byte response, the `01 83 02` exception, a bad CRC, and an echo mismatch

## 3. Connection core over a fake link (ble-legacy: Request discipline, Closing)

- [x] 3.1 Define the unexported `link` seam and `Conn` with a request mutex and a 5 s per-request timeout; verify tests with `fakeLink` cover timeout, no retry after a failure, and read-after-close (no frame written)
- [x] 3.2 Verify `go test -race ./...` in `ble/` passes with concurrent `State` calls on one `Conn`

## 4. AC200M register map (ble-legacy: Station state, Battery pack selection, Battery pack data, Output switches)

- [x] 4.1 Implement `State` (reads 10–49 and 91, decodes the fields in the spec); verify a decoding test with synthetic registers matches the spec scenario (500 W AC in, 85 %, AC on, DC off) plus device type and serial
- [x] 4.2 Implement `Pack(n)` (range 1–3, write 3006, 10 s wait, read 91–127, check register 96); verify tests using `testing/synctest` cover pack 2 decoding, the stale-pack error, out of range without a write, and cancellation during the wait
- [x] 4.3 Implement `SetACOutput` / `SetDCOutput` (registers 3007/3008, exactly one write, echo check); verify tests assert the exact frame, a single write, and no write to any other register across all `Conn` methods
- [x] 4.4 Write doc comments for every exported identifier (units, the 10 s pack wait, the fact that `Pack` writes the selector); verify `go doc -all ./ble` reads correctly and golangci-lint is clean

## 5. Radio adapter (ble-legacy: Supported stations, Discovery, Connection)

- [x] 5.1 Implement advertised-name matching (`^AC200M\d+$` → model and serial); verify a table test covers the AC200MAX name, the Apex name, and an empty name
- [x] 5.2 Implement `Scan` (deduplicated, ends on context, empty result is not an error) and `Connect` on `tinygo.org/x/bluetooth` (find `ff02`/`ff01`, subscribe, close on failure); verify `go build ./...` and `go vet ./...` pass with `GOOS=linux`, `darwin`, and `windows`

## 6. CLI `gobluetti-ble` (ble-cli)

- [x] 6.1 Implement `ble/cmd/gobluetti-ble` with `scan | state | pack | set`, `-timeout` (default 30s), Ctrl-C, and usage validation before any BLE use; verify tests of argument parsing and output formatting run against a fake station without a radio
- [x] 6.2 Add the command doc comment (usage, the fact that `set` switches real outputs, the fact that `pack` writes the selector, macOS Bluetooth permission); verify `go doc ./ble/cmd/gobluetti-ble`

## 7. Docs, config, CI

- [x] 7.1 Update `README.md` and `README_uk.md` together: replace "Why no Bluetooth" with "Bluetooth (legacy stations)" (supported models, install, why encrypted models are excluded); verify both have the same sections and facts
- [x] 7.2 Update the `openspec/config.yaml` context line about BLE scope; verify `openspec validate ble-legacy` passes
- [x] 7.3 Extend `.github/workflows/ci.yml` to run gofmt, tidy, vet, build, `test -race`, golangci-lint, and govulncheck in `ble/`; verify locally with the same commands in `ble/`, which all pass

## 8. Integration checks

- [x] 8.1 Run the full verification in both modules (`gofmt -l .`, `go vet ./...`, `go test -race ./...`, `go build ./...`, `golangci-lint run ./...`, govulncheck) and record the output here
  - 2026-11-09, go1.27.3 darwin/arm64, golangci-lint with the repository `.golangci.yml`.
  - Root module: gofmt empty, `go mod tidy -diff` clean, vet/build ok, `test -race` ok in all 4 packages, lint `0 issues`, govulncheck: no vulnerable code called (2 in required modules, not reached; they were already there).
  - `ble` module: gofmt empty, tidy clean, vet/build ok, `test -race` ok (`ble`, `ble/cmd/gobluetti-ble`), lint `0 issues`, govulncheck: no vulnerable code called (1 in a required module, not reached).
  - `GOOS=linux|darwin|windows go vet ./... && go build ./...` in `ble/`: ok on all three.
  - Root `go.mod`/`go.sum` unchanged; `go list -m all` in the root does not list `tinygo.org/x/bluetooth`.
- [x] 8.2 Check the diff and fixtures for secrets and real serial numbers (only made-up serials such as `AC200M2235000000001`); record the result here
  - grep over the new and changed files: only the made-up serial `2235000000001` (as `AC200M…`/`AP300…` names and its register words). No tokens, emails, or real serials.
- [x] 8.3 Real device, read-only, only with the station owner present and agreeing: `gobluetti-ble scan` and `state`, then `pack 1`, compared with the station display. Record the outcome (or "not done") here, with no serial numbers
  - 2026-11-10, AC200MAX, run by the owner. Firmware accepts plaintext frames. `scan` found the station as `AC200M…`, model AC200M, RSSI about −55 dBm.
  - `state`: battery 83 %, all inputs and outputs 0 W, AC and DC outputs off, register 91 = 3.
  - `pack 1`: 53.19 V, 83 %, 16 cells at 3.31 V, consistent with the 16-cell LiFePO4 pack and the battery level.
  - `pack 2` and `pack 3`: the selector switched (register 96 matched), voltage and cells read 0. No expansion battery is attached, so register 91 is the number of pack slots (upstream `pack_num_max`), not the number of connected packs.
- [ ] 8.4 Real `set`, only after the owner explicitly confirms that specific command; record the outcome or "not done"
- [x] 8.6 Follow-up to 8.3: rename `State.PackCount` to `PackSlots` (CLI: `pack slots`) and make `Pack` return `ErrNoPack` for a slot that reads 0 V; verify `TestPackNotConnected`, the updated state and format tests, and the full gate in `ble/`
- [x] 8.5 Write the final review summary here
  - Implemented: nested module `github.com/Niord/gobluetti/ble` (`doc.go`, `modbus.go`, `conn.go`, `station.go`) and `ble/cmd/gobluetti-ble`; README (en/uk), LICENSE (bluetti_mqtt notice), `openspec/config.yaml`, CI matrix over both modules.
  - Spec coverage: every ble-legacy requirement except Discovery and Connection is tested through a fake station (`conn_test.go`, timing under `testing/synctest`). Supported stations has a table test. Discovery and Connection are thin tinygo calls, compiled for three OSes but not exercised without a radio. ble-cli is covered by parse, usage-before-BLE, and format tests.
  - Deviations from upstream: no retries (upstream retries 5×), and the pack read verifies register 96. Both are recorded in design.md.
  - Implementation choice not in the design: writes use GATT write-without-response, as bleak's default in bluetti_mqtt does. If a station rejects it, switch to `Write` (one line in `gattLink.write`).
  - Answered by 8.3: the AC200MAX firmware accepts plaintext frames, and register 91 is the slot count (see 8.6).

## Workflow follow-up

- `/opsx:archive ble-legacy` after all tasks are done
- Tag `ble/v0.1.0` after 8.3 succeeds
