# cloud-watch Specification

## Purpose

Lets Go programs learn within seconds that a BLUETTI device changed, through the cloud
gateway's websocket, instead of polling the device state on a timer.

## Requirements

### Requirement: Watch lifecycle
`Watch` SHALL block. For every change notification it calls the caller's function with the
device serial number. It returns only when the context is done (an error matching the context's
error) or when the token cannot be renewed (an error matching `ErrLoginRequired`). The callback
SHALL run on a single goroutine, one call at a time.

#### Scenario: Context cancelled
- **WHEN** the caller cancels the context while `Watch` is connected
- **THEN** `Watch` closes the connection and returns an error matching `context.Canceled`

### Requirement: Websocket endpoint
`Watch` SHALL connect to `wss://<gateway host>/api/edgeiotgw/ws-coordination/websocket`. The
gateway host comes from the same rule as API requests: the token's `host`, or
`gw.bluettipower.com`. An `http://` gateway URL maps to `ws://`.

#### Scenario: Default gateway
- **WHEN** the token has no `host`
- **THEN** `Watch` connects to `wss://gw.bluettipower.com/api/edgeiotgw/ws-coordination/websocket`

### Requirement: STOMP session
After the websocket opens, `Watch` SHALL send a STOMP `CONNECT` frame. The frame carries
`accept-version:1.0,1.1,2.0`, a `heart-beat` header, `host`, and the same `Authorization`,
`x-os`, `x-app-key` and `x-app-ver` values as API requests. On `CONNECTED` it SHALL subscribe to
`/ws-subscribe/user/<user-name>/notify`, where `user-name` is that frame's header. A `CONNECTED`
frame without `user-name` is a connection failure.

#### Scenario: Subscribe after connect
- **WHEN** the server answers `CONNECTED` with `user-name:u42`
- **THEN** `Watch` sends `SUBSCRIBE` with `destination:/ws-subscribe/user/u42/notify` and `ack:auto`

### Requirement: Change notifications
For each `MESSAGE` frame whose JSON body has `data.message.deviceSn`, `Watch` SHALL call the
callback with that serial number. Messages without a serial number, or with a body that is not
valid JSON, SHALL be ignored, and the session continues.

#### Scenario: Device changed
- **WHEN** a `MESSAGE` arrives with body `{"data":{"message":{"deviceSn":"SN1"}}}`
- **THEN** the callback is called once with `SN1`

#### Scenario: Malformed message
- **WHEN** a `MESSAGE` arrives with body `not json`
- **THEN** the callback is not called and `Watch` keeps running

### Requirement: State updates
`WatchUpdates` SHALL run the same session as `Watch`, with the same lifecycle, reconnect, and
authentication rules. It SHALL call its callback once for every `MESSAGE` frame that `Watch`
would report, passing an update with the serial number. It SHALL NOT make any REST request.
`Watch` SHALL keep calling its callback with the serial number only.

#### Scenario: Same notifications as Watch
- **WHEN** a `MESSAGE` arrives with body `{"data":{"message":{"deviceSn":"SN1"}}}`
- **THEN** the `WatchUpdates` callback is called once with an update for `SN1` that has no values

#### Scenario: REST never answers
- **WHEN** the gateway's REST endpoints never respond and a `DEVICE_REALTIME_STATUS` push for `SN1` arrives
- **THEN** the callback receives the push's values for `SN1`, and the gateway receives no REST request

### Requirement: Push body
A device notification body SHALL be read as
`{"data":{"message":{"deviceSn","messageType","payload":{...}}}}`. The update SHALL carry the
message type as sent. It SHALL carry every `payload` field whose value is a string, number, or
boolean, as text under its push name, except `allFieldIsNull` and `timestamp`. A `null` field
means "not in this push" and SHALL be left out.

#### Scenario: Battery pack push
- **WHEN** a push has payload `{"allFieldIsNull":false,"batteryVoltage":"520.5","batterySoc":null}`
- **THEN** the update's fields are `batteryVoltage` = `"520.5"` only

#### Scenario: Numeric field
- **WHEN** a payload field is the JSON number `62`
- **THEN** the update carries it as `"62"`

### Requirement: Values by function code
For a `DEVICE_REALTIME_STATUS` push, the update SHALL also carry values by function code, mapped
from push fields: `batterySoc`→`SOC`, `powerGridIn`→`GridAllTotalPower`,
`powerAcOut`→`ACLoadAllTotalPower`, `powerPvIn`→`PVAllTotalPower`,
`powerDcOut`→`DCLoadAllTotalPower`, `acSwitch`→`SetCtrlAc`, and `dsgEmptyTime`→`DsgFullTime`. A
code SHALL be present only when its push field is. Other message types SHALL carry no values.

#### Scenario: Realtime status
- **WHEN** a `DEVICE_REALTIME_STATUS` push has `batterySoc` `"80"`, `powerAcOut` `"480"`, and `chgFullTime` `"900"`
- **THEN** the values are `SOC` = `"80"` and `ACLoadAllTotalPower` = `"480"`, and `chgFullTime` appears only among the fields

#### Scenario: Unknown message type
- **WHEN** a push has `messageType` `"SOMETHING_ELSE"` and payload `{"batterySoc":"50"}`
- **THEN** the update has that type, field `batterySoc` = `"50"`, and no values

### Requirement: Device timestamp
The update SHALL carry the time from `payload.timestamp`. That field has the form
`2006-01-02 15:04:05` in China Standard Time (UTC+8), with no zone. When the field is absent or
does not parse, the time SHALL be zero.

#### Scenario: Timestamp in China time
- **WHEN** `payload.timestamp` is `"2026-01-15 20:00:00"`
- **THEN** the update time is `2026-01-15T12:00:00Z`

### Requirement: Malformed pushes
A push body SHALL NEVER end the session or panic. A body without a serial number SHALL produce no
update. A body with a serial number SHALL produce an update with whatever else could be read: a
missing, `null`, or non-object `payload` gives no fields or values. A field of another JSON type
SHALL be skipped.

#### Scenario: Payload is not an object
- **WHEN** a push for `SN1` has `"payload":"oops"`
- **THEN** the callback receives an update for `SN1` with no fields and no values, and the session continues

#### Scenario: Message is not an object
- **WHEN** a push body is `{"data":{"message":"a string"}}`
- **THEN** the callback is not called and the session continues

### Requirement: Heartbeat
`Watch` SHALL send STOMP heartbeats (a newline). It sends them at the negotiated interval, or
every 60 seconds when the server requests none, as upstream does; the BLUETTI gateway announces
`heart-beat:0,0`. It SHALL treat the connection as lost when nothing, heartbeats included,
arrives within twice the server's announced send interval, or within 3 minutes if the server
announces none.

#### Scenario: Silent server
- **WHEN** the server announced heartbeats and then sends nothing for longer than twice that interval
- **THEN** `Watch` drops the connection and reconnects

#### Scenario: Gateway without heartbeats
- **WHEN** the server answers `CONNECTED` with `heart-beat:0,0`
- **THEN** `Watch` sends a newline every 60 seconds and waits up to 3 minutes for traffic before reconnecting

### Requirement: Reconnect
After a lost connection, a failed dial, or a non-authentication STOMP `ERROR`, `Watch` SHALL
reconnect. Delays start at 1 second and double up to 30 seconds. The delay resets after a
successful `CONNECTED`.

#### Scenario: Gateway restarts
- **WHEN** the connection drops twice in a row before `CONNECTED`
- **THEN** `Watch` waits about 1 second, then about 2 seconds, before the next attempts

### Requirement: Authentication errors
When the server sends a STOMP `ERROR` whose `message` header holds JSON with `msgCode` 400, 403,
600, or 805, `Watch` SHALL refresh the token, notify the token callback, and reconnect. If the
refresh is impossible or rejected, `Watch` SHALL return an error matching `ErrLoginRequired`.

#### Scenario: Token expired on the websocket
- **WHEN** the server sends `ERROR` with `message:{"msgCode"\c805,"message"\c"token expired"}`
- **THEN** `Watch` refreshes the token and reconnects with the new token

#### Scenario: Refresh rejected
- **WHEN** after that error SSO rejects the refresh
- **THEN** `Watch` returns an error matching `ErrLoginRequired`
