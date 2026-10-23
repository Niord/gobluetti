package gobluetti_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Niord/gobluetti"
)

type gatewayRequest struct {
	method, path, query string
	header              http.Header
	body                string
}

// fakeGateway records API requests and answers each with reply(request).
type fakeGateway struct {
	reply func(r gatewayRequest) (status int, body string)

	mu   sync.Mutex
	reqs []gatewayRequest
}

func newFakeGateway(t *testing.T, reply func(r gatewayRequest) (int, string)) (*fakeGateway, string) {
	t.Helper()
	g := &fakeGateway{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("fake gateway: read body: %v", err)
		}
		req := gatewayRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header, body: string(body)}
		g.mu.Lock()
		g.reqs = append(g.reqs, req)
		g.mu.Unlock()
		status, resp := g.reply(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp)) // the test reads the client's result, not this write
	}))
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func (g *fakeGateway) requests() []gatewayRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reqs
}

func ok(data string) (int, string) {
	return http.StatusOK, `{"msgId":"m","msgCode":0,"data":` + data + `}`
}

func newTestClient(gatewayURL string, auth *gobluetti.Auth, tok gobluetti.Token) *gobluetti.Client {
	c := gobluetti.NewClient(auth, &tok)
	c.GatewayURL = gatewayURL
	return c
}

func TestClientGatewayAndHeaders(t *testing.T) {
	gw, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return ok(`[]`) })
	tests := []struct {
		name       string
		gatewayURL string
		host       string
	}{
		{name: "gateway URL", gatewayURL: gwURL},
		{name: "token host wins", gatewayURL: "http://127.0.0.1:1", host: gwURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(gw.requests())
			c := newTestClient(tt.gatewayURL, nil, gobluetti.Token{AccessToken: "acc", Host: tt.host})
			if _, err := c.Devices(t.Context()); err != nil {
				t.Fatalf("Devices() error = %v", err)
			}
			reqs := gw.requests()
			if len(reqs) != before+1 {
				t.Fatalf("Devices() sent %d requests to the gateway, want 1", len(reqs)-before)
			}
			r := reqs[len(reqs)-1]
			if r.method != http.MethodGet || r.path != "/api/bluiotdata/ha/v2/devices" {
				t.Errorf("Devices() request = %s %s, want GET /api/bluiotdata/ha/v2/devices", r.method, r.path)
			}
			want := map[string]string{
				"Authorization": "acc",
				"X-Os":          "open",
				"X-App-Key":     "19974C15BCCBC91B21A8BA71BF5",
				"X-App-Ver":     "1.0.5",
			}
			for k, v := range want {
				if got := r.header.Get(k); got != v {
					t.Errorf("Devices() header %s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   gobluetti.APIError
	}{
		{name: "msgCode", status: 200, body: `{"msgId":"m1","msgCode":1002,"data":"secret-body"}`, want: gobluetti.APIError{MsgCode: 1002, MsgID: "m1"}},
		{
			name:   "msgCode with message",
			status: 200,
			body:   `{"msgId":null,"msgCode":403,"message":"设备不属于当前用户"}`,
			want:   gobluetti.APIError{MsgCode: 403, Message: "设备不属于当前用户"},
		},
		{name: "HTTP status", status: 502, body: `secret-body`, want: gobluetti.APIError{StatusCode: 502}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return tt.status, tt.body })
			_, err := newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"}).Devices(t.Context())
			apiErr, isAPI := errors.AsType[*gobluetti.APIError](err)
			if !isAPI || *apiErr != tt.want {
				t.Fatalf("Devices() error = %v, want %+v", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret-body") || strings.Contains(err.Error(), "acc") {
				t.Errorf("Devices() error %q contains the response body or token", err)
			}
		})
	}
}

func TestProactiveRefresh(t *testing.T) {
	sso, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"new","refresh_token":"ref2","expires_in":2592000}`)
	gw, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return ok(`[]`) })
	c := newTestClient(gwURL, auth, gobluetti.Token{AccessToken: "old", RefreshToken: "ref1", Expiry: time.Now().Add(72 * time.Hour)})
	var saved []*gobluetti.Token
	c.OnToken = func(tok *gobluetti.Token) { saved = append(saved, tok) }

	for range 2 {
		if _, err := c.Devices(t.Context()); err != nil {
			t.Fatalf("Devices() error = %v", err)
		}
	}
	if n := len(sso.requests()); n != 1 {
		t.Errorf("two calls with a token expiring in 3 days sent %d refreshes, want 1", n)
	}
	for _, r := range gw.requests() {
		if got := r.header.Get("Authorization"); got != "new" {
			t.Errorf("gateway got Authorization %q, want the refreshed token", got)
		}
	}
	if len(saved) != 1 || saved[0].AccessToken != "new" || saved[0].RefreshToken != "ref2" {
		t.Errorf("OnToken calls = %+v, want one call with the new token", saved)
	}
	if got := c.Token(); got.AccessToken != "new" {
		t.Errorf("Token() = %+v, want the new token", got)
	}
}

func TestProactiveRefreshFailureTolerated(t *testing.T) {
	_, auth := newFakeSSO(t, http.StatusInternalServerError, `oops`)
	gw, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return ok(`[]`) })
	c := newTestClient(gwURL, auth, gobluetti.Token{AccessToken: "old", RefreshToken: "ref", Expiry: time.Now().Add(72 * time.Hour)})
	if _, err := c.Devices(t.Context()); err != nil {
		t.Fatalf("Devices() with failing proactive refresh error = %v, want nil", err)
	}
	if reqs := gw.requests(); len(reqs) != 1 || reqs[0].header.Get("Authorization") != "old" {
		t.Errorf("gateway requests = %d, want one with the old token", len(reqs))
	}
}

// expiredFor answers 805 to requests carrying the given access token.
func expiredFor(token string) func(gatewayRequest) (int, string) {
	return func(r gatewayRequest) (int, string) {
		if r.header.Get("Authorization") == token {
			return http.StatusOK, `{"msgId":"m","msgCode":805}`
		}
		return ok(`[]`)
	}
}

func TestExpiredTokenRenewed(t *testing.T) {
	_, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"new","expires_in":3600}`)
	gw, gwURL := newFakeGateway(t, expiredFor("old"))
	c := newTestClient(gwURL, auth, gobluetti.Token{AccessToken: "old", RefreshToken: "ref"})
	var saved int
	c.OnToken = func(*gobluetti.Token) { saved++ }
	if _, err := c.Devices(t.Context()); err != nil {
		t.Fatalf("Devices() after 805 error = %v", err)
	}
	if n := len(gw.requests()); n != 2 {
		t.Errorf("Devices() after 805 sent %d gateway requests, want 2", n)
	}
	if saved != 1 {
		t.Errorf("OnToken called %d times, want 1", saved)
	}
}

func TestLoginRequired(t *testing.T) {
	tests := []struct {
		name      string
		ssoStatus int
		ssoBody   string
		wantOAuth bool
	}{
		{name: "refresh rejected", ssoStatus: http.StatusBadRequest, ssoBody: `{"error":"invalid_grant"}`, wantOAuth: true},
		{name: "805 again after refresh", ssoStatus: http.StatusOK, ssoBody: `{"access_token":"new"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, auth := newFakeSSO(t, tt.ssoStatus, tt.ssoBody)
			_, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return 200, `{"msgId":"m","msgCode":805}` })
			c := newTestClient(gwURL, auth, gobluetti.Token{AccessToken: "acc-secret", RefreshToken: "ref-secret"})
			_, err := c.Devices(t.Context())
			if !errors.Is(err, gobluetti.ErrLoginRequired) {
				t.Fatalf("Devices() error = %v, want ErrLoginRequired", err)
			}
			if _, isOAuth := errors.AsType[*gobluetti.OAuthError](err); isOAuth != tt.wantOAuth {
				t.Errorf("Devices() error = %v, *OAuthError in chain = %t, want %t", err, isOAuth, tt.wantOAuth)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("Devices() error %q contains a token", err)
			}
		})
	}
}

func TestConcurrentRenewal(t *testing.T) {
	sso, auth := newFakeSSO(t, http.StatusOK, `{"access_token":"new","expires_in":3600}`)
	_, gwURL := newFakeGateway(t, expiredFor("old"))
	c := newTestClient(gwURL, auth, gobluetti.Token{AccessToken: "old", RefreshToken: "ref"})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := c.Devices(t.Context()); err != nil {
				t.Errorf("concurrent Devices() error = %v", err)
			}
		})
	}
	wg.Wait()
	if n := len(sso.requests()); n != 1 {
		t.Errorf("4 concurrent calls hitting 805 sent %d refreshes, want 1", n)
	}
}

const devicesJSON = `[
 {"sn":"SN1","name":"Garage","model":"AC300","online":"1","isBindByCurUser":"1","supportNetwork":"1","stateList":[
   {"fnCode":"SOC","fnName":"Battery","fnValue":"87","fnType":"SENSOR","sensorInfo":{"sensorType":"SensorDeviceClass.BATTERY","unit":"%"}},
   {"fnCode":"PVAllTotalPower","fnName":"PV","fnValue":120,"fnType":"SENSOR"},
   {"fnCode":"SetCtrlWorkMode","fnName":"Mode","fnValue":"1","fnType":"SELECT","supportModeValues":[{"code":"1","name":"Backup"},{"code":2,"name":"Self-consumption"}]}
 ]},
 {"sn":"SN2","name":"Cabin","model":"AP300","online":"0","isBindByCurUser":"0","supportNetwork":"1","stateList":[]}
]`

func TestDevices(t *testing.T) {
	_, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return ok(devicesJSON) })
	got, err := newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"}).Devices(t.Context())
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	want := []gobluetti.Device{
		{
			SN: "SN1", Name: "Garage", Model: "AC300", Online: true, Bound: true, NetworkSupported: true,
			States: []gobluetti.State{
				{Code: gobluetti.FnSOC, Name: "Battery", Value: "87", Type: gobluetti.StateSensor, SensorType: "SensorDeviceClass.BATTERY", Unit: "%"},
				{Code: gobluetti.FnPVPower, Name: "PV", Value: "120", Type: gobluetti.StateSensor}, // numeric fnValue
				{Code: gobluetti.FnWorkMode, Name: "Mode", Value: "1", Type: gobluetti.StateSelect, Options: []gobluetti.Option{
					{Code: "1", Name: "Backup"}, {Code: "2", Name: "Self-consumption"}, // numeric option code
				}},
			},
		},
		{SN: "SN2", Name: "Cabin", Model: "AP300", NetworkSupported: true, States: []gobluetti.State{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Devices() =\n%+v\nwant\n%+v", got, want)
	}
	if soc, found := got[0].State(gobluetti.FnSOC); !found || soc.Value != "87" {
		t.Errorf("State(SOC) = %+v, %t, want value 87", soc, found)
	}
	if _, found := got[0].State("NoSuchCode"); found {
		t.Error("State(NoSuchCode) found = true, want false")
	}
}

func TestDevice(t *testing.T) {
	_, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) { return ok(devicesJSON) })
	c := newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"})
	dev, err := c.Device(t.Context(), "SN2")
	if err != nil || dev.SN != "SN2" || dev.Model != "AP300" {
		t.Errorf("Device(SN2) = %+v, %v, want the AP300", dev, err)
	}
	if _, err := c.Device(t.Context(), "SN9"); !errors.Is(err, gobluetti.ErrDeviceNotFound) {
		t.Errorf("Device(SN9) error = %v, want ErrDeviceNotFound", err)
	}
}

func TestDefaultUnits(t *testing.T) {
	_, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) {
		return ok(`[{"sn":"SN1","stateList":[
			{"fnCode":"PVAllTotalPower","fnValue":"0","fnType":"SENSOR","sensorInfo":{"sensorType":"SensorDeviceClass.POWER","unit":null}},
			{"fnCode":"DsgFullTime","fnValue":"5","fnType":"SENSOR","sensorInfo":{"sensorType":"SensorDeviceClass.DURATION","unit":null}},
			{"fnCode":"SetCtrlAc","fnValue":"1","fnType":"SWITCH"}]}]`)
	})
	dev, err := newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"}).Device(t.Context(), "SN1")
	if err != nil {
		t.Fatalf("Device(SN1) error = %v", err)
	}
	for code, want := range map[string]string{gobluetti.FnPVPower: "W", gobluetti.FnDischargeTime: "min", gobluetti.FnACOutput: ""} {
		if s, _ := dev.State(code); s.Unit != want {
			t.Errorf("State(%s).Unit = %q, want %q", code, s.Unit, want)
		}
	}
}

func TestSetAndBind(t *testing.T) {
	tests := []struct {
		name     string
		call     func(c *gobluetti.Client) error
		msgCode  int
		wantPath string
		wantBody any
		wantErr  bool
	}{
		{
			name:     "set accepted",
			call:     func(c *gobluetti.Client) error { return c.Set(t.Context(), "SN1", gobluetti.FnACOutput, "0") },
			wantPath: "/api/bluiotdata/ha/v1/fulfillment",
			wantBody: map[string]any{"sn": "SN1", "fnCode": "SetCtrlAc", "fnValue": "0"},
		},
		{
			name:     "set rejected",
			call:     func(c *gobluetti.Client) error { return c.Set(t.Context(), "SN1", gobluetti.FnACOutput, "0") },
			msgCode:  1001,
			wantPath: "/api/bluiotdata/ha/v1/fulfillment",
			wantBody: map[string]any{"sn": "SN1", "fnCode": "SetCtrlAc", "fnValue": "0"},
			wantErr:  true,
		},
		{
			name:     "bind",
			call:     func(c *gobluetti.Client) error { return c.Bind(t.Context(), "SN1") },
			wantPath: "/api/bluiotdata/ha/v1/bindDevices",
			wantBody: map[string]any{"bindSnList": []any{"SN1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, gwURL := newFakeGateway(t, func(gatewayRequest) (int, string) {
				return http.StatusOK, `{"msgId":"m","msgCode":` + strconv.Itoa(tt.msgCode) + `}`
			})
			err := tt.call(newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"}))
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("%s error = %v, want error = %t", tt.name, err, tt.wantErr)
			}
			reqs := gw.requests()
			if len(reqs) != 1 {
				t.Fatalf("%s sent %d requests, want exactly 1", tt.name, len(reqs))
			}
			r := reqs[0]
			if r.method != http.MethodPost || r.path != tt.wantPath || r.header.Get("Content-Type") != "application/json" {
				t.Errorf("%s request = %s %s (%s), want POST %s JSON", tt.name, r.method, r.path, r.header.Get("Content-Type"), tt.wantPath)
			}
			var got any
			if err := json.Unmarshal([]byte(r.body), &got); err != nil {
				t.Fatalf("%s body %q: %v", tt.name, r.body, err)
			}
			gotJSON, _ := json.Marshal(got)          // a decoded JSON value always marshals
			wantJSON, _ := json.Marshal(tt.wantBody) // same
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("%s body = %s, want %s", tt.name, gotJSON, wantJSON)
			}
		})
	}
}

func TestDeviceStates(t *testing.T) {
	const statesJSON = `[{"sn":"SN1","name":null,"model":null,"online":"1","isBindByCurUser":"1","supportNetwork":"1",
		"stateList":[{"fnCode":"SOC","fnName":"Battery","fnValue":"100","fnType":"SENSOR"}]},
		{"sn":"SN2","online":"0","isBindByCurUser":"1","supportNetwork":"1","stateList":[]}]`
	tests := []struct {
		name      string
		sns       []string
		reply     func(gatewayRequest) (int, string)
		wantQuery string // "" means no request
		want      []gobluetti.Device
		wantCode  int // msgCode of the expected *APIError
		wantErr   bool
	}{
		{
			name:      "two devices",
			sns:       []string{"SN1", "SN2"},
			reply:     func(gatewayRequest) (int, string) { return ok(statesJSON) },
			wantQuery: "sns=SN1%2CSN2",
			want: []gobluetti.Device{
				{SN: "SN1", Online: true, Bound: true, NetworkSupported: true, States: []gobluetti.State{
					{Code: gobluetti.FnSOC, Name: "Battery", Value: "100", Type: gobluetti.StateSensor},
				}},
				{SN: "SN2", Bound: true, NetworkSupported: true, States: []gobluetti.State{}},
			},
		},
		{
			name: "unknown serial number",
			sns:  []string{"SN1", "SN9"},
			reply: func(gatewayRequest) (int, string) {
				return http.StatusOK, `{"msgId":"m","msgCode":1,"message":"Wrong parameter: device does not exist"}`
			},
			wantQuery: "sns=SN1%2CSN9",
			wantCode:  1,
			wantErr:   true,
		},
		{name: "no serial numbers", wantErr: true},
		{name: "empty serial number", sns: []string{"SN1", ""}, wantErr: true},
		{name: "serial number with a comma", sns: []string{"SN1,SN2"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, gwURL := newFakeGateway(t, func(r gatewayRequest) (int, string) { return tt.reply(r) })
			got, err := newTestClient(gwURL, nil, gobluetti.Token{AccessToken: "acc"}).DeviceStates(t.Context(), tt.sns...)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("DeviceStates(%q) error = %v, want error %t", tt.sns, err, tt.wantErr)
			}
			if apiErr, isAPI := errors.AsType[*gobluetti.APIError](err); tt.wantCode != 0 && (!isAPI || apiErr.MsgCode != tt.wantCode) {
				t.Errorf("DeviceStates(%q) error = %v, want *APIError with msgCode %d", tt.sns, err, tt.wantCode)
			}
			reqs := g.requests()
			switch {
			case tt.wantQuery == "" && len(reqs) != 0:
				t.Errorf("DeviceStates(%q) sent %d requests, want none", tt.sns, len(reqs))
			case tt.wantQuery != "" && (len(reqs) != 1 || reqs[0].method != http.MethodGet ||
				reqs[0].path != "/api/bluiotdata/ha/v1/deviceStates" || reqs[0].query != tt.wantQuery):
				t.Errorf("DeviceStates(%q) requests = %+v, want one GET /api/bluiotdata/ha/v1/deviceStates?%s", tt.sns, reqs, tt.wantQuery)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DeviceStates(%q) =\n%+v\nwant\n%+v", tt.sns, got, tt.want)
			}
		})
	}
}
