# cloud-auth Specification

## Purpose

Lets Go programs obtain and renew OAuth2 tokens for a user's BLUETTI cloud account, which every
other cloud feature of the library needs.

## Requirements

### Requirement: Default client identity
With no client settings, the library SHALL identify itself to BLUETTI SSO with the values the
upstream integration publishes: SSO base URL `https://sso.bluettipower.com`, client ID
`HomeAssistant`, and client secret `SG9tZUFzc2lzdGFudA==`. The secret is sent as that literal
string, not base64-decoded.

#### Scenario: Zero-value configuration
- **WHEN** a caller builds an authorize URL or requests a token without setting any client fields
- **THEN** the request goes to `https://sso.bluettipower.com` with client ID `HomeAssistant` and, for token requests, client secret `SG9tZUFzc2lzdGFudA==`

### Requirement: Overridable client identity
The caller SHALL be able to override the SSO base URL, the client ID, the client secret, and the
HTTP client used for token requests. Each value it does not set keeps its default.

#### Scenario: Custom SSO and client ID
- **WHEN** a caller sets the SSO base URL to `http://127.0.0.1:9000` and the client ID to `my-app`, and leaves the secret unset
- **THEN** token requests go to `http://127.0.0.1:9000/oauth2/token` with client ID `my-app` and the default client secret

### Requirement: Authorize URL
The library SHALL build the browser authorize URL as `<sso>/oauth2/grant` with the query
parameters `response_type=code`, `client_id`, `redirect_uri`, and `state`, all URL-encoded. It
SHALL return the URL without making any network request.

#### Scenario: URL for a loopback redirect
- **WHEN** a caller asks for the authorize URL with redirect URI `http://127.0.0.1:8765/callback` and state `abc`
- **THEN** the result is `https://sso.bluettipower.com/oauth2/grant` with `response_type=code`, `client_id=HomeAssistant`, `redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcallback`, and `state=abc`

### Requirement: Code exchange
The library SHALL exchange an authorization code for a token. It sends a form-encoded POST to
`<sso>/oauth2/token` with `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`,
and `client_secret`. The redirect URI must equal the one used in the authorize URL.

#### Scenario: Successful exchange
- **WHEN** SSO answers the exchange with HTTP 200 and JSON containing `access_token`, `refresh_token`, `expires_in` and `host`
- **THEN** the library returns a token with those access and refresh tokens, the host, and an expiry equal to the receive time plus `expires_in` seconds

#### Scenario: Response without expires_in
- **WHEN** a successful token response has no `expires_in`
- **THEN** the returned token has a zero (unknown) expiry, and the call does not fail

### Requirement: Token refresh
The library SHALL renew a token with a form-encoded POST to `<sso>/oauth2/token` with
`grant_type=refresh_token`, `refresh_token`, `client_id`, and `client_secret`. A `refresh_token`
or `host` missing from the response SHALL keep its previous value, so a server that does not
rotate refresh tokens keeps the old one. A missing `expires_in` SHALL give a zero (unknown)
expiry, not the old one.

#### Scenario: Server rotates the refresh token
- **WHEN** the refresh response contains a new `refresh_token`
- **THEN** the returned token carries the new access token, the new refresh token, and the new expiry

#### Scenario: Server does not return refresh_token or host
- **WHEN** the refresh response contains `access_token` and `expires_in` but no `refresh_token` and no `host`
- **THEN** the returned token keeps the previous refresh token and host

#### Scenario: Refresh response without expires_in
- **WHEN** the refresh response contains `access_token` but no `expires_in`
- **THEN** the returned token has a zero expiry

#### Scenario: Token without refresh token
- **WHEN** a caller asks to refresh a token whose refresh token is empty
- **THEN** the library returns an error without making a network request

### Requirement: Token serialization
A token SHALL round-trip through JSON without loss, using the fields `access_token`,
`refresh_token`, `expiry` (RFC 3339) and `host`. This lets callers store tokens in files or
databases.

#### Scenario: JSON round trip
- **WHEN** a token is marshaled to JSON and unmarshaled back
- **THEN** the access token, refresh token, expiry and host are equal to the original

### Requirement: OAuth error reporting
When SSO rejects a token request with a non-2xx status, the library SHALL return an error that
exposes the HTTP status and, when the body is an OAuth2 error JSON, its `error` and
`error_description` values. Callers can then detect `invalid_grant`, which means a new login is
needed. Transport failures and context cancellation SHALL be returned as distinct, wrapped errors.

#### Scenario: Revoked refresh token
- **WHEN** SSO answers a refresh with HTTP 400 and `{"error":"invalid_grant"}`
- **THEN** the returned error exposes status 400 and error code `invalid_grant`

#### Scenario: Non-JSON error body
- **WHEN** SSO answers with HTTP 500 and an HTML body
- **THEN** the returned error exposes status 500 and an empty error code

#### Scenario: Cancelled context
- **WHEN** the caller's context is cancelled while a token request is in flight
- **THEN** the call returns promptly with an error that matches `context.Canceled`

### Requirement: No secret leakage
The library SHALL NOT write access tokens, refresh tokens, authorization codes, or the client
secret to logs, and SHALL NOT include them in error messages.

#### Scenario: Error from a failed exchange
- **WHEN** a code exchange fails with an OAuth error
- **THEN** the error message contains neither the authorization code nor the client secret

### Requirement: Loopback login
The library SHALL run a complete browser login. It listens on `127.0.0.1` on an OS-assigned port,
uses `http://127.0.0.1:<port>/callback` as the redirect URI, passes the authorize URL to a
caller-supplied function, waits for the redirect, and returns the exchanged token.

#### Scenario: Successful login
- **WHEN** the browser is redirected to the callback with the expected `state` and a `code`
- **THEN** the library exchanges the code with the same redirect URI, shows a short "you can close this window" page, stops listening, and returns the token

#### Scenario: Listener scope
- **WHEN** the loopback login is waiting for the redirect
- **THEN** it accepts connections only on `127.0.0.1`, not on other interfaces

### Requirement: Loopback login state protection
Each loopback login SHALL generate an unpredictable `state` value. It SHALL reject a callback
whose `state` does not match, without exchanging its code.

#### Scenario: Mismatched state
- **WHEN** the callback arrives with a `state` different from the one in the authorize URL
- **THEN** the login fails with an error and no token request is sent

#### Scenario: SSO reports an error
- **WHEN** the callback arrives with an `error` parameter (for example, the user denied access)
- **THEN** the login fails with an error that includes that `error` value

### Requirement: Loopback login cancellation
The loopback login SHALL stop listening and return when the caller's context is cancelled or its
deadline passes. The same applies when the caller-supplied URL function returns an error.

#### Scenario: Login timeout
- **WHEN** the caller's context deadline passes before any callback arrives
- **THEN** the login returns an error matching `context.DeadlineExceeded` and the port is closed

#### Scenario: Browser cannot be opened
- **WHEN** the caller-supplied URL function returns an error
- **THEN** the login returns that error and the port is closed
