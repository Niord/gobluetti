# Spec Delta

## Purpose

Gives users a command-line way to list, read, command, and watch their BLUETTI devices with a
token file. It doubles as the tool for checking the library against a real account.

## ADDED Requirements

### Requirement: Token file
`gobluetti` SHALL read the token from the file given by `-token`. The default is
`~/.config/gobluetti/token.json`, the same format `gobluetti-login` writes. When the client
renews the token, the tool SHALL replace that file atomically, with permission `0600`.

#### Scenario: Missing token file
- **WHEN** the token file does not exist
- **THEN** the tool exits with a non-zero status and suggests running `gobluetti-login`

#### Scenario: Token renewed
- **WHEN** a command triggers a token refresh
- **THEN** the token file holds the new token afterwards, with permission `0600`

### Requirement: List devices
`gobluetti devices` SHALL print one line per device: the serial number, model, name,
`online`/`offline`, and `bound`/`unbound`.

#### Scenario: Two devices
- **WHEN** the account has an online AC300 and an offline AP300
- **THEN** the output has two lines with their serial numbers, models, names, and `online` and `offline`

### Requirement: Show one device
`gobluetti device <sn>` SHALL print one line per state: the function code, type, value, the unit
when known, and for `SELECT` the allowed options as `code=name`.

#### Scenario: Show states
- **WHEN** device `SN1` has `SOC` `"87"` with unit `%` and `SetCtrlWorkMode` `"1"` with options `1=Backup`, `2=Self-consumption`
- **THEN** the output contains a `SOC` line with `87 %` and a `SetCtrlWorkMode` line listing `1=Backup 2=Self-consumption`

### Requirement: Send a command
`gobluetti set <sn> <code> <value>` SHALL send exactly one command and print the result. It SHALL
NOT ask for confirmation, because running the subcommand is the confirmation.

#### Scenario: Command accepted
- **WHEN** the user runs `gobluetti set SN1 SetACECO 1` and the cloud answers msgCode 0
- **THEN** the tool prints that the command was accepted and exits with status 0

#### Scenario: Command rejected
- **WHEN** the cloud answers a non-zero msgCode
- **THEN** the tool prints the msgCode and exits with a non-zero status

### Requirement: Bind devices
`gobluetti bind <sn>...` SHALL bind one or more devices for cloud control and print them. Without
a serial number it SHALL print the usage and exit with a non-zero status.

#### Scenario: Bind two devices
- **WHEN** the user runs `gobluetti bind SN1 SN2` and the cloud answers msgCode 0
- **THEN** the tool prints `bound: SN1 SN2` and exits with status 0

### Requirement: Watch devices
`gobluetti watch` SHALL run until interrupted (Ctrl-C). It prints a timestamped line with the
serial number for every change notification.

#### Scenario: Change arrives
- **WHEN** the cloud notifies that `SN1` changed
- **THEN** the tool prints a line with the current time and `SN1`

### Requirement: Login required
When a command fails with the library's "log in again" error, the tool SHALL say so and suggest
running `gobluetti-login`. It exits with a non-zero status.

#### Scenario: Session ended
- **WHEN** the refresh token is rejected during `gobluetti devices`
- **THEN** the tool prints a message to run `gobluetti-login` and exits with a non-zero status
