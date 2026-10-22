# cloud-client Specification

## Purpose

Lets Go programs list a user's BLUETTI power stations, read their state, send commands, and bind
devices through the BLUETTI cloud API, keeping the account's OAuth2 token valid along the way.

## Requirements

### Requirement: Gateway address
The client SHALL send API requests to the token's `host` when it is non-empty, and to
`https://gw.bluettipower.com` otherwise. A caller-set gateway URL SHALL replace that default.

#### Scenario: Token without host
- **WHEN** the token has an empty `host` and no gateway URL is set
- **THEN** the device list request goes to `https://gw.bluettipower.com/api/bluiotdata/ha/v2/devices`

#### Scenario: Token with host
- **WHEN** the token's `host` is `https://eu.example`
- **THEN** requests go to `https://eu.example/api/bluiotdata/...`

### Requirement: Request headers
Every API request SHALL carry `Authorization` set to the raw access token (no `Bearer` prefix),
`x-os: open`, `x-app-key` with upstream's application key, and `x-app-ver: 1.0.5`. Requests with
a body SHALL send it as JSON with `Content-Type: application/json`.

#### Scenario: Headers on a device list request
- **WHEN** the client lists devices with access token `acc`
- **THEN** the request has `Authorization: acc`, `x-os: open`, `x-app-key: 19974C15BCCBC91B21A8BA71BF5`, and `x-app-ver: 1.0.5`

### Requirement: Response envelope
The client SHALL decode the `{msgId, msgCode, data}` envelope. `msgCode` 0 means success. Any
other `msgCode` SHALL be returned as an error that exposes the `msgCode`, `msgId`, and the
gateway's `message`. A non-2xx
HTTP status SHALL be returned as an error that exposes the status, without the response body.
Response bodies SHALL be read with a size limit.

#### Scenario: Business error
- **WHEN** the gateway answers HTTP 200 with `{"msgId":"m1","msgCode":1002}`
- **THEN** the call returns an error exposing msgCode 1002 and msgId `m1`

#### Scenario: HTTP error
- **WHEN** the gateway answers HTTP 502
- **THEN** the call returns an error exposing status 502

### Requirement: Token change notification
Whenever the client replaces its token, it SHALL call the caller's token callback with the new
token before the call that triggered the replacement returns. The client SHALL expose its current
token.

#### Scenario: Token renewed during a call
- **WHEN** a call triggers a refresh and SSO returns a new token
- **THEN** the callback receives the new token, and the client's current token equals it

### Requirement: Proactive token refresh
Before an API call, when the token's expiry is known and fewer than 7 days away, the client SHALL
refresh the token, at most once per hour. A failed proactive refresh SHALL NOT fail the call while
the current token has not expired.

#### Scenario: Token close to expiry
- **WHEN** the token expires in 3 days and no refresh was attempted in the last hour
- **THEN** the client refreshes before the call and uses the new token

#### Scenario: Refresh attempted recently
- **WHEN** the token expires in 3 days and a refresh was attempted 10 minutes ago
- **THEN** the client does not refresh again and makes the call with the current token

#### Scenario: Proactive refresh fails
- **WHEN** a proactive refresh fails and the current token expires in 3 days
- **THEN** the call proceeds with the current token

### Requirement: Expired token recovery
When the gateway answers with `msgCode` 805, the client SHALL refresh the token and retry the
call once. When the refresh is impossible or rejected, or the retry again returns 805, the call
SHALL return an error matching `ErrLoginRequired`.

#### Scenario: Expired token renewed
- **WHEN** a call gets msgCode 805 and the refresh succeeds
- **THEN** the call is retried once with the new token and returns its result

#### Scenario: Refresh token rejected
- **WHEN** a call gets msgCode 805 and SSO rejects the refresh with `invalid_grant`
- **THEN** the call returns an error matching `ErrLoginRequired`, and the OAuth error is reachable through the error chain

### Requirement: Concurrent use
A client SHALL be safe for concurrent use. Concurrent calls that need a refresh at the same time
SHALL cause a single refresh request.

#### Scenario: Two calls hit 805 together
- **WHEN** two concurrent calls both receive msgCode 805
- **THEN** SSO receives one refresh request, and both calls are retried with the new token

### Requirement: Device list
The client SHALL list the devices of the account from `GET /api/bluiotdata/ha/v2/devices`. Each
device SHALL carry its serial number, name, model, online flag, whether it is bound to the
current user, whether it supports network control, and its states.

#### Scenario: Two devices
- **WHEN** the gateway returns two devices with `online` `"1"` and `"0"`
- **THEN** the result has two devices, the first online and the second offline, each with its states

### Requirement: Device state model
Each state SHALL expose its function code, name, value as the string the cloud sent, type
(`SENSOR`, `SWITCH`, or `SELECT`), the allowed options (code and name) for `SELECT`, and the
sensor type and unit. When the cloud sends no unit, the unit SHALL default by sensor type as
upstream does: `%` for battery, `W` for power, `min` for duration. A device SHALL allow looking up a state by
function code. The library SHALL provide named constants for the function codes upstream uses.

#### Scenario: Look up a state
- **WHEN** a device has a state `SOC` with value `"87"`
- **THEN** looking up `SOC` returns that state with value `"87"`, and looking up an absent code reports that it is not found

#### Scenario: Missing unit
- **WHEN** the cloud sends `PVAllTotalPower` with `sensorInfo` `{"sensorType":"SensorDeviceClass.POWER","unit":null}`
- **THEN** the state's unit is `W`

#### Scenario: Select options
- **WHEN** the cloud sends `SetCtrlWorkMode` with `supportModeValues` `[{"code":"1","name":"Backup"}]`
- **THEN** the state has type `SELECT` and one option with code `1` and name `Backup`

### Requirement: Read one device
The client SHALL read one device's current state from the device list
(`GET /api/bluiotdata/ha/v2/devices`) by serial number, not from `/ha/v1/deviceStates`. If the
account has no device with that serial number, it SHALL return an error matching
`ErrDeviceNotFound`.

#### Scenario: Device found
- **WHEN** the client reads device `SN1` and the device list contains it with `SOC` `"90"`
- **THEN** the result is device `SN1` with `SOC` `"90"`, with full state metadata

#### Scenario: Device missing
- **WHEN** the client reads device `SN9` and the device list does not contain it
- **THEN** the call returns an error matching `ErrDeviceNotFound`

### Requirement: Send a command
The client SHALL send a command with `POST /api/bluiotdata/ha/v1/fulfillment` and the JSON body
`{"sn": <sn>, "fnCode": <code>, "fnValue": <value>}`. It succeeds only when the gateway answers
`msgCode` 0. The client SHALL NOT validate the value itself, and SHALL NOT retry a command except
the single retry after 805.

#### Scenario: Turn AC output off
- **WHEN** the caller sends `SetCtrlAc` = `"0"` to `SN1`
- **THEN** the gateway receives `{"sn":"SN1","fnCode":"SetCtrlAc","fnValue":"0"}` exactly once

### Requirement: Bind devices
Commands to a device that is not bound for this client fail with `msgCode` 403 ("device does not
belong to the current user"), even when the device list shows it as bound to the account. The
client SHALL bind devices with `POST /api/bluiotdata/ha/v1/bindDevices` and the JSON body
`{"bindSnList": [<sn>, ...]}`.

#### Scenario: Bind one device
- **WHEN** the caller binds `SN1`
- **THEN** the gateway receives `{"bindSnList":["SN1"]}`

### Requirement: No token leakage
The client SHALL NOT log tokens, and SHALL NOT include tokens or response bodies in error
messages.

#### Scenario: Error after 805
- **WHEN** a call fails with `ErrLoginRequired`
- **THEN** the error message contains neither the access token nor the refresh token
