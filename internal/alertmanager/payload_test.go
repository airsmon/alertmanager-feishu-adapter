package alertmanager

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPayloadDecodesAlertmanagerV4Fixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/firing.json")
	if err != nil {
		t.Fatal(err)
	}

	var got Payload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if got.Version != "4" {
		t.Errorf("Version = %q, want 4", got.Version)
	}
	if got.Status != "firing" {
		t.Errorf("Status = %q, want firing", got.Status)
	}
	if got.CommonLabels.Get("namespace") != "monitoring" {
		t.Errorf("namespace = %q, want monitoring", got.CommonLabels.Get("namespace"))
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("len(Alerts) = %d, want 1", len(got.Alerts))
	}
	alert := got.Alerts[0]
	if alert.Labels.Get("pod") != "device-exporter-7d8f9c6b5-x2k4m" {
		t.Errorf("pod = %q", alert.Labels.Get("pod"))
	}
	wantStart := time.Date(2026, 7, 22, 5, 45, 32, 0, time.UTC)
	if !alert.StartsAt.Equal(wantStart) {
		t.Errorf("StartsAt = %s, want %s", alert.StartsAt, wantStart)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("fixture failed validation: %v", err)
	}
}

func TestNilKVGet(t *testing.T) {
	var kv KV
	if got := kv.Get("missing"); got != "" {
		t.Errorf("Get on nil KV = %q, want empty", got)
	}
}

func TestPayloadValidate(t *testing.T) {
	valid := Payload{
		Version: "4",
		Status:  "firing",
		Alerts: []Alert{{
			Status:   "firing",
			StartsAt: time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid payload: %v", err)
	}

	tests := []struct {
		name string
		edit func(*Payload)
		want string
	}{
		{
			name: "unsupported version",
			edit: func(payload *Payload) { payload.Version = "3" },
			want: "version",
		},
		{
			name: "invalid group status",
			edit: func(payload *Payload) { payload.Status = "pending" },
			want: "group status",
		},
		{
			name: "no alerts",
			edit: func(payload *Payload) { payload.Alerts = nil },
			want: "at least one",
		},
		{
			name: "too many alerts",
			edit: func(payload *Payload) {
				payload.Alerts = make([]Alert, 11)
				for i := range payload.Alerts {
					payload.Alerts[i] = valid.Alerts[0]
				}
			},
			want: "maximum is 10",
		},
		{
			name: "invalid alert status",
			edit: func(payload *Payload) { payload.Alerts[0].Status = "suppressed" },
			want: "alerts[0] has invalid status",
		},
		{
			name: "zero startsAt",
			edit: func(payload *Payload) { payload.Alerts[0].StartsAt = time.Time{} },
			want: "startsAt must not be zero",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := valid
			payload.Alerts = append([]Alert(nil), valid.Alerts...)
			test.edit(&payload)
			err := payload.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestPayloadValidateAllowsResolvedAlertWithoutEndsAt(t *testing.T) {
	payload := Payload{
		Version: "4",
		Status:  "resolved",
		Alerts: []Alert{{
			Status:   "resolved",
			StartsAt: time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		}},
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("resolved alert with zero EndsAt should remain compatible: %v", err)
	}
}

func TestPayloadDecodeAllowsUnknownFields(t *testing.T) {
	data := []byte(`{
		"version":"4",
		"status":"firing",
		"futureTopLevelField":{"enabled":true},
		"alerts":[{
			"status":"firing",
			"startsAt":"2026-07-22T01:02:03Z",
			"futureAlertField":"value"
		}]
	}`)
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unknown fields should be ignored: %v", err)
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("decoded payload failed validation: %v", err)
	}
}
