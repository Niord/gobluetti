# Design

## Context

- The repository has only `go.mod` (`github.com/Niord/gobluetti`, `go 1.26.0`). This change
  creates the first package, so it also sets the package layout.
- Upstream (`bluetti-official/bluetti-home-assistant` at `70b3065`) supplies only configuration:
  - `profile/application.yaml`: `sso: https://sso.bluettipower.com`, `client-id: HomeAssistant`,
    `client-secret: SG9tZUFzc2lzdGFudA==`;
  - `application_credentials.py`: the authorize URL is `<sso>/oauth2/grant` and the token URL is
    `<sso>/oauth2/token`;
  - `config_flow.py`: imports the client ID and secret as-is (the secret is not decoded).
- The OAuth2 mechanics come from Home Assistant core
  (`helpers/config_entry_oauth2_flow.py`, `LocalOAuth2Implementation`), not from upstream:
  - the authorize URL carries `response_type=code`, `client_id`, `redirect_uri`, and `state`;
  - token requests are form-encoded POSTs, with `client_id` and `client_secret` in the body;
  - on refresh, HA merges the new response over the old token (`{**old, **new}`), so a missing
    `refresh_token` keeps the old one;
  - HA derives `expires_at` from `expires_in`.

  Upstream reads `token["expires_at"]`, `token["refresh_token"]` and, if present,
  `token["host"]`.
- The user verified in a browser that SSO accepts `redirect_uri=http://127.0.0.1:8765/callback`
  and redirects there with `?code=...`.

## Goals / Non-Goals

**Goals:**
- Standard library only.
- The zero value is usable: `var a gobluetti.Auth` works against the real SSO with upstream's
  identity.
- The loopback login and the CLI are thin layers over the exchange and refresh primitives, with
  no OAuth logic of their own.
- Everything is testable offline against a fake SSO (`httptest`).

**Non-Goals:**
- Automatic refresh, token-change callbacks, and API request headers belong to `cloud-client`.
- PKCE, device-code flow, and scopes: upstream and HA use none of them, and we would have no way
  to verify that SSO supports them.
- Token storage beyond the CLI's file. The library itself writes no files.

## Decisions

**1. One root package, plus one command.**
`github.com/Niord/gobluetti` holds `auth.go` (`Auth`, `Token`, `OAuthError`, exchange and
refresh) and `login.go` (loopback login). `cmd/gobluetti-login/main.go` is the tool.
- *Alternative:* a separate `auth` subpackage. Rejected: consumers would import two paths, and
  the cloud client in the next change needs `Token` right next to it anyway.

**2. `Auth` is a plain struct whose zero value uses the defaults.**

```go
type Auth struct {
    SSOURL       string       // default https://sso.bluettipower.com
    ClientID     string       // default HomeAssistant
    ClientSecret string       // default SG9tZUFzc2lzdGFudA==
    HTTPClient   *http.Client // default: a package client with a 30s timeout (http.DefaultClient has none)
}
func (a *Auth) AuthCodeURL(redirectURI, state string) string
func (a *Auth) Exchange(ctx context.Context, code, redirectURI string) (*Token, error)
func (a *Auth) Refresh(ctx context.Context, t *Token) (*Token, error)
func (a *Auth) LoginLocal(ctx context.Context, openURL func(url string) error) (*Token, error)
```

This follows the `http.Client` pattern: empty fields fall back to unexported defaults.
- *Alternative:* a `NewAuth(opts...)` constructor with functional options. Rejected: more API
  surface for four optional fields.
- *Alternative:* `golang.org/x/oauth2`. Rejected for three reasons:
  - it adds a dependency for about 60 lines of `net/http`;
  - its `Token` hides `host` behind `Extra("host")`;
  - its `Transport` sends `Authorization: Bearer …`, while BLUETTI expects the raw token, so the
    cloud client could not reuse it anyway.

**3. `Token` is a plain JSON-tagged struct.**

```go
type Token struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token,omitempty"`
    Expiry       time.Time `json:"expiry,omitzero"`
    Host         string    `json:"host,omitempty"`
}
```

- `Expiry` is computed as receive time + `expires_in`, because an absolute time survives
  storage, while `expires_in` is meaningless once saved.
- `time.Time` marshals as RFC 3339 by default.
- Refresh copies the old token and overwrites only the fields present in the response, which
  matches HA's merge.

**4. Tolerant parsing of the token response.**
The wire struct reads `access_token`, `refresh_token`, `host` and `expires_in`. HA casts with
`int(token["expires_in"])`, which hints that the value may arrive as a string, so `expires_in`
accepts both a JSON number and a numeric string (a small `UnmarshalJSON`). A missing `expires_in`
gives a zero `Expiry`. A 2xx response without `access_token` is an error.

**5. Errors.**

```go
type OAuthError struct {
    StatusCode  int
    Code        string // OAuth2 "error", empty if the body is not OAuth2 JSON
    Description string // OAuth2 "error_description"
}
```

- A non-2xx response returns `*OAuthError`. The body is read through `io.LimitReader` (64 KiB).
  Only `error` and `error_description` are kept; the raw body is never put into the message.
- Transport errors are wrapped as `fmt.Errorf("gobluetti: token request: %w", err)`, so
  `errors.Is(err, context.Canceled)` works.
- Credentials travel only in the POST body. The `*url.Error` text contains the URL, and the URL
  holds no secrets.

**6. Loopback login.**
- The listener is `net.Listen("tcp", "127.0.0.1:0")`. The OS picks the port, and the redirect
  URI becomes `http://127.0.0.1:<port>/callback`.
- `state` comes from `crypto/rand.Text()` (stdlib since Go 1.24).
- An `http.Server` with `ReadHeaderTimeout` serves `/callback` and returns 404 for every other
  path, such as `/favicon.ico`. The first callback is accepted through a buffered channel of size
  1 and a `sync.Once`; later callbacks get a plain "already handled" response.
- The handler checks `error`, then `state`, then the presence of `code`. It answers the browser
  with a short text page, and `LoginLocal` performs the exchange after the handler returns.
- `LoginLocal` waits on the callback channel and on `ctx.Done()`. `defer srv.Close()` frees the
  port on every path, including an error from `openURL`.
- *Alternative:* a fixed port. Rejected: it collides with other programs, and SSO accepts any
  port.

**7. CLI.**
- Flags: `-o <path>` and `-refresh <path>`, parsed with the `flag` package.
- `main` only wires up `signal.NotifyContext(os.Interrupt)` with a 5-minute timeout and calls
  `run(ctx, auth *gobluetti.Auth, openBrowser func(string) error, args []string, stdout, stderr io.Writer) error`.
  Tests call `run` with an `Auth` that points at the fake SSO and an `openBrowser` that plays the
  browser, so the binary needs no hidden test flag.
- The browser opener is `open` on darwin, `rundll32 url.dll,FileProtocolHandler` on windows, and
  `xdg-open` elsewhere. It always prints the URL to stderr first and ignores the opener's error,
  so `openURL` never fails in the CLI.
- File writes are atomic: `os.CreateTemp` in the target directory (mode `0600` by default),
  write, `Sync`, `Close`, then `os.Rename`. A failed refresh never writes, so the source stays
  intact.

**8. Testing.**
`auth_test.go` runs against an `httptest.Server` fake SSO and covers:
- the form fields of exchange and refresh;
- merge on refresh;
- both `expires_in` forms;
- `OAuthError` parsing and that errors never contain the code or secret;
- `AuthCodeURL` encoding.

`login_test.go` drives `LoginLocal` with an `openURL` that acts as the browser: it parses
`redirect_uri` and `state` from the URL and sends a GET to the callback. It covers success,
mismatched state, `error=access_denied`, and context timeout.

`cmd/gobluetti-login` tests cover the refresh path through `run`: `0600` on the result, and the
source left unchanged when the refresh is rejected.

## Risks / Trade-offs

- **[BLUETTI revokes or restricts `client_id=HomeAssistant`]** → The identity is overridable
  through `Auth` fields, so consumers can switch without a library release. The README already
  states that the project is unofficial.
- **[SSO starts rejecting loopback redirects]** → `AuthCodeURL`/`Exchange` take any redirect
  URI, so consumers can use their own registered URL. Only `LoginLocal` and the CLI would stop
  working.
- **[Token response shape differs from the HA assumptions]** → Parsing is tolerant
  (Decision 4). The first real login (tasks, section 5) confirms the actual fields before
  archive.
- **[Token file is plaintext on disk]** → The file is created with mode `0600`, and stdout is the
  default so the user picks the storage. Documented in the README.
- **[Another local process calls the callback port]** → Random `state` plus first-callback-wins:
  a wrong `state` fails the login instead of accepting a foreign code.
- **[Impersonating the HA integration]** → A deliberate decision by the project owner
  (2026-10-22), based on values that BLUETTI publishes under MIT.

## Open Questions

These were answered by the manual check in tasks section 5 (2026-10-22, the project owner's
account). None of them changed the API or the tasks.

- **Does the token response include `host`?** No: for this account SSO returned no `host`, and
  `Token.Host` stays empty. In that case upstream uses `gw.bluettipower.com` for REST and the
  websocket. For `cloud-client`, the fallback is therefore the main path, not an edge case.
- **How long does an access token live?** About 30 days: the expiry after login was
  2026-11-22T22:21:34+02:00.
- **Does refresh rotate the refresh token?** Yes. The SHA-256 prefix changed from `ad3f6bb543a4`
  to `99c2a50ae1ac` after one `-refresh`. Callers must persist the token returned by every
  `Refresh`.

New finding, open for `cloud-client`:

- **Refresh did not extend the expiry.** After `-refresh` the expiry was the same to the second
  (2026-11-22T22:21:34+02:00). This suggests SSO reports `expires_in` relative to a fixed session
  end, and a refresh does not move that end. Upstream refreshes when fewer than 7 days remain.
  Whether a refresh inside that window extends the session cannot be checked until about
  2026-11-15. If it does not, users must log in again about every 30 days, and `cloud-client`
  needs a clear "log in again" error and documentation for it.
