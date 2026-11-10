// Package ble reads and switches legacy BLUETTI power stations over
// Bluetooth Low Energy, without the BLUETTI cloud.
//
// Legacy stations speak plain Modbus RTU over two GATT characteristics: frames
// are written to ff02 and the answers arrive as notifications on ff01, with no
// encryption. The package supports the AC200M family, which includes the
// AC200MAX; such a station advertises the name "AC200M" followed by its
// serial number. Newer models encrypt their BLE link and are not supported.
//
// The protocol and the register map are ported from bluetti_mqtt
// (https://github.com/warhammerkid/bluetti_mqtt, MIT, commit ffe9b96).
//
// Typical use:
//
//	stations, err := ble.Scan(ctx) // ctx bounds how long to listen
//	conn, err := ble.Connect(ctx, stations[0].Name)
//	defer conn.Close()
//	state, err := conn.State(ctx)
//
// SetACOutput and SetDCOutput switch real outputs: whatever is plugged in
// loses or gains power.
package ble
