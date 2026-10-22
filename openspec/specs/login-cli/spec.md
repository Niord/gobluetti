# login-cli Specification

## Purpose

Gives users a ready-made way to obtain and refresh a BLUETTI cloud token file on a machine with a
browser, so headless consumers of the library can use a token without their own login UI.

## Requirements

### Requirement: Interactive login
Run without a refresh flag, `gobluetti-login` SHALL perform the loopback browser login. It prints
the authorize URL to stderr, tries to open it in the system browser, and waits up to 5 minutes or
until interrupted (Ctrl-C).

#### Scenario: Browser opens
- **WHEN** the user runs `gobluetti-login` and completes the login in the browser
- **THEN** the tool writes the token and exits with status 0

#### Scenario: No browser available
- **WHEN** the system browser cannot be opened
- **THEN** the tool still prints the authorize URL to stderr and keeps waiting for the callback

#### Scenario: Login not completed
- **WHEN** 5 minutes pass, or the user presses Ctrl-C, before the callback arrives
- **THEN** the tool exits with a non-zero status and writes no token

### Requirement: Token output
The tool SHALL write the token as JSON in the library's token format. Without `-o` it writes to
stdout. With `-o <path>` it writes to that file with permission `0600`, replacing an existing
file. Only the token goes to stdout; prompts and diagnostics go to stderr.

#### Scenario: Output to stdout
- **WHEN** the login succeeds and `-o` is not given
- **THEN** stdout contains exactly one JSON token object and nothing else

#### Scenario: Output to a file
- **WHEN** the login succeeds with `-o token.json`
- **THEN** `token.json` contains the JSON token, its permission is `0600`, and stdout is empty

### Requirement: Token summary
After writing a token, the tool SHALL print a one-line summary to stderr with the expiry, the
host, and whether a refresh token is present. The summary SHALL NOT contain the token values.

#### Scenario: Summary after login
- **WHEN** a token with expiry, host, and refresh token is written
- **THEN** stderr shows the expiry time, the host, and "refresh token: yes", with no access or refresh token value

### Requirement: Refresh a stored token
With `-refresh <path>`, the tool SHALL read the JSON token from that file and refresh it without a
browser. It writes the result back to the same file with permission `0600`, or to `-o <path>` if
that is given. On failure it SHALL leave the source file unchanged.

#### Scenario: Successful refresh
- **WHEN** the user runs `gobluetti-login -refresh token.json` and SSO accepts the refresh token
- **THEN** `token.json` holds the refreshed token and the tool exits with status 0

#### Scenario: Refresh rejected
- **WHEN** SSO rejects the refresh token
- **THEN** the tool prints the OAuth error code to stderr, exits with a non-zero status, and `token.json` is unchanged
