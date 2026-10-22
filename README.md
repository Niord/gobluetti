# gobluetti

[🇬🇧 English](./README.md) | [🇺🇦 Українська](./README_uk.md)

A Go library for monitoring and controlling BLUETTI power stations through the BLUETTI cloud.

It is a Go port of the cloud part of the official
[BLUETTI Home Assistant integration](https://github.com/bluetti-official/bluetti-home-assistant),
without any Home Assistant dependencies. This is an unofficial project, not affiliated with BLUETTI.

> **Status: early development.** Only logging in (OAuth2 tokens) is implemented so far, and the API
> may still change. Do not depend on this module until the first tagged release.

## Scope

- **Cloud only.** The library talks to the BLUETTI cloud with your BLUETTI account
  (OAuth2). It needs internet access, and the station must be online in the BLUETTI app.
- **No Bluetooth.** Local BLE control is out of scope (see below).
- Supported stations are the ones marked "Cloud Control" in the
  [upstream support table](https://github.com/bluetti-official/bluetti-home-assistant#-power-station-support-list),
  for example AC300 and Apex 300 (AP300).
- Planned features follow upstream: device list, state (battery SOC, PV, grid, AC and DC power),
  AC/DC output switches, ECO modes, work mode, and live updates.

## Why no Bluetooth

The official integration does not implement the BLE protocol in its source code. Encryption and
frame decoding are in a closed native library that it downloads at runtime. That library is built
for Linux and CPython only, and Go cannot use it. Community re-implementations depend on keys
extracted from the BLUETTI app. We do not want to ship those keys or tie the library to a binary
blob, so gobluetti uses the cloud only.

If you need local Bluetooth control, look at
[bluetti-bt-lib](https://github.com/Patrick762/bluetti-bt-lib) (Python).

## Requirements

- Go 1.26 or newer.
- A BLUETTI account with the station added in the BLUETTI app.

## Installation

```bash
go get github.com/Niord/gobluetti
```

## Logging in

Every call needs an OAuth2 token for your BLUETTI account. There are three ways to get one.

**Command line, for headless services.** Run the login on a machine with a browser and copy
the token file to the server:

```bash
go run github.com/Niord/gobluetti/cmd/gobluetti-login@latest -o token.json
# later, renew a stored token without a browser:
go run github.com/Niord/gobluetti/cmd/gobluetti-login@latest -refresh token.json
```

**From Go, on a machine with a browser.** `LoginLocal` listens on `127.0.0.1`, waits for the
browser to come back from the BLUETTI login page, and returns the token:

```go
var auth gobluetti.Auth // zero value: BLUETTI SSO with the default client identity

tok, err := auth.LoginLocal(ctx, func(url string) error {
	fmt.Println("Open this URL to log in:", url)
	return nil
})
// Store tok (it marshals to JSON) and renew it before it expires:
tok, err = auth.Refresh(ctx, tok)
```

**From a web application.** Use your own redirect URL with `AuthCodeURL` and `Exchange`; see
`ExampleAuth_Exchange` in the package documentation.

`Refresh` returns an `*OAuthError` with `Code == "invalid_grant"` when the refresh token is no
longer valid; log in again in that case.

The token grants full access to the account. `gobluetti-login` creates token files with mode
`0600`; keep them out of version control and backups that others can read.

## Safety

The library can switch the outputs of a real power station. Turning off AC or DC output cuts
power to everything plugged into it.

## License

[MIT](./LICENSE). Includes code ported from the BLUETTI Home Assistant integration, Copyright (c) 2025 Bluetti.
