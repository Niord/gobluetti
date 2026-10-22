# Design

## Context

- Already in place: `Auth` (exchange and refresh), `Token` (`Host` empty for the owner's
  account), and `OAuthError` in the root package; `cmd/gobluetti-login` with its own atomic
  token-file writer.
- Upstream at `70b3065`:
  - `api/bluetti.py`: headers `Authorization: <raw token>`, `x-os: open`, `x-app-key`, and
    `x-app-ver`. The last one is set to the integration version, `1.0.5` (`manifest.json`). The
    gateway comes from `profile/application.yaml`.
  - `api/product_client.py`:
    - `GET /ha/v2/devices`;
    - `GET /ha/v1/deviceStates?sns=`;
    - `POST /ha/v1/fulfillment {sn, fnCode, fnValue}`;
    - `POST /ha/v1/bindDevices {bindSnList}`.
  - `api/unify_response.py`: the envelope `{msgId, msgCode, data}`.
  - `model/product.py`, `models.py`:
    - `stateList` entries carry `fnCode`, `fnName`, `fnValue`, `fnType`, `supportModeValues`,
      and `sensorInfo{sensorType, unit}`;
    - `online` and `isBindByCurUser` are the strings `"1"`/`"0"`;
    - a device with `isBindByCurUser == "0"` is handled as unbound.
  - `api/websocket.py`, `__init__.py`:
    - `wss://<host>/api/edgeiotgw/ws-coordination/websocket`;
    - STOMP `CONNECT` with the API headers plus `accept-version` and `heart-beat`;
    - `CONNECTED` carries a `user-name` header;
    - the client subscribes with `SUBSCRIBE /ws-subscribe/user/<user-name>/notify`;
    - the `MESSAGE` body is `{"data":{"message":{"deviceSn":...}}}`;
    - an `ERROR` whose `message` header (with `\c` escaped) holds `msgCode` 400, 403, 600, or
      805 means a token problem;
    - reconnect backoff is 1 s, doubling up to 30 s.
  - `oauth.py` `AuthTokenRefresh`: refresh when fewer than 7 days are left, at most once per
    hour.
- From the `cloud-auth` real-account check:
  - SSO returns no `host`;
  - access tokens live about 30 days;
  - refresh tokens rotate;
  - a refresh did not move the expiry.

## Goals / Non-Goals

**Goals:**
- One `Client` type carries everything: REST, token upkeep, and `Watch`.
- Model-independent: the library passes the cloud's state list through and never maps fields per
  model.
- Offline tests against fakes: an `httptest` REST gateway, SSO, and an `x/net/websocket` server.
- The only new module is `golang.org/x/net`.

**Non-Goals:**
- Client-side validation of command values, typed value accessors, and polling helpers.
- Telling "device offline" apart from other command failures. The cloud's `msgCode` is passed
  through as is.
- Handling an unbound device beyond exposing the flag.

## Decisions

**1. A constructor plus optional fields.**

```go
func NewClient(auth *Auth, tok *Token) *Client

type Client struct {
    OnToken    func(*Token) // optional; called with every new token, serialized
    GatewayURL string       // optional; default https://gw.bluettipower.com; Token.Host wins
    HTTPClient *http.Client // optional; default: the 30s client from auth.go
    // unexported: auth, mu, tok, lastRefresh, backoff
}
func (c *Client) Token() *Token // copy of the current token
func (c *Client) Devices(ctx context.Context) ([]Device, error)
func (c *Client) Device(ctx context.Context, sn string) (*Device, error) // filters Devices (see Open Questions)
func (c *Client) Set(ctx context.Context, sn, code, value string) error
func (c *Client) Bind(ctx context.Context, sns ...string) error
func (c *Client) Watch(ctx context.Context, onChange func(sn string)) error
```

- The token changes under the client's control, so it is unexported behind a mutex and read with
  `Token()`. An exported `Token` field would race.
- Optional settings are plain fields, like `Auth`, and are set before first use.
- A nil `auth` means `&Auth{}`.
- *Alternative:* functional options. Rejected: three optional fields do not need them.

**2. Errors.**
- `ErrLoginRequired` and `ErrDeviceNotFound` are sentinels.
- `*APIError{StatusCode, MsgCode int; MsgID string}` covers both a non-2xx HTTP status and a
  non-zero `msgCode`. That is one type for "the gateway said no", not two.
- A failed renewal returns `fmt.Errorf("gobluetti: %w: %w", ErrLoginRequired, cause)`. Then
  `errors.Is` matches the sentinel and `errors.AsType[*OAuthError]` still finds the SSO reason.
- Bodies are read with a 1 MiB limit and are never put into error text.

**3. One request path with token upkeep.** Every method calls
`c.call(ctx, method, path, query, body, out)`:

1. **Fresh-token step (under `mu`).**
   - If the expiry is known, less than 7 days away, and more than 1 hour has passed since
     `lastRefresh`: set `lastRefresh`, then refresh.
   - On success, replace the token and call `OnToken`.
   - On failure, keep the old token unless it has already expired.
2. Send the request with the token taken in step 1.
3. **On `msgCode` 805, renew (under `mu`).**
   - If the current token differs from the one used, another goroutine already renewed it, so
     reuse that one.
   - Otherwise refresh; a failure gives `ErrLoginRequired`.
   - Then retry once. A second 805 also gives `ErrLoginRequired`.

Holding `mu` during the refresh makes concurrent calls wait for one refresh instead of each
sending its own. `OnToken` runs under `mu`, so calls are serialized and ordered. It must not call
back into the `Client`; this is documented.
- *Alternative:* `golang.org/x/sync/singleflight`. Rejected: a mutex plus the "did the token
  change" check already gives one refresh, and it adds no import.

**4. Device model.**

```go
type Device struct {
    SN, Name, Model  string
    Online           bool // online == "1"
    Bound            bool // isBindByCurUser != "0" (upstream acts only on "0")
    NetworkSupported bool // supportNetwork == "1"
    States           []State
}
func (d *Device) State(code string) (State, bool)

type State struct {
    Code, Name, Value string
    Type              StateType // "SENSOR", "SWITCH", "SELECT"
    Options           []Option  // SELECT only
    SensorType, Unit  string    // from sensorInfo, may be empty
}
type Option struct{ Code, Name string }
```

- Function-code constants follow `icon_config.py`: `FnSOC`, `FnACOutput` (`SetCtrlAc`),
  `FnDCOutput`, `FnPower` (`SetCtrlPowerOn`), `FnSleep` (`SetCtrlPowerOn-2`), `FnWorkMode`,
  `FnACECO`, `FnDCECO`, `FnStormMode`, `FnInverterState`, `FnPVPower`, `FnGridPower`,
  `FnACLoadPower`, `FnDCLoadPower`, and `FnChargeFullTime`.
- The wire decoding lives in function-local structs. One package-level helper, `flexString`
  (`UnmarshalJSON`), accepts `fnValue` both as a JSON string and as a number. Upstream compares
  values as strings, but the wire type is not guaranteed.

**5. Watch over `golang.org/x/net/websocket`.**
- **Dial.** `websocket.NewConfig(wsURL, origin)` with `origin` = the https gateway URL, then
  `cfg.DialContext(ctx)`. One STOMP frame per websocket text message is sent with
  `websocket.Message.Send`. Received messages may hold several NUL-terminated frames or a bare
  `\n` heartbeat; the parser splits them.
- **STOMP.** It is about 70 lines of unexported code in `watch.go`:
  - frame encode;
  - frame decode, with the STOMP 1.2 header unescaping of `\c`, `\n`, `\r`, `\\`.
- **Session.**
  1. Dial and send `CONNECT`.
  2. Wait up to 30 s for `CONNECTED`; a `user-name` is required.
  3. Send `SUBSCRIBE id:0 destination:/ws-subscribe/user/<u>/notify ack:auto`.
  4. Read in a loop with `SetReadDeadline`.
- **Heartbeat.**
  - The client offers `heart-beat:10000,10000`.
  - It sends every `max(10s, server receive)` from a goroutine started with `wg.Go`. That
    goroutine stops when the session ends: the session context is cancelled and the connection
    closed, then `wg.Wait`.
  - No write mutex: `x/net/websocket.Conn` is documented as safe for concurrent use.
  - The read deadline is `2 × max(server send, 10s)`, or 2 minutes when the server sends 0.
  - Upstream offers 60 s. We offer 10 s so that a lost connection is noticed sooner; the server
    takes the maximum anyway.
- **Reconnect.**
  - Backoff is 1 → 2 → 4 … → 30 s through a timer in a `select` with `ctx.Done()`. It resets
    after `CONNECTED`.
  - An auth `ERROR` (400, 403, 600, 805) renews the token through the same step as decision 3,
    then reconnects at once. A failed renewal returns `ErrLoginRequired`.
- **Callback.** It is called from the read loop, so it runs on a single goroutine. A slow
  callback delays reading. This is documented; the caller can hand work off itself.
- **Tests.** In-package tests set `c.backoff` and `c.heartbeat` (the 10 s floor) to
  milliseconds. The fake server is
  `websocket.Handler` from the same package, served by `httptest`.
- *Alternatives:* a hand-written websocket client was rejected because the user chose the Go
  team's package. `coder/websocket` and `gorilla/websocket` were rejected as third-party.

**6. `internal/tokenfile` holds the shared file code.**
- `Load(path) (*gobluetti.Token, error)` and `Save(path, tok) error`: atomic, `0600`, the same
  code as today's `writeFileAtomic`.
- `DefaultPath()` returns `~/.config/gobluetti/token.json`. It deliberately does not use
  `os.UserConfigDir`, which on macOS is `~/Library/Application Support`, so that the path is the
  same on Linux and Mac, as the README shows.
- `gobluetti-login` switches to it with no behavior change; its tests stay as they are.

**7. `cmd/gobluetti`.**
- `run(ctx, auth *gobluetti.Auth, gatewayURL string, args []string, stdout, stderr io.Writer)
  error` is called by `main` with zero values, and by tests with fakes.
- The global flag is `-token`, then a subcommand.
- Output goes through `text/tabwriter`.
- `OnToken` saves the token through `tokenfile.Save`. A save failure is printed to stderr and
  does not fail the command.
- `watch` runs until Ctrl-C (`signal.NotifyContext`).
- `ErrLoginRequired` leads to "run gobluetti-login" and exit status 1.

**8. Real-account checks are manual and separate for read and write** (see tasks):
- read-only `devices` and `device`;
- `watch` while the user changes something in the BLUETTI app;
- exactly one `set` that the user picks and runs themselves, followed by a `set` back.

## Risks / Trade-offs

- **[`deviceStates` may return only values, without names, types, or options; upstream only
  copies `fnValue` from it]** → `Device` returns whatever arrives. The manual check records the
  real shape. If metadata is missing, a follow-up can merge it with `Devices`. The API would not
  change.
- **[Bind may be required before `deviceStates`, commands, or notifications work for a
  device]** → `Bind` is exposed. The manual check records whether the owner's devices are
  already bound (`Bound`).
- **[Refresh does not extend the ~30-day session]** → Upkeep still follows upstream. When the
  session really ends, users get a clear `ErrLoginRequired` and the CLI says to run
  `gobluetti-login`.
- **[Proactive refresh at most hourly in the last week]** → It is bounded at about 168 extra SSO
  calls in the worst case, the same as upstream.
- **[`x/net` raises its `go` directive above ours later]** → We stay on the last compatible
  `x/net` until we raise `go` ourselves, which CLAUDE.md treats as a behavior change.
- **[A STOMP frame split across websocket messages]** → Not seen with Spring-style brokers. The
  parser treats an unterminated frame as "wait for more" within one session.
- **[A command reaches a real device]** → Tests use fakes only. Real writes follow tasks section
  8, under the user's control.

## Open Questions

Answered by the real-account check on 2026-10-23 (tasks section 6). Two answers changed the
implementation:

- **Does `deviceStates` return full metadata?** No, and it is worse than that.
  - For the online AP300 it returned a placeholder: `online:"0"`, `isBindByCurUser:"0"`,
    `name`/`model` null, and an empty `stateList`, while `/ha/v2/devices` showed the device
    online, bound, and with 13 states.
  - For the AC300 (switched off) it returned values without `supportModeValues` and
    `sensorInfo`.
  - **Changed:** `Device(sn)` now filters `/ha/v2/devices`, which returns live values in about
    0.3 s.
  - Upstream would mark the AP300 unbound from `deviceStates`; we avoid that.
- **Bound / notifications without `Bind`?**
  - Notifications arrive without `Bind`.
  - Commands do not. They fail with `msgCode 403` "device does not belong to the current user",
    although the device list shows `isBindByCurUser:"1"`. Only `deviceStates` reflects the
    binding for this client.
  - After `Bind` (upstream calls it once in `oauth.py` during setup), commands work and
    `deviceStates` returns real data. It still has no options or sensor info, so `Device` stays
    on the device list.
  - **Changed:**
    - `APIError.Message` carries the gateway's text;
    - `gobluetti bind` was added;
    - the README says to bind each device once.
- **Heartbeat announced by the gateway?** It announces `heart-beat:0,0` and sends none.
  - **Changed:** `Watch` sends a newline every 60 s when the server requests no heartbeats, as
    upstream does.
  - The silent read timeout is 10 minutes instead of 2. With 2 minutes, every quiet period
    would have dropped and rebuilt the session.
- **Also found:**
  - the cloud sends `unit: null` for power and duration, so units now default by sensor type,
    as upstream's `sensor.py` does;
  - the AP300 reports `SetSleepMode` and `DsgFullTime`, so constants were added;
  - work modes use codes `workmode_0`…`workmode_3`;
  - on the AP300, `SetACECO`/`SetDCECO` are of type `SWITCH`.
