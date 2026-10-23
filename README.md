# gobluetti

[🇬🇧 English](./README.md) | [🇺🇦 Українська](./README_uk.md)

A Go library for monitoring and controlling BLUETTI power stations through the BLUETTI cloud.

It is a Go port of the cloud part of the official
[BLUETTI Home Assistant integration](https://github.com/bluetti-official/bluetti-home-assistant),
without any Home Assistant dependencies. This is an unofficial project, not affiliated with BLUETTI.

> **Status: v0, before a stable API.** Login, device list and state, commands, and real-time
> change notifications work and were checked against a real account. Until v1.0.0 the API may
> change between minor versions, so pin a version.

## Scope

- **Cloud only.** The library talks to the BLUETTI cloud with your BLUETTI account
  (OAuth2). It needs internet access, and the station must be online in the BLUETTI app.
- **No Bluetooth.** Local BLE control is out of scope (see below).
- Supported stations are the ones marked "Cloud Control" in the
  [upstream support table](https://github.com/bluetti-official/bluetti-home-assistant#-power-station-support-list),
  for example AC300 and Apex 300 (AP300).
- Features follow upstream: device list, state (battery SOC, PV, grid, AC and DC power),
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
- One dependency: `golang.org/x/net` from the Go team, for the websocket only.

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

## Using the client

```go
c := gobluetti.NewClient(nil, tok)
c.OnToken = func(tok *gobluetti.Token) { /* persist it */ }

devs, err := c.Devices(ctx) // every station with its states
soc, _ := devs[0].State(gobluetti.FnSOC)
fmt.Println(devs[0].Model, soc.Value, soc.Unit)

err = c.Set(ctx, devs[0].SN, gobluetti.FnACOutput, "0") // AC output off

err = c.WatchUpdates(ctx, func(u gobluetti.Update) { // blocks; called for every push
	fmt.Println(u.SN, u.Values[gobluetti.FnSOC])
})
```

- A station is a list of states, exactly as the cloud describes it. Every state has a function
  code, a string value, and a type: `SENSOR`, `SWITCH` (`"1"`/`"0"`), or `SELECT`, which comes
  with its `Options`. The `Fn*` constants name the codes the official integration knows. Run
  `gobluetti device <sn>` to see what your station reports.
- The client renews the token by itself and calls `OnToken` with every new one. **Persist it**:
  BLUETTI rotates refresh tokens, so the old token stops working. When renewal is impossible,
  calls fail with `ErrLoginRequired`: log in again.
- **Bind each device once** with `c.Bind(ctx, sn)` (or `gobluetti bind <sn>`), as the official
  integration does during setup. Until then commands fail with `msgCode 403` ("device does not
  belong to the current user"), even though the device list shows the device.
- A `Client` is safe for concurrent use.
- API calls time out after 15 seconds unless you set `c.HTTPClient`.

### Live state from pushes

The cloud's REST API (`Devices`, `Device`, `DeviceStates`) can hang or time out for hours while
its websocket keeps pushing station readings about once a minute. `WatchUpdates` gives you those
readings without any REST call:

```go
last := map[string]map[string]string{} // sn → function code → value
err = c.WatchUpdates(ctx, func(u gobluetti.Update) {
	if last[u.SN] == nil {
		last[u.SN] = map[string]string{}
	}
	maps.Copy(last[u.SN], u.Values) // a push carries only some readings: merge, do not replace
})
```

- `u.Values` holds readings by function code: SOC, grid, AC, PV and DC power, the AC output
  switch, and the battery time left. `u.Fields` holds every reading under the cloud's own
  names, for example `batteryVoltage`. `u.Time` is when the station reported.
- The callback runs on the goroutine that reads the websocket, one push at a time. **Return
  quickly**: hand the update to your own goroutine and do REST calls there. A callback that
  blocks delays every later push.
- `Watch(ctx, func(sn string))` still works and reports only which station changed.
- `c.DeviceStates(ctx, sn1, sn2)` reads current values for several stations in one request. It
  is lighter than `Devices`, but has no names, options, or units.

## Command line

```bash
go install github.com/Niord/gobluetti/cmd/gobluetti@latest
gobluetti devices                    # serial number, model, name, online/offline
gobluetti device <sn>                # every state: code, type, value, options
gobluetti set <sn> SetACECO 1        # one command; switches the real station
gobluetti watch                      # prints a line whenever a station changes
gobluetti bind <sn> [<sn>]           # once per station, before the first set
```

It reads `~/.config/gobluetti/token.json` (`-token` to change it), the file that
`gobluetti-login -o` writes, and saves renewed tokens back to it.

## Safety

The library can switch the outputs of a real power station. Turning off AC or DC output cuts
power to everything plugged into it.

## License

[MIT](./LICENSE). Includes code ported from the BLUETTI Home Assistant integration, Copyright (c) 2025 Bluetti.
