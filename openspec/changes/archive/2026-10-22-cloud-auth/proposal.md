# Proposal

## Why

Every gobluetti feature talks to the BLUETTI cloud. The cloud needs an OAuth2 access token for the
user's BLUETTI account. Upstream (the BLUETTI Home Assistant integration) does not implement the
OAuth2 flow itself. Home Assistant does it, so we have to write the flow in Go. This change comes
first because the device list, state, and control features all depend on it.

## What Changes

- New root package `github.com/Niord/gobluetti` with OAuth2 authorization-code primitives against
  BLUETTI SSO (`https://sso.bluettipower.com`):
  - build the authorize URL;
  - exchange an authorization code for a token;
  - refresh a token.
- The client identity defaults to the values upstream publishes (`client_id` `HomeAssistant`, and
  its client secret), so the zero value works. Callers can override the client ID, client secret,
  SSO base URL, and HTTP client.
- A `Token` type that carries the access token, the refresh token, the expiry, and the account's
  data-center `host` returned by SSO. It serializes to JSON so callers can store it.
- Typed OAuth errors, so callers can tell "refresh token rejected, log in again" apart from
  transport failures.
- A loopback login helper. It listens on `127.0.0.1` on a random port, hands the authorize URL to
  a caller-supplied function (for example, to open a browser), waits for the redirect, checks
  `state`, and exchanges the code.
- A command-line tool, `cmd/gobluetti-login`. It runs the loopback login on a machine with a
  browser and writes the token as JSON to stdout or to a `0600` file. It can also refresh a stored
  token file. This covers headless consumers, such as a service on a Raspberry Pi: log in once on
  a desktop and copy the token file over.

Out of scope, for the next change (`cloud-client`):

- calling the cloud API with the token: request headers, `msgCode 805`, and lazy auto-refresh with
  a token-change callback;
- choosing between the token `host` and `gw.bluettipower.com`;
- devices, state, control, and the websocket.

## Capabilities

### New Capabilities

- `cloud-auth`: obtaining and renewing BLUETTI cloud OAuth2 tokens from Go code. Covers the
  authorize URL, code exchange, refresh, the token model, error reporting, and the loopback login
  helper.
- `login-cli`: the `gobluetti-login` command-line tool for obtaining and refreshing a token file.

### Modified Capabilities

None. This is the first change; there are no existing specs.

## Impact

- New code: root package `gobluetti` (auth and loopback login) and `cmd/gobluetti-login`.
- Public API: first exported identifiers of the module. Once released, they form the compatibility
  contract.
- Dependencies: standard library only.
- External systems: BLUETTI SSO `/oauth2/grant` (browser) and `/oauth2/token` (HTTP). Tests use a
  local fake SSO. Real-account checks are manual and run by the user.
- Security: tokens never appear in logs or errors. The token file is created with mode `0600`.
  The loopback listener binds only to `127.0.0.1`.
