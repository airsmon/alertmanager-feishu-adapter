// Package card converts Alertmanager webhook payloads into Feishu Card 2.0
// messages. Card fields are strongly typed so schema is always encoded as the
// JSON string "2.0" rather than the number 2.
package card

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/airsmon/alertmanager-feishu-adapter/internal/alertmanager"
)

const (
	DefaultEnvironment = "production"
	DefaultCluster     = "infra-01"
	SchemaV2           = "2.0"

	maxSummaryRunes     = 300
	maxDescriptionRunes = 500
)

// Options controls deployment-specific text and time rendering. Its zero value
// is ready for the current production environment.
type Options struct {
	Environment string
	Cluster     string
	Location    *time.Location
	Now         func() time.Time
}

// Message is the request body accepted by a Feishu custom bot webhook.
type Message struct {
	MsgType string `json:"msg_type"`
	Card    Card   `json:"card"`
}

// Card is a Feishu Interactive Card 2.0 document.
type Card struct {
	Schema string `json:"schema"`
	Config Config `json:"config"`
	Header Header `json:"header"`
	Body   Body   `json:"body"`
}

type Config struct {
	WidthMode string `json:"width_mode"`
}

type Header struct {
	Template string `json:"template"`
	Title    Text   `json:"title"`
}

type Body struct {
	Direction       string    `json:"direction"`
	VerticalSpacing string    `json:"vertical_spacing"`
	Elements        []Element `json:"elements"`
}

// Element models the element variants used by the production card. Optional
// fields are omitted so each variant remains valid Card 2.0 JSON.
type Element struct {
	Tag               string       `json:"tag"`
	FlexMode          string       `json:"flex_mode,omitempty"`
	HorizontalSpacing string       `json:"horizontal_spacing,omitempty"`
	Columns           []Column     `json:"columns,omitempty"`
	Content           string       `json:"content,omitempty"`
	Expanded          *bool        `json:"expanded,omitempty"`
	Header            *PanelHeader `json:"header,omitempty"`
	Elements          []Element    `json:"elements,omitempty"`
	Text              *Text        `json:"text,omitempty"`
}

type Column struct {
	Tag             string    `json:"tag"`
	Width           string    `json:"width"`
	Weight          int       `json:"weight"`
	VerticalSpacing string    `json:"vertical_spacing"`
	Elements        []Element `json:"elements"`
}

type PanelHeader struct {
	Title Text `json:"title"`
}

type Text struct {
	Tag       string `json:"tag"`
	TextSize  string `json:"text_size,omitempty"`
	TextColor string `json:"text_color,omitempty"`
	Content   string `json:"content"`
}

// Build renders one Alertmanager notification group using the current card
// layout. The returned value can be passed directly to json.Marshal.
func Build(payload alertmanager.Payload, opts Options) Message {
	opts = withDefaults(opts)
	status := normalizedStatus(payload.Status)
	alertName := commonOrFirst(payload, "alertname")
	severity := commonOrFirst(payload, "severity")
	namespace := commonOrFirst(payload, "namespace")
	if namespace == "" {
		namespace = "-"
	}

	top := Element{
		Tag:               "column_set",
		FlexMode:          "none",
		HorizontalSpacing: "16px",
		Columns: []Column{
			{
				Tag:             "column",
				Width:           "weighted",
				Weight:          1,
				VerticalSpacing: "16px",
				Elements: []Element{
					markdown("**状态**\n" + statusDisplay(status)),
					markdown("**命名空间**\n" + safeText(namespace)),
				},
			},
			{
				Tag:             "column",
				Width:           "weighted",
				Weight:          1,
				VerticalSpacing: "16px",
				Elements: []Element{
					markdown("**级别**\n" + valueOrDash(severity)),
					markdown("**环境 / 集群**\n" + safeText(opts.Environment) + " / " + safeText(opts.Cluster)),
				},
			},
		},
	}

	expanded := false
	footer := fmt.Sprintf("Alertmanager · %s · %s · 本组 %d 条", safeText(opts.Environment), safeText(opts.Cluster), len(payload.Alerts))
	if len(payload.Alerts) > 0 {
		footer += " · 首条 ID " + shortID(payload.Alerts[0].Fingerprint)
	}

	return Message{
		MsgType: "interactive",
		Card: Card{
			Schema: SchemaV2,
			Config: Config{WidthMode: "default"},
			Header: Header{
				Template: headerColor(status, severity),
				Title: Text{
					Tag:     "plain_text",
					Content: headerTitle(status, alertName),
				},
			},
			Body: Body{
				Direction:       "vertical",
				VerticalSpacing: "8px",
				Elements: []Element{
					top,
					markdown(coreContent(payload, opts)),
					{
						Tag:      "collapsible_panel",
						Expanded: &expanded,
						Header: &PanelHeader{Title: Text{
							Tag:     "plain_text",
							Content: "更多信息 · 资源 / 详情 / 追踪",
						}},
						Elements: []Element{markdown(detailContent(payload))},
					},
					{
						Tag: "div",
						Text: &Text{
							Tag:       "plain_text",
							TextSize:  "notation",
							TextColor: "grey",
							Content:   footer,
						},
					},
				},
			},
		},
	}
}

func withDefaults(opts Options) Options {
	if opts.Environment == "" {
		opts.Environment = DefaultEnvironment
	}
	if opts.Cluster == "" {
		opts.Cluster = DefaultCluster
	}
	if opts.Location == nil {
		// China has used UTC+08:00 without daylight-saving changes since 1991.
		// A fixed zone keeps scratch/distroless images independent of tzdata.
		opts.Location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

func markdown(content string) Element {
	return Element{Tag: "markdown", Content: content}
}

func normalizedStatus(status string) string {
	if strings.EqualFold(strings.TrimSpace(status), "resolved") {
		return "resolved"
	}
	return "firing"
}

func headerColor(status, severity string) string {
	if status == "resolved" {
		return "green"
	}
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return "red"
	case "warning":
		return "orange"
	case "info":
		return "blue"
	default:
		return "grey"
	}
}

func headerTitle(status, alertName string) string {
	if status == "resolved" {
		return "✅ [RESOLVED] " + safeText(alertName)
	}
	return "🚨 [FIRING] " + safeText(alertName)
}

func statusDisplay(status string) string {
	if status == "resolved" {
		return "🟢 UP / RESOLVED"
	}
	return "🔴 DOWN / FIRING"
}

func commonOrFirst(payload alertmanager.Payload, key string) string {
	if value := payload.CommonLabels.Get(key); value != "" {
		return value
	}
	if len(payload.Alerts) > 0 {
		return payload.Alerts[0].Labels.Get(key)
	}
	return ""
}

func coreContent(payload alertmanager.Payload, opts Options) string {
	var blocks []string
	for _, alert := range payload.Alerts {
		status := normalizedStatus(alert.Status)
		if alert.Status == "" {
			status = normalizedStatus(payload.Status)
		}
		lines := []string{
			"**告警名称：** " + valueOrDash(alert.Labels.Get("alertname")),
			"**告警对象：** " + objectName(alert.Labels),
			"**告警摘要：** " + valueOrDash(truncateText(alert.Annotations.Get("summary"), maxSummaryRunes)),
			"**首次触发：** " + formatTime(alert.StartsAt, opts.Location) + "（Asia/Shanghai）",
		}
		if status == "resolved" {
			lines = append(lines, "**恢复时间：** "+formatTime(alert.EndsAt, opts.Location)+"（Asia/Shanghai）")
		}
		lines = append(lines, "**持续时长：** "+formatDuration(alertDuration(alert, status, opts.Now())))
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

func detailContent(payload alertmanager.Payload) string {
	var blocks []string
	for _, alert := range payload.Alerts {
		labels := alert.Labels
		lines := []string{"**追踪标识：** " + shortID(alert.Fingerprint)}
		appendLabel := func(title, key string) {
			if value := labels.Get(key); value != "" {
				lines = append(lines, "**"+title+"：** "+safeText(value))
			}
		}

		appendLabel("数据中心", "datacenter")
		appendLabel("设备名称", "device")
		appendLabel("设备 ID", "equip_id")
		appendLabel("数据源", "exported_instance")
		appendLabel("机柜名称", "rack")
		appendLabel("通道类型", "aisle")
		appendLabel("采集位置", "position")
		appendLabel("电源相位", "phase")
		appendLabel("Pod 名称", "pod")
		appendLabel("容器名称", "container")
		if workload := workloadName(labels); workload != "" {
			lines = append(lines, "**工作负载：** "+workload)
		}
		appendLabel("节点名称", "node")
		appendLabel("服务名称", "service")
		appendLabel("端点名称", "endpoint")
		appendLabel("采集任务", "job")
		appendLabel("实例地址", "instance")
		lines = append(lines,
			"**告警详情：** "+valueOrDash(truncateText(alert.Annotations.Get("description"), maxDescriptionRunes)),
			"**告警指纹：** "+valueOrDash(alert.Fingerprint),
		)
		if dashboard := alert.Annotations.Get("dashboard_url"); validHTTPSURL(dashboard) {
			lines = append(lines, "**监控面板：** [查看 Dashboard]("+dashboard+")")
		}
		if runbook := alert.Annotations.Get("runbook_url"); validHTTPSURL(runbook) {
			lines = append(lines, "**处置手册：** [查看 Runbook]("+runbook+")")
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

func objectName(labels alertmanager.KV) string {
	if value := labels.Get("device"); value != "" {
		object := "Device / " + safeText(value)
		if rack := labels.Get("rack"); rack != "" {
			object += " / " + safeText(rack)
		}
		return object
	}
	for _, candidate := range []struct {
		key, kind string
	}{
		{"pod", "Pod"},
		{"node", "Node"},
		{"service", "Service"},
		{"instance", "Instance"},
		{"job", "Job"},
		{"endpoint", "Endpoint"},
	} {
		if value := labels.Get(candidate.key); value != "" {
			return candidate.kind + " / " + safeText(value)
		}
	}
	return "-"
}

func workloadName(labels alertmanager.KV) string {
	if value := labels.Get("workload"); value != "" {
		return safeText(value)
	}
	for _, candidate := range []struct {
		key, kind string
	}{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
		{"daemonset", "DaemonSet"},
		{"replicaset", "ReplicaSet"},
	} {
		if value := labels.Get(candidate.key); value != "" {
			return candidate.kind + " / " + safeText(value)
		}
	}
	if value := labels.Get("owner_name"); value != "" {
		if kind := labels.Get("owner_kind"); kind != "" {
			return safeText(kind) + " / " + safeText(value)
		}
		return safeText(value)
	}
	return ""
}

func formatTime(value time.Time, location *time.Location) string {
	if value.IsZero() {
		return "-"
	}
	return value.In(location).Format("2006-01-02 15:04:05")
}

func alertDuration(alert alertmanager.Alert, status string, now time.Time) time.Duration {
	end := now
	if status == "resolved" && !alert.EndsAt.IsZero() {
		end = alert.EndsAt
	}
	if alert.StartsAt.IsZero() || end.Before(alert.StartsAt) {
		return 0
	}
	return end.Sub(alert.StartsAt)
}

func formatDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	value = value.Truncate(time.Second)
	hours := value / time.Hour
	value %= time.Hour
	minutes := value / time.Minute
	seconds := (value % time.Minute) / time.Second
	var parts []string
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return safeText(value)
}

func shortID(value string) string {
	value = safeText(value)
	return truncateText(value, 8)
}

func truncateText(value string, limit int) string {
	value = safeText(value)
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

// safeText neutralizes Feishu's <at ...> mention prefix in every dynamic value,
// including mixed-case forms, without corrupting UTF-8 text.
func safeText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	var out strings.Builder
	last := 0
	for i := 0; i+2 < len(value); i++ {
		if value[i] != '<' || (value[i+1] != 'a' && value[i+1] != 'A') || (value[i+2] != 't' && value[i+2] != 'T') {
			continue
		}
		out.WriteString(value[last:i])
		out.WriteString("＜")
		last = i + 1
	}
	if last == 0 {
		return value
	}
	out.WriteString(value[last:])
	return out.String()
}

func validHTTPSURL(value string) bool {
	if value == "" || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	return true
}
