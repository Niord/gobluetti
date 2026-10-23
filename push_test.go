package gobluetti

import (
	"encoding/json"
	"maps"
	"os"
	"testing"
	"time"
)

// readPush returns a recorded push body from testdata/push. The bodies are
// captures from an AP300 with identifiers and readings replaced by
// made-up values.
func readPush(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/push/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPushFixtures guards the fixtures themselves: trimming one would weaken
// every test that reads it.
func TestPushFixtures(t *testing.T) {
	for _, name := range []string{"realtime-main", "realtime-pack"} {
		var doc struct {
			Data struct {
				Message struct {
					MessageType string         `json:"messageType"`
					Payload     map[string]any `json:"payload"`
				} `json:"message"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(readPush(t, name)), &doc); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		if m := doc.Data.Message; m.MessageType != MessageRealtimeStatus || len(m.Payload) != 79 {
			t.Errorf("fixture %s: messageType %q, %d payload keys, want %s and 79", name, m.MessageType, len(m.Payload), MessageRealtimeStatus)
		}
	}
}

func TestParseUpdate(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		want   Update
		wantOK bool
	}{
		{
			name:   "serial number only",
			body:   `{"data":{"message":{"deviceSn":"SN1"}}}`,
			want:   Update{SN: "SN1"},
			wantOK: true,
		},
		{
			name: "main realtime push",
			body: readPush(t, "realtime-main"),
			want: Update{
				SN:   "SN1",
				Type: MessageRealtimeStatus,
				Time: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
				Values: map[string]string{
					FnSOC: "80", FnGridPower: "500", FnACLoadPower: "480", FnPVPower: "0",
					FnDCLoadPower: "0", FnACOutput: "1", FnDischargeTime: "600",
				},
				Fields: map[string]string{
					"powerPvIn": "0", "powerGridIn": "500", "powerAcOut": "480", "powerDcOut": "0",
					"batterySoc": "80", "acSwitch": "1", "pvSwitch": "0", "gridSwitch": "0",
					"ac_line": "1", "pv_line": "0", "grid_line": "0", "feedback_line": "0", "pv_to_grid_line": "0",
					"ac_icon": "1", "pv_icon": "0", "grid_icon": "0", "dsgEmptyTime": "600", "chgFullTime": "900",
					"pvTotalEnergy": "0.0", "dcTotalEnergy": "0.0", "packChargingStatus": "0", "powerInvTotal": "-500",
				},
			},
			wantOK: true,
		},
		{
			name: "battery pack push carries no function codes",
			body: readPush(t, "realtime-pack"),
			want: Update{
				SN:   "SN1",
				Type: MessageRealtimeStatus,
				Time: time.Date(2026, 1, 15, 12, 0, 3, 0, time.UTC),
				Fields: map[string]string{
					"packAverageTemp": "25", "batteryVoltage": "520.5", "packTotalChgEnergy": "1000.0",
					"packTotalDsgEnergy": "900.0", "batterySoh": "98",
				},
			},
			wantOK: true,
		},
		{
			name: "numbers and booleans as text, objects and arrays skipped",
			body: `{"data":{"message":{"deviceSn":"SN1","messageType":"DEVICE_REALTIME_STATUS","payload":
				{"batterySoc":62,"acSwitch":true,"powerAcOut":"","nested":{"a":1},"list":[1],"none":null}}}}`,
			want: Update{
				SN: "SN1", Type: MessageRealtimeStatus,
				Values: map[string]string{FnSOC: "62", FnACOutput: "true", FnACLoadPower: ""},
				Fields: map[string]string{"batterySoc": "62", "acSwitch": "true", "powerAcOut": ""},
			},
			wantOK: true,
		},
		{
			name:   "unknown message type has fields but no values",
			body:   `{"data":{"message":{"deviceSn":"SN1","messageType":"SOMETHING_ELSE","payload":{"batterySoc":"50"}}}}`,
			want:   Update{SN: "SN1", Type: "SOMETHING_ELSE", Fields: map[string]string{"batterySoc": "50"}},
			wantOK: true,
		},
		{
			name:   "unparseable timestamp gives zero time",
			body:   `{"data":{"message":{"deviceSn":"SN1","payload":{"timestamp":"yesterday"}}}}`,
			want:   Update{SN: "SN1"},
			wantOK: true,
		},
		{
			name:   "payload is a string",
			body:   `{"data":{"message":{"deviceSn":"SN1","messageType":"DEVICE_REALTIME_STATUS","payload":"oops"}}}`,
			want:   Update{SN: "SN1", Type: MessageRealtimeStatus},
			wantOK: true,
		},
		{
			name:   "payload is an array",
			body:   `{"data":{"message":{"deviceSn":"SN1","payload":[1,2]}}}`,
			want:   Update{SN: "SN1"},
			wantOK: true,
		},
		{
			name:   "payload is null",
			body:   `{"data":{"message":{"deviceSn":"SN1","payload":null}}}`,
			want:   Update{SN: "SN1"},
			wantOK: true,
		},
		{
			name:   "message type is not a string",
			body:   `{"data":{"message":{"deviceSn":"SN1","messageType":{"x":1}}}}`,
			want:   Update{SN: "SN1"},
			wantOK: true,
		},
		{name: "not json", body: `not json`},
		{name: "empty", body: ``},
		{name: "data is null", body: `{"data":null}`},
		{name: "data is a string", body: `{"data":"a string"}`},
		{name: "message is a string", body: `{"data":{"message":"a string"}}`},
		{name: "old flat shape", body: `{"data":{"deviceSn":"SN1"}}`},
		{name: "serial number is a number", body: `{"data":{"message":{"deviceSn":42}}}`},
		{name: "empty serial number", body: `{"data":{"message":{"deviceSn":""}}}`},
		{name: "top level is an array", body: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseUpdate(tt.body)
			if ok != tt.wantOK || got.SN != tt.want.SN || got.Type != tt.want.Type || !got.Time.Equal(tt.want.Time) ||
				!maps.Equal(got.Values, tt.want.Values) || !maps.Equal(got.Fields, tt.want.Fields) {
				t.Errorf("parseUpdate(%.80q) =\n%+v, %t\nwant\n%+v, %t", tt.body, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func FuzzParseUpdate(f *testing.F) {
	for _, body := range []string{
		`{"data":{"message":{"deviceSn":"SN1"}}}`,
		`{"data":{"message":{"deviceSn":"SN1","messageType":"DEVICE_REALTIME_STATUS","payload":{"batterySoc":62,"x":null}}}}`,
		`{"data":{"message":"a string"}}`,
		`not json`,
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		u, ok := parseUpdate(body)
		if ok != (u.SN != "") {
			t.Errorf("parseUpdate(%q) = %+v, %t: an update must have a serial number, and only an update", body, u, ok)
		}
		if u.Type != MessageRealtimeStatus && len(u.Values) > 0 {
			t.Errorf("parseUpdate(%q) values %v for type %q, want none", body, u.Values, u.Type)
		}
	})
}
