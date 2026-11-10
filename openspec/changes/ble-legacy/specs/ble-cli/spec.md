# Spec Delta

## Purpose

Gives people a command-line tool, `gobluetti-ble`, to find legacy BLUETTI stations nearby, read
their state and battery packs, and switch their AC and DC outputs over BLE.

## ADDED Requirements

### Requirement: Commands
The tool SHALL accept exactly one of these commands:
`scan`, `state <name>`, `pack <name> <n>`, `set <name> ac|dc on|off`, `set <name> power off`,
and `set <name> sleep 30s|1m|5m|never`. `<name>` is the station's advertised BLE name. An unknown
command, a wrong number of arguments, a pack number that is not an integer, or a `set` argument
outside those forms SHALL print the usage to stderr and exit with a non-zero status without
using Bluetooth.

#### Scenario: Invalid set argument
- **WHEN** the user runs `gobluetti-ble set AC200M1 ac maybe`
- **THEN** the usage is printed to stderr, the exit status is non-zero, and no BLE scan starts

### Requirement: Time limit
Every command SHALL stop after the `-timeout` duration, 30 seconds by default, and on Ctrl-C.
`scan` SHALL report what it found when the time is up, and exit with status 0. Other commands
that run out of time SHALL exit with a non-zero status.

#### Scenario: Scan finishes
- **WHEN** the user runs `gobluetti-ble -timeout 10s scan` and one station advertises
- **THEN** after 10 seconds the tool prints one line with that station's name, model, and serial, and exits with 0

### Requirement: Output
`state` SHALL print one `field: value` line per state field, with units (W, %). `pack` SHALL
print the pack number, voltage, percent, and the cell voltages. `set` SHALL print one
confirmation line naming the station, the output, and the new value. Errors SHALL be printed to
stderr, with a non-zero exit status.

#### Scenario: Successful set
- **WHEN** `gobluetti-ble set AC200M1 dc on` succeeds
- **THEN** the tool prints a line confirming the DC output of `AC200M1` is on and exits with 0

### Requirement: One command per run
`set` SHALL send exactly one command and nothing else that changes the station. `power off`
shuts the station down. `state`
and `pack` SHALL NOT switch any output.

#### Scenario: Pack read does not switch outputs
- **WHEN** the user runs `gobluetti-ble pack AC200M1 2`
- **THEN** the only register written is the pack selector, 3006
