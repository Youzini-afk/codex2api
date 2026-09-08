package proxy

import (
	"slices"
	"testing"
)

// 设置里的思考强度别名同样按模型放行 max:5.6+ 保留,旧模型钳到 xhigh。
func TestParseReasoningEffortModelEntries_MaxGatedByModel(t *testing.T) {
	supported := []string{"gpt-5.6-sol", "gpt-5.4"}
	entries, err := parseReasoningEffortModelEntries(
		`[{"model":"gpt-5.6-sol","effort":"max"},{"model":"gpt-5.4","effort":"max"}]`,
		supported, true)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[0].Effort != "max" {
		t.Fatalf("gpt-5.6-sol effort = %q, want max", entries[0].Effort)
	}
	if entries[1].Effort != "xhigh" {
		t.Fatalf("gpt-5.4 effort = %q, want xhigh", entries[1].Effort)
	}
}

func TestAutomaticReasoningEffortsFollowModelCapabilities(t *testing.T) {
	tests := []struct {
		model string
		want  []string
	}{
		{model: "gpt-5.4", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{model: "gpt-5.5", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{model: "gpt-5.6-sol", want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{model: "gpt-6-astra", want: []string{"low", "medium", "high", "xhigh", "max"}},
		{model: "gpt-5.4-pro", want: []string{"medium", "high", "xhigh"}},
		{model: "gpt-5.3-codex-spark", want: []string{"low", "medium", "high", "xhigh"}},
		{model: "codex-auto-review", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{model: "gpt-reserve", want: nil},
		{model: "gpt-image-2", want: nil},
	}
	for _, test := range tests {
		if got := automaticReasoningEffortsForModel(test.model); !slices.Equal(got, test.want) {
			t.Errorf("automaticReasoningEffortsForModel(%q) = %v, want %v", test.model, got, test.want)
		}
	}
}
