# Spec Delta

## ADDED Requirements

### Requirement: Read device states
The client SHALL read the current values of one or more devices from
`GET /api/bluiotdata/ha/v1/deviceStates`, with the serial numbers comma-separated in one `sns`
parameter. Each result carries the device's serial number, online, bound, and network flags, plus
states with code, name, value, and type. It has no name, model, options, sensor type, or unit,
because the endpoint sends none. With no serial numbers, or an empty one or one containing a
comma, the call SHALL fail without a request.

#### Scenario: One device
- **WHEN** the caller reads the states of `SN1` and the gateway returns `SN1` with `SOC` `"100"`
- **THEN** the request has `sns=SN1`, and the result is one device `SN1` with state `SOC` = `"100"`

#### Scenario: Two devices
- **WHEN** the caller reads the states of `SN1` and `SN2`
- **THEN** the request has `sns=SN1,SN2`, and the result has one device per returned entry, in the gateway's order

#### Scenario: Unknown serial number
- **WHEN** the gateway answers `msgCode` 1 ("device does not exist")
- **THEN** the call returns an `*APIError` with `MsgCode` 1

#### Scenario: No serial numbers
- **WHEN** the caller passes no serial numbers
- **THEN** the call returns an error and the gateway receives no request

### Requirement: Default request timeout
Without a caller-supplied HTTP client, every gateway API request SHALL time out after 15 seconds.
A caller-supplied HTTP client SHALL be used as is. SSO requests made by `Auth` keep their own
default.

#### Scenario: Gateway never answers
- **WHEN** the gateway accepts a request and never responds, and the caller supplied no HTTP client
- **THEN** the call fails after about 15 seconds
