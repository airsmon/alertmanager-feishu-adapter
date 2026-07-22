// Package alertmanager contains the Alertmanager generic webhook v4 data model.
package alertmanager

import (
	"fmt"
	"time"
)

// KV is the label/annotation representation used by Alertmanager webhooks.
type KV map[string]string

// Get returns the value associated with key. It is safe on a nil map.
func (kv KV) Get(key string) string {
	return kv[key]
}

// Payload is the JSON body sent by an Alertmanager generic webhook receiver.
type Payload struct {
	Version           string  `json:"version"`
	GroupKey          string  `json:"groupKey"`
	TruncatedAlerts   int     `json:"truncatedAlerts"`
	Status            string  `json:"status"`
	Receiver          string  `json:"receiver"`
	GroupLabels       KV      `json:"groupLabels"`
	CommonLabels      KV      `json:"commonLabels"`
	CommonAnnotations KV      `json:"commonAnnotations"`
	ExternalURL       string  `json:"externalURL"`
	Alerts            []Alert `json:"alerts"`
}

// Alert is one alert in an Alertmanager generic webhook payload.
type Alert struct {
	Status       string    `json:"status"`
	Labels       KV        `json:"labels"`
	Annotations  KV        `json:"annotations"`
	StartsAt     time.Time `json:"startsAt"`
	EndsAt       time.Time `json:"endsAt"`
	GeneratorURL string    `json:"generatorURL"`
	Fingerprint  string    `json:"fingerprint"`
}

// Validate checks the bounded subset of the v4 webhook contract accepted by
// the adapter. Unknown JSON fields remain forward compatible because decoding
// is intentionally handled by encoding/json without DisallowUnknownFields.
func (payload Payload) Validate() error {
	if payload.Version != "4" {
		return fmt.Errorf("unsupported Alertmanager webhook version %q: want %q", payload.Version, "4")
	}
	if !validStatus(payload.Status) {
		return fmt.Errorf("invalid group status %q: want firing or resolved", payload.Status)
	}
	if len(payload.Alerts) == 0 {
		return fmt.Errorf("alerts must contain at least one alert")
	}
	if len(payload.Alerts) > 10 {
		return fmt.Errorf("alerts contains %d alerts: maximum is 10", len(payload.Alerts))
	}
	for i, alert := range payload.Alerts {
		if !validStatus(alert.Status) {
			return fmt.Errorf("alerts[%d] has invalid status %q: want firing or resolved", i, alert.Status)
		}
		if alert.StartsAt.IsZero() {
			return fmt.Errorf("alerts[%d] startsAt must not be zero", i)
		}
	}
	return nil
}

func validStatus(status string) bool {
	return status == "firing" || status == "resolved"
}
