# Proposal

## Why

The BLUETTI cloud's REST API can hang for hours while its websocket keeps pushing device state.
During such outages `GET /ha/v2/devices` answers in about 0.3 seconds, in 19 or 43 seconds, or
not at all. The community Home Assistant integration reports the same outage
(bluetti-community/bluetti-home-assistant#78). Meanwhile `DEVICE_REALTIME_STATUS` pushes keep
arriving about once a minute, with SOC and power readings.

gobluetti v0.1.0 keeps only the serial number from a push. Callers must read the state over REST,
which is the part that hangs. `Watch` runs its callback on the read loop, so a callback that
waits on REST also stops push delivery.

## What Changes

- New method `Client.WatchUpdates(ctx, func(Update))`. It runs the same websocket session as
  `Watch` and passes each notification on, parsed into an `Update`:
  - the serial number, the message type, and the device timestamp;
  - values by function code (`SOC`, `ACLoadAllTotalPower`, ...) for the push fields whose meaning
    a capture confirmed;
  - every non-null payload field, by its push name.

  `Watch(ctx, func(sn string))` keeps its signature and behavior; it now calls `WatchUpdates`.
- A push never fails the session. A malformed or incomplete body yields an `Update` with what
  could be read, or no `Update` when it has no serial number.
- New method `Client.DeviceStates(ctx, sns ...string)`. It reads current values from
  `GET /api/bluiotdata/ha/v1/deviceStates?sns=a,b`. This lighter endpoint returns the same codes
  and values as the device list, but no names, models, options, or units. `Device(sn)` stays on
  the device list (see design.md).
- **Changed default:** without a caller-supplied `HTTPClient`, the request timeout drops from 30
  seconds to 15.
- **Changed timing:** `Watch` treats a session as dead after 3 minutes without traffic (was 10)
  when the server announces no heartbeats. A capture showed a connection that stayed open but
  delivered nothing for 24 minutes, while a new connection got pushes within 45 seconds.
- Recorded, scrubbed push bodies become test fixtures.
- Docs: README (both languages) and doc comments say that REST can be down for hours while pushes
  still arrive, and that a `Watch`/`WatchUpdates` callback must return quickly.
- No **BREAKING** changes: every exported identifier and signature of v0.1.0 stays.

Out of scope:
- mapping push fields whose meaning the capture could not confirm (`chgFullTime`, `dcSwitch`,
  `workMode`, ECO, sleep). They stay available through the raw fields;
- the older flat push shape `data.deviceSn`, which neither the pinned upstream nor the captured pushes use;
- `gobluetti watch` output, which stays one serial number per line;
- caching or merging state in the library. Callers merge pushes into what they keep.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `cloud-watch`: parsed state updates from push bodies (`WatchUpdates`), the push body format and
  its robustness rules, and the 3-minute silent timeout.
- `cloud-client`: reading values for one or more devices from `/ha/v1/deviceStates`, and the 15
  second default request timeout.

## Impact

- Code: `watch.go` (parsing, `WatchUpdates`, timeout), `device.go` (`DeviceStates`), `client.go`
  (default timeout), new tests and `testdata/` fixtures.
- API: additions only, namely `Update`, `Client.WatchUpdates`, `Client.DeviceStates`, and the
  `MessageRealtimeStatus` constant. v0.1.0 callers build unchanged.
- Callers that rely on the implicit 30-second timeout get 15 seconds. They can set `HTTPClient`
  to keep 30.
- No new dependencies.
- Release: tag `v0.2.0` with a changelog for consumers.
