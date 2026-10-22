# Spec Delta

## Purpose

Lets Go programs learn within seconds that a BLUETTI device changed, through the cloud
gateway's websocket, instead of polling the device state on a timer.

## ADDED Requirements

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

### Requirement: Heartbeat
`Watch` SHALL send STOMP heartbeats (a newline). It sends them at the negotiated interval, or
every 60 seconds when the server requests none, as upstream does; the BLUETTI gateway announces
`heart-beat:0,0`. It SHALL treat the connection as lost when nothing, heartbeats included,
arrives within twice the server's announced send interval, or within 10 minutes if the server
announces none.

#### Scenario: Silent server
- **WHEN** the server announced heartbeats and then sends nothing for longer than twice that interval
- **THEN** `Watch` drops the connection and reconnects

#### Scenario: Gateway without heartbeats
- **WHEN** the server answers `CONNECTED` with `heart-beat:0,0`
- **THEN** `Watch` sends a newline every 60 seconds and waits up to 10 minutes for traffic before reconnecting

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
