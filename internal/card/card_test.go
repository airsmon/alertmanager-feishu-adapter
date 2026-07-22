package card

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/airsmon/alertmanager-feishu-adapter/internal/alertmanager"
)

func TestBuildFiringCardFromFixture(t *testing.T) {
	payload := loadFixture(t)
	now := time.Date(2026, 7, 22, 6, 47, 35, 0, time.UTC)
	got := Build(payload, Options{Now: func() time.Time { return now }})

	if got.MsgType != "interactive" {
		t.Errorf("MsgType = %q", got.MsgType)
	}
	if got.Card.Schema != "2.0" {
		t.Errorf("Schema = %q, want 2.0", got.Card.Schema)
	}
	if got.Card.Config.WidthMode != "default" {
		t.Errorf("WidthMode = %q, want default", got.Card.Config.WidthMode)
	}
	if got.Card.Header.Template != "red" {
		t.Errorf("header color = %q, want red", got.Card.Header.Template)
	}
	if got.Card.Header.Title.Content != "🚨 [FIRING] DeviceOutputVoltageLow" {
		t.Errorf("header title = %q", got.Card.Header.Title.Content)
	}

	elements := got.Card.Body.Elements
	if len(elements) != 4 {
		t.Fatalf("body elements = %d, want 4", len(elements))
	}
	wantTags := []string{"column_set", "markdown", "collapsible_panel", "div"}
	for i, want := range wantTags {
		if elements[i].Tag != want {
			t.Errorf("element[%d].Tag = %q, want %q", i, elements[i].Tag, want)
		}
	}

	top := elements[0]
	if top.FlexMode != "none" || top.HorizontalSpacing != "16px" || len(top.Columns) != 2 {
		t.Errorf("unexpected top column layout: %#v", top)
	}
	for i, column := range top.Columns {
		if column.Width != "weighted" || column.Weight != 1 || column.VerticalSpacing != "16px" {
			t.Errorf("column %d layout = %#v", i, column)
		}
	}
	if got := top.Columns[0].Elements[0].Content; got != "**状态**\n🔴 DOWN / FIRING" {
		t.Errorf("status content = %q", got)
	}
	if got := top.Columns[0].Elements[1].Content; got != "**命名空间**\nmonitoring" {
		t.Errorf("namespace content = %q", got)
	}
	if got := top.Columns[1].Elements[0].Content; got != "**级别**\ncritical" {
		t.Errorf("severity content = %q", got)
	}
	if got := top.Columns[1].Elements[1].Content; got != "**环境 / 集群**\nproduction / infra-01" {
		t.Errorf("environment content = %q", got)
	}

	core := elements[1].Content
	assertContains(t, core, "**告警对象：** Device / UPS-A / RACK-01")
	assertContains(t, core, "**首次触发：** 2026-07-22 13:45:32（Asia/Shanghai）")
	assertContains(t, core, "**持续时长：** 1h 2m 3s")
	assertContains(t, core, "＜at user_id=all>请处理</at>")
	if strings.Contains(strings.ToLower(core), "<at") {
		t.Errorf("core contains an active Feishu mention: %q", core)
	}

	panel := elements[2]
	if panel.Expanded == nil || *panel.Expanded {
		t.Errorf("collapsible panel should be explicitly collapsed")
	}
	details := panel.Elements[0].Content
	for _, want := range []string{
		"**设备名称：** UPS-A",
		"**Pod 名称：** device-exporter-7d8f9c6b5-x2k4m",
		"**工作负载：** Deployment / device-exporter",
		"**实例地址：** 192.0.2.10:8080",
		"[查看 Dashboard](https://grafana.example.com/d/devices?var-device=UPS-A)",
	} {
		assertContains(t, details, want)
	}
	if strings.Contains(details, "查看 Runbook") {
		t.Errorf("insecure runbook URL was rendered: %q", details)
	}

	footer := elements[3].Text
	if footer == nil || footer.Content != "Alertmanager · production · infra-01 · 本组 1 条 · 首条 ID abcdef01" {
		t.Errorf("footer = %#v", footer)
	}
}

func TestSchemaMarshalsAsJSONString(t *testing.T) {
	message := Build(alertmanager.Payload{}, Options{})
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	cardValue, ok := raw["card"].(map[string]any)
	if !ok {
		t.Fatalf("card has type %T", raw["card"])
	}
	schema, ok := cardValue["schema"].(string)
	if !ok || schema != "2.0" {
		t.Fatalf("schema = %#v (%T), want JSON string 2.0", cardValue["schema"], cardValue["schema"])
	}
	if strings.Contains(string(data), `"schema":2`) {
		t.Fatalf("schema was encoded as a number: %s", data)
	}
}

func TestHeaderColorMappingAndResolvedOverride(t *testing.T) {
	tests := []struct {
		status, severity, want string
	}{
		{"firing", "critical", "red"},
		{"firing", "warning", "orange"},
		{"firing", "info", "blue"},
		{"firing", "unknown", "grey"},
		{"resolved", "critical", "green"},
		{"RESOLVED", "warning", "green"},
	}
	for _, test := range tests {
		t.Run(test.status+"_"+test.severity, func(t *testing.T) {
			payload := alertmanager.Payload{
				Status:       test.status,
				CommonLabels: alertmanager.KV{"alertname": "Test", "severity": test.severity},
			}
			if got := Build(payload, Options{}).Card.Header.Template; got != test.want {
				t.Errorf("color = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolvedCardUsesEndTimeAndTotalDuration(t *testing.T) {
	start := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	end := start.Add(2*time.Hour + 3*time.Minute + 4*time.Second)
	payload := alertmanager.Payload{
		Status:       "resolved",
		CommonLabels: alertmanager.KV{"alertname": "Recovered", "severity": "critical"},
		Alerts: []alertmanager.Alert{{
			Status:      "resolved",
			Labels:      alertmanager.KV{"alertname": "Recovered", "pod": "pod-1"},
			Annotations: alertmanager.KV{"summary": "ok"},
			StartsAt:    start,
			EndsAt:      end,
			Fingerprint: "1234567890",
		}},
	}
	got := Build(payload, Options{Now: func() time.Time { return end.Add(24 * time.Hour) }})
	if got.Card.Header.Template != "green" {
		t.Errorf("resolved color = %q", got.Card.Header.Template)
	}
	core := got.Card.Body.Elements[1].Content
	assertContains(t, core, "**恢复时间：** 2026-07-22 11:03:04（Asia/Shanghai）")
	assertContains(t, core, "**持续时长：** 2h 3m 4s")
}

func TestSafeTextAndUnicodeTruncation(t *testing.T) {
	input := "前缀<at user_id=all>全员</at>和<AT>大写</AT>"
	got := safeText(input)
	if strings.Contains(strings.ToLower(got), "<at") {
		t.Fatalf("safeText left active mention syntax: %q", got)
	}
	if !strings.Contains(got, "＜at") || !strings.Contains(got, "＜AT") {
		t.Fatalf("safeText did not preserve readable text: %q", got)
	}

	long := strings.Repeat("界", 301)
	truncated := truncateText(long, 300)
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncated text is invalid UTF-8")
	}
	if got := utf8.RuneCountInString(truncated); got != 300 {
		t.Fatalf("rune count = %d, want 300", got)
	}

	invalid := string([]byte{'o', 'k', 0xff, '<', 'a', 't'})
	if got := truncateText(invalid, 20); !utf8.ValidString(got) || strings.Contains(strings.ToLower(got), "<at") {
		t.Fatalf("invalid UTF-8/mention was not normalized: %q", got)
	}
}

func TestValidHTTPSURL(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"https://grafana.example.com/d/abc?var-job=device-exporter", true},
		{"https://grafana.example.com:8443/runbook#step-1", true},
		{"http://grafana.example.com/d/abc", false},
		{"https://", false},
		{"https://user:pass@example.com/private", false},
		{"https://example.com/path with space", false},
		{"https://example.com/\nheader", false},
		{"javascript:alert(1)", false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			if got := validHTTPSURL(test.value); got != test.want {
				t.Errorf("validHTTPSURL(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestObjectAndWorkloadPriority(t *testing.T) {
	labels := alertmanager.KV{
		"device":     "UPS-1",
		"rack":       "R-1",
		"pod":        "pod-1",
		"workload":   "custom/workload",
		"deployment": "deployment-1",
		"owner_kind": "Job",
		"owner_name": "job-1",
		"service":    "service-1",
		"instance":   "instance-1",
	}
	if got := objectName(labels); got != "Device / UPS-1 / R-1" {
		t.Errorf("objectName = %q", got)
	}
	if got := workloadName(labels); got != "custom/workload" {
		t.Errorf("workloadName = %q", got)
	}
}

func loadFixture(t *testing.T) alertmanager.Payload {
	t.Helper()
	data, err := os.ReadFile("../../testdata/firing.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload alertmanager.Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return payload
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("content does not contain %q:\n%s", want, got)
	}
}
