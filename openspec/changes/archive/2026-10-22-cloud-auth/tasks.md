# Tasks

## 1. Token model and errors

- [x] 1.1 Add `doc.go` with the package doc comment (cloud-only, unofficial) and verify with `go doc github.com/Niord/gobluetti` that it renders
- [x] 1.2 Implement `Token` with JSON tags (`expiry` as RFC 3339, `omitzero`/`omitempty`) and verify with a JSON round-trip test
- [x] 1.3 Implement `OAuthError` (status, code, description; `Error()` with no body or secret) and verify with tests for an OAuth2 JSON body, a non-JSON HTML body, and an oversized body (64 KiB limit)

## 2. Exchange, refresh, authorize URL

- [x] 2.1 Implement `Auth` with defaults (`SSOURL`, `ClientID`, `ClientSecret`, `HTTPClient`) and `AuthCodeURL`; verify by tests that the zero value targets `https://sso.bluettipower.com/oauth2/grant` with `client_id=HomeAssistant`, that overrides apply field by field, and that `redirect_uri` is URL-encoded
- [x] 2.2 Implement the token response parsing (`expires_in` as number or numeric string; missing `expires_in` gives a zero expiry; 2xx without `access_token` is an error) and verify with table tests
- [x] 2.3 Implement `Exchange`; verify against an `httptest` fake SSO that the form has `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`, and literal `client_secret`, and that the result's expiry is receive time + `expires_in`
- [x] 2.4 Implement `Refresh` with field-wise merge; verify by tests for a rotated refresh token, a response without `refresh_token`/`host` (old values kept), an empty refresh token (error, no request sent), and `invalid_grant` returning `*OAuthError` with code `invalid_grant`
- [x] 2.5 Verify that a cancelled context makes `Exchange`/`Refresh` return an error matching `context.Canceled`, and that no error message from any test in this group contains the code, refresh token, or client secret

## 3. Loopback login

- [x] 3.1 Implement `LoginLocal` (listen on `127.0.0.1:0`, `crypto/rand.Text()` state, `/callback` handler, first-callback-wins, 404 elsewhere, `defer srv.Close()`); verify with a test whose `openURL` acts as the browser and receives a token
- [x] 3.2 Verify the failure paths with tests: mismatched `state` sends no token request; `error=access_denied` returns an error containing `access_denied`; a context deadline returns `context.DeadlineExceeded`; an `openURL` error is returned; in every case the port is closed afterwards (a new dial to it fails)
- [x] 3.3 Verify that the listener address is `127.0.0.1` (test asserts the redirect URI host)

## 4. `gobluetti-login` CLI

- [x] 4.1 Implement `cmd/gobluetti-login` with `run(ctx, auth, args, stdout, stderr)`, `-o`, the interactive login, the browser opener, and the one-line stderr summary (expiry, host, `refresh token: yes/no`); verify with `go build ./cmd/gobluetti-login` and `gobluetti-login -h`
- [x] 4.2 Implement atomic `0600` file output and `-refresh <path>`; verify with `run` tests against a fake SSO: refreshed file has mode `0600`; a rejected refresh leaves the source byte-for-byte unchanged and returns an error naming the OAuth code; stdout carries only the JSON token when `-o` is absent
- [x] 4.3 Add a "Logging in" section to `README.md` and `README_uk.md` (CLI usage, `LoginLocal` and `AuthCodeURL`/`Exchange` snippet, token-file security note); verify that both READMEs have the same sections and that the snippet compiles (copied into an `Example` test)

## 5. Real-account check (run by the user)

- [x] 5.1 The user runs `go run ./cmd/gobluetti-login -o ~/.config/gobluetti/token.json` and logs in; record here only the stderr summary (expiry, host, refresh-token presence), never token values
  - 2026-10-22: the browser login succeeded. `Token written: expires 2026-11-22T22:21:34+02:00, host unknown, refresh token: yes`
- [x] 5.2 The user runs `go run ./cmd/gobluetti-login -refresh ~/.config/gobluetti/token.json`; record whether the expiry moved and whether the refresh token changed (compare a hash such as `shasum` before and after, not the value)
  - 2026-10-22: the refresh succeeded.
  - Refresh-token SHA-256 prefix `ad3f6bb543a4` became `99c2a50ae1ac`, so the token rotated.
  - Summary: `expires 2026-11-22T22:21:34+02:00, host unknown, refresh token: yes`. The expiry did not move.
- [x] 5.3 Write the answers to the design's Open Questions (host format, token lifetime, refresh-token rotation) into `design.md`; if `host` is not a full URL, note it for `cloud-client`

## 6. Final checks

- [x] 6.1 Run `gofmt -l .` (empty), `go vet ./...`, `go test -race ./...`, `go build ./...`, and verify all pass; paste the summary here
  - 2026-10-22, go1.27.3 darwin/arm64, module `go 1.26.0`:
    - `gofmt -l .`: empty;
    - `go vet ./...`: ok;
    - `go test -race -count=1 ./...`: ok for both packages; also stable with `-count=3`;
    - `go build ./...`: ok;
    - `go mod tidy -diff`: clean, no dependencies;
    - `golangci-lint` (golang-skills bundled config, includes gosec/noctx/errcheck): 0 issues;
    - `govulncheck`: not installed; no third-party modules to scan.
- [x] 6.2 Update the local `CLAUDE.md` layout block with the root package and `cmd/gobluetti-login`, and verify that `git status` does not list it (still ignored)
- [x] 6.3 Write the final review summary here: specs vs. implementation, deviations from upstream/HA, and remaining risks

  **Specs vs. implementation.**
  - Every `cloud-auth` requirement has a test in `auth_test.go` or `login_test.go`:
    - defaults and overrides;
    - authorize URL encoding;
    - the exchange and refresh forms;
    - refresh merge, including the missing `expires_in` case;
    - JSON round trip;
    - `OAuthError` for JSON, HTML, and oversized bodies;
    - no secrets in error text;
    - cancellation;
    - loopback success, wrong state, SSO error, missing code, timeout, `openURL` error, and the
      port closed afterwards.
  - Every `login-cli` requirement is tested through `run` in `cmd/gobluetti-login/main_test.go`,
    except the 5-minute limit and Ctrl-C. Those reduce to the `LoginLocal` context path, which
    `TestLoginLocalTimeout` covers, so no test waits 5 minutes.
  - The real-account check (section 5) confirmed the exchange, the refresh, and the file output
    against BLUETTI SSO.

  **Deviations from upstream/HA, and changes made during apply.**
  - A refresh response without `expires_in` gives a zero expiry. HA recomputes the expiry from
    the old `expires_in`. The spec was updated.
  - The default HTTP client has a 30s timeout instead of `http.DefaultClient`. The design was
    updated.
  - `run` takes the browser opener as a parameter, for tests. The design was updated.
  - Token-endpoint error bodies are never echoed; only the OAuth2 `error` and
    `error_description` are kept.
  - Like HA: no PKCE, and credentials go in the form body.

  **Remaining risks.**
  - Refresh did not extend the session; see the design's Open Questions. Users may need to log in
    again about every 30 days. To be checked around 2026-11-15, and handled in `cloud-client`.
  - SSO returned no `host`, so `cloud-client` must fall back to `gw.bluettipower.com`.
  - The client identity is upstream's `HomeAssistant`. If BLUETTI revokes it, consumers override
    the `Auth` fields.

## Workflow follow-up

- Commit and push after the user reviews the diff.
- Archive the change with `/opsx:archive` once section 5 is done.
- Next change: `cloud-client` (request headers, `msgCode 805`, lazy refresh with a token callback, `host` vs. `gw.bluettipower.com`).
