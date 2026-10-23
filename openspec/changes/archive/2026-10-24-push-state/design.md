# Design

## Context

See proposal.md (Why). Facts this design rests on come from read-only captures of the gateway
with an AP300 (model `AP300-AP300`). Only `Devices`, `deviceStates`, and the websocket
subscription were used; no commands. The fixtures are scrubbed copies of the captured bodies.

- **Push format.** In about 45 minutes every `MESSAGE` was
  `{"code":0,"handler":"handleDeviceStatus","msgCode":0,"msgId",...,"data":{"accountId",
  "accountType":"User","protocolType":"STATUS_ADVISE","message":{"messageType":
  "DEVICE_REALTIME_STATUS","deviceSn","type":"AP300","payload":{...}}}}`.
  - The payload always had the same 79 keys. Two kinds alternated:
    - a main report with 24 non-null fields: powers, SOC, switches, time left;
    - a battery-pack report with 7 non-null fields: `batteryVoltage`, `packAverageTemp`,
      `batterySoh`, pack energy totals.
  - So `null` means "not in this push", and each push is a partial update, not a full snapshot.
  - Values are JSON strings (`"500"`, `"520.5"`). `allFieldIsNull` is a boolean.
- **Cadence.** Pushes come about once a minute, sometimes three within 8 seconds. They do not
  depend on REST calls. A first hypothesis that `deviceStates` triggers them was ruled out: pushes
  arrived before any request and kept coming after.
- **Field meaning.** A push and a `deviceStates` answer about a minute apart were compared.
  - Equal values: `batterySoc`/`SOC`, `powerGridIn`/`GridAllTotalPower`,
    `powerAcOut`/`ACLoadAllTotalPower`, `powerPvIn`/`PVAllTotalPower`,
    `powerDcOut`/`DCLoadAllTotalPower`, `acSwitch`/`SetCtrlAc`, `dsgEmptyTime`/`DsgFullTime`.
  - Mismatches:
    - `chgFullTime` was non-zero in the push while `ChgFullTime` was 0 in the state list;
    - `dcSwitch` is null while `SetCtrlDc` is "0";
    - `workMode`, ECO, and sleep were null or absent.
- **Timestamp.** Pushes arrived within seconds of their `payload.timestamp` read as UTC+8,
  so the field is UTC+8 with no zone.
- **deviceStates.** `?sns=<sn>` answered in about 0.2 seconds with the same top-level keys as
  `/devices`. `name`/`model` were null and `stateList` entries had only `fnCode`, `fnName`,
  `fnType`, `fnValue`. Values matched the device list.
  - The v0.1.0 finding of an empty, unbound placeholder came from before `Bind`. It no longer
    holds after binding.
  - `sns=a,b` and the repeated `sns=a&sns=b` both returned one entry per serial number.
  - One unknown serial number turns the whole call into `msgCode 1` "Wrong parameter: 设备不存在"
    ("device does not exist").
  - No `sns` gives `msgCode 400`.
- **Websocket sessions.** The gateway closed sessions by itself (EOF) 1 to 3 minutes after
  `CONNECTED`, and our reconnect handles that. Once, a session stayed open and delivered nothing for
  24 minutes, while a new session got pushes within 45 seconds. v0.1.0 waits up to 10 minutes
  before it gives up on a silent session.
- **REST hang.** `/devices` returned no headers within 20 seconds, three minutes after
  `deviceStates` had answered in 0.48 seconds.

Upstream (`70b3065`, `models.py:60`) reads only `data.message.deviceSn` from a push and refreshes
over `deviceStates`. The community integration (`dc5fc8e`, `models.py:71-133`) also reads only
the serial number and `messageType`/`payload.allFieldIsNull`, and still refreshes over REST. No
upstream code maps push fields. The mapping here is ours, from the capture.

## Goals / Non-Goals

**Goals:**
- A caller can follow station state from pushes alone, with no REST call.
- Every v0.1.0 call keeps compiling and behaving as before, except the two named timing defaults.
- No push body, however malformed, can crash or stop a session.

**Non-Goals:**
- State caching or merging inside the library.
- Decoupling the callback from the read loop: callers own concurrency (documented).
- Retries for REST reads.
- Function-code mapping for fields not confirmed by a capture.

## Decisions

### `WatchUpdates(ctx, func(Update))` next to `Watch`
`Watch` becomes a thin wrapper: `WatchUpdates(ctx, func(u Update) { onChange(u.SN) })`. One
session implementation serves both.

```go
type Update struct {
	SN     string
	Type   string            // messageType, for example MessageRealtimeStatus
	Time   time.Time         // payload.timestamp; zero if absent
	Values map[string]string // by function code; only codes this push carried
	Fields map[string]string // every non-null scalar payload field, by push name
}
```

- **Alternatives considered:**
  - *A field on `Client` such as `OnUpdate`.* Rejected: it is hidden state, and it runs two
    callbacks per message for no gain.
  - *Changing the `Watch` callback type.* Rejected: it breaks v0.1.0 callers.
  - *A channel-returning API.* Rejected: it makes callers handle back-pressure, and a blocked
    channel stalls the read loop exactly like a slow callback.
  - *`[]State` instead of `Values`.* Rejected: a push has no names, types, options, or units.
    Half-filled `State`s would look like full ones. A `map` is also what callers merge into their
    cache.
- **`Fields` is kept** next to `Values`. It holds 17 more readings that callers can use
  (`batteryVoltage`, `batterySoh`, `packAverageTemp`, energy totals) without the library guessing
  their meaning. Push names are the cloud's, not a contract of ours. The doc comment says so.
- **`MessageRealtimeStatus = "DEVICE_REALTIME_STATUS"`** is exported so callers can filter.

### Partial pushes stay partial
Each `Update` holds only the fields that push carried. The library does not merge, because a
merge needs per-device state that outlives sessions, and callers decide where it lives (for
example, a cache that survives restarts). "Absent" and "zero" stay distinct: a pack-only push carries no SOC, so a cache keeps
the last SOC.

### Function-code mapping limited to seven confirmed fields
These are the fields whose values matched `deviceStates`. `chgFullTime` disagreed with
`ChgFullTime`, so mapping it would put a wrong value under a known code. `dcSwitch`, `workMode`,
ECO, and sleep could not be checked, because the AP300 sent null. Leaving a code out costs a
caller one `Fields` lookup; a wrong code costs a wrong UI value. More codes can be added once a
capture from another model confirms them; adding one is a spec change.

### Timestamp as fixed UTC+8
We use `time.FixedZone("UTC+8", 8*3600)`, not `time.LoadLocation("Asia/Shanghai")`. China has no
daylight saving, and the fixed zone needs no tzdata on a minimal system image. If a data center
other than `gw.bluettipower.com` sends another zone, the time will be off by whole hours. That
is noted as an open question.

### Parsing with typed decode plus a raw payload
The body decodes into structs whose `message` and `payload` are `json.RawMessage`. Each is
decoded on its own, so a `"payload":"oops"` loses the payload but keeps the serial number. The
payload decodes into `map[string]json.RawMessage`, and each value goes through the existing
`flexString` logic. Objects and arrays are skipped. This follows the KeyError lesson of
bluetti-official/bluetti-home-assistant#171: no lookup assumes a key or a type. A fuzz test
guards it.

### `DeviceStates(ctx, sns ...string) ([]Device, error)`; `Device(sn)` unchanged
`deviceStates` exists and returns the device list's values, which confirms the task's premise.
It lacks names, models, options, and units, though. Routing `Device(sn)` through it would empty
`Name`, `Model`, `Options`, and `Unit` for v0.1.0 callers, a silent change of meaning, so
`Device(sn)` stays on the device list.

`DeviceStates` returns `[]Device` with only the fields the endpoint fills (documented), so callers
reuse `Device.State`. Serial numbers go comma-separated, since both forms worked and a comma
matches upstream's single `sns` parameter.

`msgCode 1` is returned as `*APIError`, not `ErrDeviceNotFound`. The code is a generic "wrong
parameter", and with several serial numbers we cannot tell which one is unknown.

- **Alternative considered:** merging `deviceStates` values into a cached device list. Rejected:
  it means library-side state, and the cache would go stale exactly during an outage.

### Default timeout 15 seconds
Observed answers were about 0.3 seconds when healthy, and 19 s, 43 s, or never when not.
Waiting 30 seconds rarely saves a call and holds callers twice as long. The community fix chose 10
seconds plus one retry. We pick 15 seconds and no retry: with pushes as the primary source, a
failed REST read is cheap, and a retry adds load to a struggling service. Callers can still pass
their own `HTTPClient`.

### Silent session timeout 3 minutes
A live session carried pushes about every minute, and the gateway itself closes sessions every 1
to 3 minutes. A session silent for 3 minutes is very likely dead. The cost of being wrong is one
extra reconnect, a single websocket dial, every 3 minutes for a quiet account. In v0.1.0 the
timeout went from 2 to 10 minutes to avoid churn when no pushes came. That data was taken before
binding, and it does not match today's cadence. 3 minutes leaves room above the one-minute
cadence.

### Callback contract: documented, not enforced
The callback stays on the read loop, one call at a time, in arrival order. Ordering matters when
merging partial pushes. A slow callback delays the following pushes, and if it blocks for more
than 3 minutes the session drops. Docs say: copy the update and return, and do REST work on
another goroutine. Spawning a goroutine per push in the library would lose ordering, and a
bounded queue would have to drop pushes silently.

## Risks / Trade-offs

- **[Push format is undocumented and may change]** → Parsing is tolerant: unknown types still
  produce an `Update` with `Fields`, and missing keys give empty maps. `Watch` behavior does not
  depend on the payload.
- **[Mapping verified on one model (AP300) only]** → The seven mappings use generic field names
  that cover power, SOC, and the AC switch. Other models may differ, and `Fields` stays available.
  Fixtures from other models can be added later.
- **[Fixed UTC+8 may be wrong for other data centers]** → The time is advisory. Callers can use
  receipt time instead. Recorded as an open question.
- **[15-second default cuts off slow but successful answers (19 seconds seen)]** → During
  outages those were rare. Callers who prefer waiting set `HTTPClient`. The change is named in the
  release notes.
- **[3-minute silence timeout reconnects more often for idle accounts]** → One dial per 3 minutes
  at most is far below upstream's own traffic. It is not a polling loop against REST.
- **[`Fields` names leak cloud vocabulary into callers]** → The doc comment marks them as the
  cloud's names, with no stability promise beyond what the cloud keeps.

## Migration Plan

- Release as `v0.2.0` (minor: additions plus two changed defaults).
- The changelog lists `WatchUpdates`/`Update`/`DeviceStates`, the 15-second default, and the
  3-minute silence timeout.
- Consumers on v0.1.0 upgrade with no code changes. To keep the old timeout, they set
  `HTTPClient: &http.Client{Timeout: 30 * time.Second}`.
- Rollback: pin `v0.1.0`.

## Open Questions

- Do other BLUETTI data centers also send `payload.timestamp` in UTC+8? This does not change the
  API. If needed, a later fix changes the zone.
- Which push fields carry `SetCtrlDc`, work mode, and ECO on models that report them? This would
  add mappings later and does not affect this change's shape.
