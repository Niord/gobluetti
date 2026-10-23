# Tasks

## 1. Push fixtures

- [x] 1.1 Add `testdata/push/realtime-main.json` and `testdata/push/realtime-pack.json`, scrubbed from the captured pushes (serial `SN1`, `accountId`/`msgId` replaced, no location, Wi-Fi, ICCID, or IP values); verify with `grep -rE 'AP300[0-9]{6,}|[0-9a-f]{20,}' testdata/` returning nothing
- [x] 1.2 Add a test that loads each fixture and checks its shape (`data.message.messageType`, 79 payload keys), and verify it passes with `go test -run Fixture ./...`

## 2. Push parsing

- [x] 2.1 Add `Update`, `MessageRealtimeStatus`, and an unexported parser for push bodies (raw `message`/`payload`, `flexString` values, objects/arrays skipped, `null` left out, `allFieldIsNull`/`timestamp` excluded from `Fields`), with doc comments that name `Fields` as the cloud's vocabulary; verify table tests cover the cloud-watch "Push body" scenarios
- [x] 2.2 Map the seven confirmed fields to function codes for `DEVICE_REALTIME_STATUS` only; verify tests on both fixtures (main push: `SOC`, powers, `SetCtrlAc`, `DsgFullTime`; pack push: no values) and on an unknown type
- [x] 2.3 Parse `payload.timestamp` as UTC+8 with a fixed zone; verify the `2026-01-15 20:00:00` → `2026-01-15T12:00:00Z` test and the zero-time cases (absent, unparseable)
- [x] 2.4 Add malformed-body cases (not JSON, `data` null or a string, `message` a string, `payload` a string/array/null, numbers and booleans as values, nested objects) and a `FuzzParseUpdate` that checks it never panics and gives no update without a serial; verify `go test -run 'Update|Fuzz' ./...` and `go test -fuzz FuzzParseUpdate -fuzztime 30s`

## 3. WatchUpdates

- [x] 3.1 Move the session loop to `WatchUpdates(ctx, func(Update))` and make `Watch` call it with `u.SN`; verify every existing `watch_test.go` test passes unchanged
- [x] 3.2 Add a `WatchUpdates` test with a fake gateway whose REST handlers never answer and count requests: the callback receives the fixture's values, and the REST counter stays 0; verify with `go test -race -run WatchUpdates ./...`
- [x] 3.3 Lower the no-heartbeat silent timeout to 3 minutes and update the `Watch`/`WatchUpdates` doc comments (callback runs on the read loop, must return quickly, REST may hang while pushes arrive); verify the gateway-without-heartbeats test asserts the new value

## 4. REST reads

- [x] 4.1 Add `DeviceStates(ctx, sns ...string) ([]Device, error)` on `GET /api/bluiotdata/ha/v1/deviceStates?sns=a,b`, failing without a request when `sns` is empty, with a doc comment listing which `Device` fields it fills; verify `httptest` tests for one, two, unknown (`msgCode 1` → `*APIError`), and none
- [x] 4.2 Lower the default HTTP timeout to 15 seconds and update the `HTTPClient` field comment; verify a test that the default client's timeout is 15 seconds and that a supplied client is used as is

## 5. Docs

- [x] 5.1 Update `README.md` and `README_uk.md` in the same way: a `WatchUpdates` example that merges pushes into a cache without REST, a note that REST can be down for hours while pushes arrive, the callback rule, `DeviceStates`, and the 15-second default; verify both files have the same sections and facts
- [x] 5.2 Update `example_test.go` with a `WatchUpdates` example; verify `go test -run Example ./...` passes

## 6. Verification

- [x] 6.1 Run `gofmt -l .` (empty), `go vet ./...`, `go test -race ./...`, `go build ./...`, `golangci-lint run ./...`, and `govulncheck ./...`; record the output here
  - `gofmt -l .`: empty. `go build ./...`, `go vet ./...`: ok. `go fix -diff ./...`: empty.
  - `go test -race -count=1 ./...`: ok for all four packages. With `-v`: `TestParseUpdate`,
    `TestPushFixtures`, `FuzzParseUpdate` (seeds), `TestWatchUpdatesWithoutREST`,
    `TestWatchSilentSession`, `TestDeviceStates`, `TestDefaultAPITimeout`, and every existing
    `TestWatch*` PASS.
  - `go test -fuzz FuzzParseUpdate -fuzztime 30s`: about 6.2M executions, no failure.
  - `golangci-lint run ./...`: 0 issues. `govulncheck ./...`: no vulnerabilities found.
  - Examples have no `// Output:`, like the existing ones, so they are compiled (`go vet`) but not run.
- [x] 6.2 Run a read-only check against a real account, together with the maintainer: `WatchUpdates` for a few minutes and one `DeviceStates`; record the result here, or record that it was not done
  - Done with the maintainer, one AP300, a program built against this tree. No commands were
    sent, and the token was not refreshed: `Auth` pointed at an unreachable SSO, and `OnToken`
    was never called.
  - `DeviceStates(sn)`: answered in under a second with 13 states (code, type, value), online
    and bound.
  - `WatchUpdates` for 5 minutes: 5 updates, all `DEVICE_REALTIME_STATUS`. Three were main
    reports with 22 fields and all seven values (`SOC`, `ACLoadAllTotalPower`,
    `GridAllTotalPower`, `PVAllTotalPower`, `DCLoadAllTotalPower`, `SetCtrlAc`, `DsgFullTime`).
    Two were pack reports with 5 fields and no values.
  - `Update.Time` was 0 to 3 seconds before receipt, which confirms the UTC+8 reading.
  - Values matched `DeviceStates` taken just before: SOC equal, AC and grid power within the
    drift of a changing load.
  - Main reports came about every 2 minutes this time (about 1 minute in the earlier captures).
    The 3-minute silence timeout still sits above that.
- [x] 6.3 Write the final review summary and the consumer changelog for `v0.2.0` in this file

## Review summary

- Every requirement in the two delta specs has a test:
  - cloud-watch: `TestParseUpdate`, `FuzzParseUpdate`, `TestWatchUpdatesWithoutREST`,
    `TestWatchSilentSession`;
  - cloud-client: `TestDeviceStates`, `TestDefaultAPITimeout`.
- Existing behavior is unchanged where the specs keep it. Every v0.1.0 test passes unmodified,
  `Watch` still reports serial numbers only, and `Device(sn)` still reads the device list.
- Deviations decided during implementation and recorded in the specs:
  - only gateway API calls get the 15-second default; SSO (`Auth`) keeps 30 seconds;
  - `DeviceStates` rejects empty serial numbers and ones containing a comma without a request;
  - the silence timeout is 18 steps of the heartbeat floor (3 minutes), in the style of
    `keepAliveSteps`, so a test can check it.
- Added package-level declarations:
  - `Update`, `MessageRealtimeStatus`, `Client.WatchUpdates`, and `Client.DeviceStates` are the
    exported API;
  - `parseUpdate` is used by `watchSession` and is the parser rule;
  - `pushCodes` and `pushTimeZone` are used by `parseUpdate`;
  - `apiHTTPClient` is used by `Client.send`;
  - `silentSteps` is used by `subscribe`;
  - `changedDevice` and `silentTimeout` were removed.
- Remaining risks are in design.md: mappings checked on the AP300 only, UTC+8 checked on
  `gw.bluettipower.com` only, and the push format is undocumented.

## Changelog for v0.2.0

- **New:** `Client.WatchUpdates(ctx, func(Update))` delivers the readings carried by cloud
  pushes, with no REST call. `Update` has `SN`, `Type`, `Time`, `Values` (by function code), and
  `Fields` (every reading under the cloud's names). A push carries only some readings, so merge
  updates into your last known state.
- **New:** `Client.DeviceStates(ctx, sns...)` reads current values for one or more stations in
  one request. It has no names, options, or units.
- **New:** `MessageRealtimeStatus`.
- **Changed:** gateway API calls time out after 15 seconds by default (was 30). To keep 30, set
  `Client.HTTPClient = &http.Client{Timeout: 30 * time.Second}`.
- **Changed:** `Watch`/`WatchUpdates` reconnect after 3 minutes without traffic (was 10). The
  gateway can keep a dead session open.
- **Docs:** the cloud's REST API can be down for hours while pushes still arrive. Keep `Watch`
  and `WatchUpdates` callbacks fast and do REST calls elsewhere.
- No exported identifier was renamed or removed, so v0.1.0 code builds unchanged.

## Workflow follow-up

- Archive the change with `/opsx:archive` after review.
- Tag `v0.2.0` and push the tag only after the maintainer confirms it separately.
