# Tasks

## 1. Setup

- [x] 1.1 Add `golang.org/x/net` (`go get golang.org/x/net@latest`) and verify that `go.mod` keeps `go 1.26.0`, that the added `x/net` version requires go ≤ 1.26.0, and that `go mod tidy -diff` is clean once the package is imported

## 2. Client core and token upkeep

- [x] 2.1 Implement `NewClient`, the `Client` fields, `Token()`, `ErrLoginRequired`, and `*APIError`; implement the request path (gateway choice, headers, JSON body, 1 MiB limit, envelope decode); verify with fake-gateway tests:
  - gateway selection (default, `Token.Host`, `GatewayURL`);
  - all four headers, with no `Bearer` prefix;
  - msgCode error exposing msgCode and msgId;
  - HTTP 502 exposing the status;
  - no body text in errors
- [x] 2.2 Implement proactive refresh (expiry under 7 days, at most hourly, failure tolerated while the token is unexpired) and `OnToken`; verify with tests:
  - expiry in 3 days refreshes once;
  - a second call within the hour does not refresh;
  - a failed refresh still makes the call;
  - `OnToken` gets the new token and `Token()` equals it
- [x] 2.3 Implement 805 handling (refresh, one retry, `ErrLoginRequired` wrapping the cause, a second 805 also `ErrLoginRequired`) and single-refresh under concurrency; verify with tests:
  - 805 then success;
  - `invalid_grant` gives `ErrLoginRequired`, with `*OAuthError` reachable through `errors.AsType`;
  - two concurrent 805 calls cause one SSO refresh (`-race`);
  - error text has no token values

## 3. Devices and commands

- [x] 3.1 Implement `Device`, `State`, `Option`, `StateType`, `flexString`, `(*Device).State`, and the `Fn*` constants; verify with a decode test from an upstream-shaped `stateList`:
  - `fnValue` as string and as number;
  - `supportModeValues` and `sensorInfo` mapped;
  - `online`, `isBindByCurUser`, and `supportNetwork` mapped;
  - lookup of a present code and of an absent one
- [x] 3.2 Implement `Devices` and `Device` (`ErrDeviceNotFound` on an empty list or a different SN); verify with fake-gateway tests for path, query `sns`, two devices, and not-found
- [x] 3.3 Implement `Set` and `Bind`; verify with tests that the exact JSON bodies are sent once, that a non-zero msgCode fails `Set`, and that `Set` is not retried except after 805

## 4. Watch

- [x] 4.1 Implement STOMP frame encode and decode (header unescape, several frames per message, bare-newline heartbeats); verify with table tests, including the upstream `ERROR` `message` header with `\c`
- [x] 4.2 Implement `Watch`: dial, `CONNECT`, `CONNECTED`/`user-name`, `SUBSCRIBE`, the `MESSAGE` callback, heartbeat sender, read deadline, and context cancel; verify against an `x/net/websocket` fake:
  - `CONNECT` headers and the subscribe destination;
  - a callback with `SN1`;
  - a malformed body is ignored;
  - a missing `user-name` causes a reconnect;
  - cancel returns `context.Canceled`, and no goroutine outlives `Watch` (`-race`)
- [x] 4.3 Implement reconnect with backoff and auth `ERROR` handling; verify with in-package tests on a millisecond backoff:
  - two failed dials wait about 1×, then 2× the base;
  - the backoff resets after `CONNECTED`;
  - `ERROR` 805 refreshes and reconnects with the new token;
  - a rejected refresh returns `ErrLoginRequired`;
  - a silent server triggers a reconnect

## 5. Token file and CLI

- [x] 5.1 Move token-file code to `internal/tokenfile` (`Load`, `Save`, `DefaultPath`) and switch `cmd/gobluetti-login` to it; verify that the existing `gobluetti-login` tests pass unchanged, plus `tokenfile` tests for `0600` and atomic replace
- [x] 5.2 Implement `cmd/gobluetti` (`-token`; `devices`, `device`, `set`, `watch`; `OnToken` saves the file; `ErrLoginRequired` message); verify with `run` tests against fake SSO and gateway:
  - the `devices` lines;
  - `device` with units and `code=name` options;
  - `set` accepted and rejected;
  - a missing token file suggests `gobluetti-login`;
  - a refreshed token is written with `0600`;
  - `watch` prints a line for a notification and stops on cancel
- [x] 5.3 Update `README.md` and `README_uk.md`:
  - status line;
  - a "Using the client" section (`NewClient`, `OnToken`, `Devices`, `Set`, `Watch`, safety note);
  - `gobluetti` CLI usage;
  - dependency note.

  Add `Example` tests for the snippets. Verify that both READMEs have the same sections and that the examples compile.

## 6. Real-account check (run by the user)

- [x] 6.1 The user runs `go run ./cmd/gobluetti devices`. Record the models, online flags, and `Bound` here, without serial numbers or names.
  - 2026-10-23: run by the agent with the user's permission, read-only.
  - Two devices: `AP300-AP300` online and bound; `AC300-AC300` offline (switched off) and bound.
  - The model string has the form `<model>-<model>`.
- [x] 6.2 The user runs `go run ./cmd/gobluetti device <sn>` for each station. Record which function codes appear and whether types, units, and options are present (Open Question 1).
  - AP300, 13 states:
    - `SELECT`: `SetCtrlWorkMode` with `workmode_0..3`;
    - `SWITCH`: `SetCtrlAc`, `SetCtrlDc`, `SetACECO`, `SetDCECO`, `SetSleepMode`;
    - `SENSOR`: `SOC` (%), `DsgFullTime`, `ChgFullTime` (min), and four power readings (W).
  - AC300: 8 states, without the ECO and sleep switches.
  - Types and options are present in `/ha/v2/devices`. `deviceStates` is unreliable (see
    design), so `Device` was switched to the device list.
  - Units arrive as null for power and duration; they now default by sensor type.
- [x] 6.3 The user runs `go run ./cmd/gobluetti watch` and changes a setting in the BLUETTI app. Record whether a notification arrived and the heartbeat the gateway announced (Open Questions 2–3).
  - `gobluetti watch` for 90 s received one `AP300… changed` notification. Ctrl-C ended it with
    exit 0.
  - The gateway's `CONNECTED` frame announces `version:1.1` and `heart-beat:0,0`, so a 60 s
    keep-alive and a 10 min silent timeout were added.
- [x] 6.4 The user picks one harmless command (for example `SetACECO`), runs `gobluetti set` themselves, checks the effect in the app, and sets it back. Record the codes and results here.
  - 2026-10-23: run by the agent; the user approved `SetDCECO` on the AP300.
  - First attempt: `msgCode 403`, message "设备不属于当前用户" ("device does not belong to the
    current user"). The device list showed the device as bound, but `deviceStates` showed
    `isBindByCurUser:"0"`.
  - With the user's approval, the agent ran `gobluetti bind` for both devices (msgCode 0).
    After that, `deviceStates` returned real data for the AP300.
  - `set SetDCECO 1` was accepted, and `device` showed `1` within 5 s. `set SetDCECO 0` was
    accepted and the value went back to `0`.
  - The AC output (about 690 W load) stayed on throughout.
  - Changes made:
    - `APIError.Message` carries the gateway's text;
    - the `gobluetti bind` subcommand was added;
    - the README documents binding once.
- [x] 6.5 Write the answers into `design.md` Open Questions; if `deviceStates` lacks metadata, note a follow-up change
  - Done in `design.md` Open Questions. No follow-up change is needed: `Device` already uses
    the device list.

## 7. Final checks

- [x] 7.1 Run `gofmt -l .` (empty), `go vet ./...`, `go test -race ./...`, `go build ./...`, `go mod tidy -diff`, and golangci-lint with the plugin config, and verify that all pass; paste the summary here
  - 2026-10-22, go1.27.3, module `go 1.26.0`, packages `. ./cmd/... ./internal/...`:
    - `gofmt -l`: empty;
    - `go vet`: ok;
    - `go test -race`: ok for 4 packages, and stable with `-count=5` (430 runs);
    - `go build`: ok;
    - `go mod tidy -diff`: clean, `require golang.org/x/net v0.61.0`;
    - golangci-lint (plugin config): 0 issues.
  - Rerun on 2026-10-23 after the real-account fixes, on the whole tree (`./...`; the user removed
    the stray `agent/` skills install):
    - `gofmt`, `vet`, `build`, and `tidy` clean;
    - `go test -race -count=3` ok for 4 packages, 88 top-level tests and subtests;
    - golangci-lint: 0 issues.
- [x] 7.2 Update the local `CLAUDE.md` layout block (new files, `internal/tokenfile`, `cmd/gobluetti`, the `x/net` dependency), and verify that it stays untracked
- [x] 7.3 Write the final review summary here: specs vs. implementation, deviations from upstream, and remaining risks

  **Specs vs. implementation.**
  - All `cloud-client`, `cloud-watch`, and `device-cli` requirements have tests against fakes:
    - an `httptest` gateway and SSO;
    - an `x/net/websocket` server for the STOMP session;
    - in-package tests with millisecond backoff and heartbeat.
  - The real-account check (section 6) covered:
    - device list;
    - device state;
    - live notifications;
    - binding;
    - one command and its reversal.

  **Deviations from upstream, all from real-account findings.**
  - `Device` reads `/ha/v2/devices` instead of `/ha/v1/deviceStates`. `deviceStates` lacks
    options and sensor info, and returned an unbound placeholder before `Bind`.
  - Units default by sensor type, as upstream's `sensor.py` does.
  - Heartbeats:
    - the client offers 10 s instead of 60 s;
    - when the gateway asks for none (it announces `0,0`), the client keeps a 60 s keep-alive,
      as upstream;
    - the read timeout is 10 min.
  - Auth `ERROR`s on the websocket renew the token and reconnect. Upstream only notifies the
    user.
  - `APIError` carries the gateway's `message`. `gobluetti bind` was added because commands fail
    with 403 until a device is bound.

  **Remaining risks.**
  - Refresh may not extend the ~30-day session (see the `cloud-auth` archive). Check around
    2026-11-15. Until then, users may need to run `gobluetti-login` monthly.
  - Watch session errors are not surfaced (a `ponytail:` note in `watch.go`). An error callback
    can be added if callers need it.
  - Only `SetDCECO` was exercised on real hardware. Other codes, such as the AC/DC outputs and
    work modes, use the same request shape but are untested on the devices.

## Workflow follow-up

- Commit and push after the user reviews the diff.
- Archive with `/opsx:archive` once section 6 is done.
- Around 2026-11-15: check whether a refresh inside the last 7 days extends the session (see the `cloud-auth` archive).
