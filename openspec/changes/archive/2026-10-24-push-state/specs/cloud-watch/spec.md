# Spec Delta

## ADDED Requirements

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

## MODIFIED Requirements

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
