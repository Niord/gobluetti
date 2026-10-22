# Proposal

## Why

After `cloud-auth` the library can get a token but cannot use it. This change adds what the
library exists for: listing the user's power stations, reading their state, sending commands, and
reacting to changes in real time.

## What Changes

- A `Client` for the BLUETTI cloud gateway (`https://gw.bluettipower.com`, or the token's `host`
  when SSO returns one):
  - it sends upstream's request headers;
  - it unwraps the `{msgId, msgCode, data}` envelope;
  - it reports failures as typed errors.
- Token upkeep inside the client, following upstream:
  - refresh when fewer than 7 days are left, at most once per hour;
  - on `msgCode 805`, one refresh and one retry;
  - a `OnToken` callback on every new token, because SSO rotates refresh tokens;
  - a dedicated "log in again" error when renewal is impossible.
- Device operations on the upstream endpoints:
  - list devices (`/ha/v2/devices`);
  - read one device's current state (`/ha/v1/deviceStates`);
  - send a command (`/ha/v1/fulfillment`);
  - bind devices (`/ha/v1/bindDevices`).
- A model-independent device representation. A device is a flat list of states (code, name,
  string value, type `SENSOR`/`SWITCH`/`SELECT`, options, sensor type and unit), exactly as the
  cloud describes it, plus constants for the function codes upstream knows.
- Real-time change notifications, `Watch`: a STOMP subscription over the gateway websocket that
  reports "device SN changed". It handles heartbeats, reconnects with backoff, and renews the token
  on authentication errors.
- **New dependency:** `golang.org/x/net` (only the `websocket` package; it imports nothing beyond
  the standard library). It replaces a hand-written websocket client.
- A `gobluetti` command with the subcommands `devices`, `device <sn>`, `set <sn> <code> <value>`,
  and `watch`. It uses the token file from `gobluetti-login` and writes renewed tokens back.
- The token-file read and write moves into an internal package shared by both commands. The
  behavior of `gobluetti-login` does not change.

Out of scope:
- client-side validation of `SELECT` values, since the cloud already accepts or rejects them;
- typed accessors such as an `int` SOC;
- polling helpers. Callers combine `Watch` and `Device` themselves.

## Capabilities

### New Capabilities

- `cloud-client`: calling the BLUETTI cloud API with a token:
  - the request envelope and errors;
  - token upkeep;
  - listing, reading, commanding, and binding devices;
  - the device and state model.
- `cloud-watch`: real-time device-change notifications over the gateway websocket (STOMP),
  including heartbeat, reconnect, and token renewal.
- `device-cli`: the `gobluetti` command for listing, reading, commanding, and watching devices
  from a token file.

### Modified Capabilities

None. `login-cli` behavior stays the same; only its file-writing code moves to a shared internal
package.

## Impact

- New code:
  - `client.go`, `device.go`, `watch.go` and their tests in the root package;
  - `internal/tokenfile`;
  - `cmd/gobluetti`.
- `cmd/gobluetti-login` switches to `internal/tokenfile`.
- `go.mod` gains `golang.org/x/net`; consumers get one `require` line.
- External systems:
  - REST: `gw.bluettipower.com/api/bluiotdata/ha/...`;
  - websocket: `wss://gw.bluettipower.com/api/edgeiotgw/ws-coordination/websocket`;
  - SSO: token refresh.
- Safety: `Set` switches real outputs. Tests use fakes only, and real-device writes run only with
  the user, one command at a time.
