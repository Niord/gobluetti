package gobluetti

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

// Function codes that the BLUETTI Home Assistant integration knows. A device
// reports only the codes it supports, and may report others.
const (
	FnSOC            = "SOC"                   // battery state of charge, %
	FnInverterState  = "InvWorkState"          // inverter status, read-only
	FnChargeFullTime = "ChgFullTime"           // time to full charge, minutes
	FnDischargeTime  = "DsgFullTime"           // battery time left, minutes; seen on AP300
	FnACLoadPower    = "ACLoadAllTotalPower"   // AC output power, W
	FnDCLoadPower    = "DCLoadAllTotalPower"   // DC output power, W
	FnPVPower        = "PVAllTotalPower"       // PV input power, W
	FnGridPower      = "GridAllTotalPower"     // grid input power, W
	FnWorkMode       = "SetCtrlWorkMode"       // work mode (select)
	FnACECO          = "SetACECO"              // AC ECO (select)
	FnDCECO          = "SetDCECO"              // DC ECO (select)
	FnACOutput       = "SetCtrlAc"             // AC output switch, "1" on, "0" off
	FnDCOutput       = "SetCtrlDc"             // DC output switch
	FnPower          = "SetCtrlPowerOn"        // main power switch
	FnSleep          = "SetCtrlPowerOn-2"      // sleep mode switch on some models
	FnSleepMode      = "SetSleepMode"          // sleep mode switch, seen on AP300
	FnStormMode      = "Storm_Mode_Cloud_Ctrl" // storm mode switch
)

// StateType tells how a state is used.
type StateType string

// State types reported by the cloud.
const (
	StateSensor StateType = "SENSOR" // a reading
	StateSwitch StateType = "SWITCH" // "1" on, "0" off
	StateSelect StateType = "SELECT" // one of State.Options
)

// Device is a power station as the cloud describes it.
type Device struct {
	SN    string
	Name  string
	Model string

	Online           bool
	Bound            bool // bound to the current user; false after it was unbound elsewhere
	NetworkSupported bool

	States []State
}

// State is one function of a device: a reading, a switch, or a selectable mode.
type State struct {
	Code  string // function code, for example [FnSOC]
	Name  string // human-readable name from the cloud
	Value string // the value as the cloud sent it, for example "87" or "1"
	Type  StateType

	Options []Option // the allowed values of a [StateSelect]

	SensorType string // the cloud's sensor class, for example "SensorDeviceClass.POWER"; may be empty
	Unit       string // for example "W" or "%"; the sensor class default when the cloud sends none
}

// Option is an allowed value of a selectable state.
type Option struct {
	Code string // the value to send
	Name string
}

// State returns the state with the given function code.
func (d *Device) State(code string) (State, bool) {
	i := slices.IndexFunc(d.States, func(s State) bool { return s.Code == code })
	if i < 0 {
		return State{}, false
	}
	return d.States[i], true
}

// Devices lists the account's devices with their current states.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var list []wireDevice
	if err := c.call(ctx, http.MethodGet, "/api/bluiotdata/ha/v2/devices", nil, nil, &list); err != nil {
		return nil, err
	}
	devs := make([]Device, len(list))
	for i, w := range list {
		devs[i] = w.device()
	}
	return devs, nil
}

// Device reads the current state of the device with serial number sn. It
// returns an error matching [ErrDeviceNotFound] when the account has no such device.
//
// It reads the device list, not the upstream deviceStates endpoint: for an
// AP300 that endpoint returned an empty, offline, unbound placeholder, and for
// other devices it omits options and sensor info.
func (c *Client) Device(ctx context.Context, sn string) (*Device, error) {
	devs, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(devs, func(d Device) bool { return d.SN == sn })
	if i < 0 {
		return nil, fmt.Errorf("gobluetti: device %q: %w", sn, ErrDeviceNotFound)
	}
	return &devs[i], nil
}

// Set sends one command: it sets the function code of device sn to value. It
// switches real outputs, so it is never retried, except once after the token
// was renewed. The value is not checked here; the cloud accepts or rejects it.
func (c *Client) Set(ctx context.Context, sn, code, value string) error {
	return c.call(ctx, http.MethodPost, "/api/bluiotdata/ha/v1/fulfillment", nil,
		map[string]string{"sn": sn, "fnCode": code, "fnValue": value}, nil)
}

// Bind binds the devices for cloud control by this client, as the official
// integration does once during setup. Until a device is bound, commands to it
// fail with an [*APIError] with MsgCode 403. It changes the account, not the device.
func (c *Client) Bind(ctx context.Context, sns ...string) error {
	return c.call(ctx, http.MethodPost, "/api/bluiotdata/ha/v1/bindDevices", nil,
		map[string][]string{"bindSnList": append([]string{}, sns...)}, nil) // [] rather than null
}

// defaultUnits are the units upstream's sensor.py uses when sensorInfo.unit is empty.
var defaultUnits = map[string]string{
	"SensorDeviceClass.BATTERY":  "%",
	"SensorDeviceClass.POWER":    "W",
	"SensorDeviceClass.DURATION": "min",
}

// wireDevice is a device as the gateway encodes it.
type wireDevice struct {
	SN        flexString `json:"sn"`
	Name      flexString `json:"name"`
	Model     flexString `json:"model"`
	Online    flexString `json:"online"`
	Bound     flexString `json:"isBindByCurUser"`
	Network   flexString `json:"supportNetwork"`
	StateList []struct {
		Code    flexString `json:"fnCode"`
		Name    flexString `json:"fnName"`
		Value   flexString `json:"fnValue"`
		Type    flexString `json:"fnType"`
		Options []struct {
			Code flexString `json:"code"`
			Name flexString `json:"name"`
		} `json:"supportModeValues"`
		Sensor struct {
			Type flexString `json:"sensorType"`
			Unit flexString `json:"unit"`
		} `json:"sensorInfo"`
	} `json:"stateList"`
}

func (w wireDevice) device() Device {
	dev := Device{
		SN:               string(w.SN),
		Name:             string(w.Name),
		Model:            string(w.Model),
		Online:           w.Online == "1",
		Bound:            w.Bound != "0", // upstream treats only "0" as unbound
		NetworkSupported: w.Network == "1",
		States:           make([]State, len(w.StateList)),
	}
	for i, s := range w.StateList {
		dev.States[i] = State{
			Code:       string(s.Code),
			Name:       string(s.Name),
			Value:      string(s.Value),
			Type:       StateType(s.Type),
			SensorType: string(s.Sensor.Type),
			Unit:       cmp.Or(string(s.Sensor.Unit), defaultUnits[string(s.Sensor.Type)]),
		}
		for _, o := range s.Options {
			dev.States[i].Options = append(dev.States[i].Options, Option{Code: string(o.Code), Name: string(o.Name)})
		}
	}
	return dev
}

// flexString decodes a JSON string, number, or boolean as its text: the
// cloud's value types are not documented, and upstream compares them as strings.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var v string
		err := json.Unmarshal(b, &v)
		*s = flexString(v)
		return err
	}
	if string(b) != "null" {
		*s = flexString(b)
	}
	return nil
}
