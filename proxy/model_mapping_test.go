package proxy

import (
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestResolveConfiguredModelMappingExactAndWildcard(t *testing.T) {
	mapping := `{
		"gpt-*": "gpt-5.5",
		"gpt-5.4": "gpt-5.2",
		"*-mini": "gpt-5.4-mini",
		"*codex*": "gpt-5.3-codex"
	}`
	supported := []string{"gpt-5.5", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2"}

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "exact beats wildcard", model: "gpt-5.4", want: "gpt-5.2"},
		{name: "prefix wildcard", model: "gpt-5.1", want: "gpt-5.5"},
		{name: "suffix wildcard", model: "custom-mini", want: "gpt-5.4-mini"},
		{name: "substring wildcard", model: "my-codex-alias", want: "gpt-5.3-codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveConfiguredModelMapping(tt.model, mapping, supported)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.model)
			}
			if got != tt.want {
				t.Fatalf("resolveConfiguredModelMapping(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveConfiguredModelMappingSpecificWildcardWins(t *testing.T) {
	mapping := `{
		"gpt-*": "gpt-5.5",
		"gpt-5.4-*": "gpt-5.4-mini"
	}`
	got, ok := resolveConfiguredModelMapping("gpt-5.4-preview", mapping, []string{"gpt-5.5", "gpt-5.4-mini"})
	if !ok {
		t.Fatal("expected wildcard mapping")
	}
	if got != "gpt-5.4-mini" {
		t.Fatalf("mapped model = %q, want gpt-5.4-mini", got)
	}
}

func TestResolveConfiguredModelMappingCanonicalizesTargetAlias(t *testing.T) {
	got, ok := resolveConfiguredModelMapping("legacy-model", `{"legacy-model":"gpt5.5"}`, []string{"gpt-5.5"})
	if !ok {
		t.Fatal("expected exact mapping")
	}
	if got != "gpt-5.5" {
		t.Fatalf("mapped model = %q, want gpt-5.5", got)
	}

	got, ok = resolveConfiguredModelMapping("legacy-model", `{"legacy-model":"GPT-5.5"}`, []string{"gpt-5.5"})
	if !ok {
		t.Fatal("expected exact mapping")
	}
	if got != "gpt-5.5" {
		t.Fatalf("case-normalized mapped model = %q, want gpt-5.5", got)
	}
}

func TestResolveConfiguredModelMappingIgnoresInvalidJSON(t *testing.T) {
	got, ok := resolveConfiguredModelMapping("gpt-5.4", `{bad json`, []string{"gpt-5.5"})
	if ok {
		t.Fatal("invalid JSON should not map")
	}
	if got != "gpt-5.4" {
		t.Fatalf("model = %q, want original", got)
	}
}

func TestResolveAnthropicModelUsesWildcardBeforeDefaultFallback(t *testing.T) {
	got := resolveAnthropicModel("claude-opus-4-7", `{"claude-opus-*":"gpt-5.5"}`, []string{"gpt-5.5", "gpt-5.4"})
	if got != "gpt-5.5" {
		t.Fatalf("resolveAnthropicModel wildcard = %q, want gpt-5.5", got)
	}

	got = resolveAnthropicModel("claude-haiku-4-5", `{}`, []string{"gpt-5.4-mini"})
	if got != "gpt-5.4-mini" {
		t.Fatalf("resolveAnthropicModel default = %q, want gpt-5.4-mini", got)
	}
}

func TestApplyConfiguredModelMappingToBodyRewritesBeforeValidation(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	store.SetCodexModelMapping(`{"gpt-legacy-*":"gpt-5.5"}`)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-legacy-1","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped {
		t.Fatal("expected body model to be mapped")
	}
	if original != "gpt-legacy-1" || effective != "gpt-5.5" {
		t.Fatalf("original/effective = %q/%q, want gpt-legacy-1/gpt-5.5", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
	}
}

func TestApplyFastServiceTierModelAliasToBody(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped {
		t.Fatal("expected fast model alias to be mapped")
	}
	if original != "gpt-5.5-fast" || effective != "gpt-5.5" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.5-fast/gpt-5.5", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "service_tier").String(); got != "fast" {
		t.Fatalf("service_tier = %q, want fast; body=%s", got, body)
	}
}

func TestApplyFastServiceTierModelAliasDoesNotOverrideExplicitTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	tests := []struct {
		name            string
		raw             string
		wantServiceTier string
		wantServiceAlt  string
	}{
		{name: "snake service tier", raw: `{"model":"gpt-5.5-fast","service_tier":"default"}`, wantServiceTier: "default"},
		{name: "camel service tier", raw: `{"model":"gpt-5.5-fast","serviceTier":"flex"}`, wantServiceAlt: "flex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, effective, mapped := handler.applyConfiguredModelMappingToBody(
				[]byte(tt.raw),
				[]string{"gpt-5.5"},
			)
			if !mapped {
				t.Fatal("expected fast model alias to be mapped")
			}
			if effective != "gpt-5.5" {
				t.Fatalf("effective = %q, want gpt-5.5", effective)
			}
			if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
				t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
			}
			if tt.wantServiceTier != "" {
				if got := gjson.GetBytes(body, "service_tier").String(); got != tt.wantServiceTier {
					t.Fatalf("service_tier = %q, want %q; body=%s", got, tt.wantServiceTier, body)
				}
			}
			if tt.wantServiceAlt != "" {
				if got := gjson.GetBytes(body, "serviceTier").String(); got != tt.wantServiceAlt {
					t.Fatalf("serviceTier = %q, want %q; body=%s", got, tt.wantServiceAlt, body)
				}
				if gjson.GetBytes(body, "service_tier").Exists() {
					t.Fatalf("service_tier should not be injected when serviceTier is explicit; body=%s", body)
				}
			}
		})
	}
}

func TestApplyFastServiceTierModelAliasIsCaseInsensitive(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"GPT-5.4-MINI-FAST","input":"hello"}`),
		[]string{"gpt-5.4-mini"},
	)
	if !mapped {
		t.Fatal("expected uppercase fast suffix to be mapped")
	}
	if original != "GPT-5.4-MINI-FAST" || effective != "gpt-5.4-mini" {
		t.Fatalf("original/effective = %q/%q, want GPT-5.4-MINI-FAST/gpt-5.4-mini", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4-mini" {
		t.Fatalf("body model = %q, want gpt-5.4-mini; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "service_tier").String(); got != "fast" {
		t.Fatalf("service_tier = %q, want fast; body=%s", got, body)
	}
}

func TestFastModelAliasChatTranslationUsesPriorityTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	mappedBody, _, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","messages":[{"role":"user","content":"hello"}]}`),
		[]string{"gpt-5.5"},
	)
	if !mapped || effective != "gpt-5.5" {
		t.Fatalf("mapped/effective = %v/%q, want true/gpt-5.5", mapped, effective)
	}
	translated, err := TranslateRequest(mappedBody)
	if err != nil {
		t.Fatalf("TranslateRequest returned error: %v", err)
	}
	if got := gjson.GetBytes(translated, "model").String(); got != "gpt-5.5" {
		t.Fatalf("translated model = %q, want gpt-5.5; body=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "service_tier").String(); got != "priority" {
		t.Fatalf("translated service_tier = %q, want priority; body=%s", got, translated)
	}
}

func TestFastModelAliasResponsesPrepareUsesPriorityTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	mappedBody, _, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped || effective != "gpt-5.5" {
		t.Fatalf("mapped/effective = %v/%q, want true/gpt-5.5", mapped, effective)
	}
	prepared, _ := PrepareResponsesBody(mappedBody)
	if got := gjson.GetBytes(prepared, "model").String(); got != "gpt-5.5" {
		t.Fatalf("prepared model = %q, want gpt-5.5; body=%s", got, prepared)
	}
	if got := gjson.GetBytes(prepared, "service_tier").String(); got != "priority" {
		t.Fatalf("prepared service_tier = %q, want priority; body=%s", got, prepared)
	}
}

func TestFastModelAliasOpenAIResponsesPrepareUsesPriorityTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	mappedBody, _, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped || effective != "gpt-5.5" {
		t.Fatalf("mapped/effective = %v/%q, want true/gpt-5.5", mapped, effective)
	}
	prepared := PrepareOpenAIResponsesBody(mappedBody)
	if got := gjson.GetBytes(prepared, "model").String(); got != "gpt-5.5" {
		t.Fatalf("prepared model = %q, want gpt-5.5; body=%s", got, prepared)
	}
	if got := gjson.GetBytes(prepared, "service_tier").String(); got != "priority" {
		t.Fatalf("prepared service_tier = %q, want priority; body=%s", got, prepared)
	}
}

func TestFastModelAliasOpenAIResponsesPreparePreservesExplicitTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	handler := NewHandler(store, nil, nil, nil)

	mappedBody, _, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello","serviceTier":"FAST"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped || effective != "gpt-5.5" {
		t.Fatalf("mapped/effective = %v/%q, want true/gpt-5.5", mapped, effective)
	}
	if gjson.GetBytes(mappedBody, "service_tier").Exists() {
		t.Fatalf("fast suffix should not inject service_tier when serviceTier is explicit; body=%s", mappedBody)
	}
	prepared := PrepareOpenAIResponsesBody(mappedBody)
	if got := gjson.GetBytes(prepared, "model").String(); got != "gpt-5.5" {
		t.Fatalf("prepared model = %q, want gpt-5.5; body=%s", got, prepared)
	}
	if got := gjson.GetBytes(prepared, "service_tier").String(); got != "priority" {
		t.Fatalf("explicit FAST service_tier = %q, want priority; body=%s", got, prepared)
	}
	if gjson.GetBytes(prepared, "serviceTier").Exists() {
		t.Fatalf("serviceTier should be removed after preparation; body=%s", prepared)
	}
}

func TestFastModelAliasDisabledDoesNotInjectTier(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	store.SetCodexFastModelAliasEnabled(false)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped {
		t.Fatal("expected mapping=true even when alias injection is disabled (model should still be stripped)")
	}
	if original != "gpt-5.5-fast" || effective != "gpt-5.5" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.5-fast/gpt-5.5", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
	}
	if gjson.GetBytes(body, "service_tier").Exists() {
		t.Fatalf("service_tier should not be injected when alias is disabled; body=%s", body)
	}
}

func TestFastModelAliasDefaultsOnWithoutStore(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5-fast","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if !mapped {
		t.Fatal("expected fast alias to default on without store")
	}
	if original != "gpt-5.5-fast" || effective != "gpt-5.5" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.5-fast/gpt-5.5", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "service_tier").String(); got != "fast" {
		t.Fatalf("service_tier = %q, want fast; body=%s", got, body)
	}
}

func TestApplyReasoningEffortModelAliasToBody(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	store.SetReasoningEffortModels(`[{"model":"gpt-5.5","effort":"xhigh"}]`)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5(xhigh)","input":"hello"}`),
		[]string{"gpt-5.5", "gpt-5.5(xhigh)"},
	)
	if !mapped {
		t.Fatal("expected alias to be resolved")
	}
	if original != "gpt-5.5(xhigh)" || effective != "gpt-5.5" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.5(xhigh)/gpt-5.5", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("body model = %q, want gpt-5.5; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, body)
	}
}

func TestApplyReasoningEffortModelAliasBeforeCodexMapping(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	store.SetReasoningEffortModels(`[{"model":"gpt-5.5","effort":"xhigh"}]`)
	store.SetCodexModelMapping(`{"gpt-5.5":"gpt-5.4"}`)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.5(xhigh)","input":"hello"}`),
		[]string{"gpt-5.5", "gpt-5.4", "gpt-5.5(xhigh)"},
	)
	if !mapped {
		t.Fatal("expected alias and mapping to be applied")
	}
	if original != "gpt-5.5(xhigh)" || effective != "gpt-5.4" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.5(xhigh)/gpt-5.4", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
		t.Fatalf("body model = %q, want gpt-5.4; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, body)
	}
}

func TestApplyConfiguredModelMappingToBodyIgnoresClaudeMappingSetting(t *testing.T) {
	store := auth.NewStore(nil, nil, nil)
	store.SetModelMapping(`{"gpt-5.2":"gpt-5.5"}`)
	handler := NewHandler(store, nil, nil, nil)

	body, original, effective, mapped := handler.applyConfiguredModelMappingToBody(
		[]byte(`{"model":"gpt-5.2","input":"hello"}`),
		[]string{"gpt-5.5"},
	)
	if mapped {
		t.Fatal("Claude model_mapping should not rewrite Codex/OpenAI requests")
	}
	if original != "gpt-5.2" || effective != "gpt-5.2" {
		t.Fatalf("original/effective = %q/%q, want gpt-5.2/gpt-5.2", original, effective)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.2" {
		t.Fatalf("body model = %q, want gpt-5.2; body=%s", got, body)
	}
}

func TestStripCompactModelSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		want      string
		wantMatch bool
	}{
		{name: "gpt-5.4 compact", model: "gpt-5.4-openai-compact", want: "gpt-5.4", wantMatch: true},
		{name: "gpt-5.5 compact", model: "gpt-5.5-openai-compact", want: "gpt-5.5", wantMatch: true},
		{name: "case insensitive suffix", model: "gpt-5.4-OpenAI-Compact", want: "gpt-5.4", wantMatch: true},
		{name: "trims whitespace", model: "  gpt-5.4-openai-compact  ", want: "gpt-5.4", wantMatch: true},
		{name: "no suffix untouched", model: "gpt-5.4", want: "gpt-5.4", wantMatch: false},
		{name: "codex base untouched", model: "gpt-5.3-codex", want: "gpt-5.3-codex", wantMatch: false},
		{name: "suffix only stays", model: "-openai-compact", want: "-openai-compact", wantMatch: false},
		{name: "empty stays", model: "", want: "", wantMatch: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := stripCompactModelSuffix(tt.model)
			if matched != tt.wantMatch {
				t.Fatalf("stripCompactModelSuffix(%q) matched = %v, want %v", tt.model, matched, tt.wantMatch)
			}
			if got != tt.want {
				t.Fatalf("stripCompactModelSuffix(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestStripCompactModelSuffixFromBody(t *testing.T) {
	body, model, matched := stripCompactModelSuffixFromBody([]byte(`{"model":"gpt-5.4-openai-compact","input":"hi"}`))
	if !matched {
		t.Fatalf("expected compact suffix to be stripped")
	}
	if model != "gpt-5.4" {
		t.Fatalf("stripped model = %q, want gpt-5.4", model)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
		t.Fatalf("body model = %q, want gpt-5.4; body=%s", got, body)
	}
	// input 等其它字段保持不变
	if got := gjson.GetBytes(body, "input").String(); got != "hi" {
		t.Fatalf("input should be preserved, got %q", got)
	}

	// 无后缀请求原样返回，不改写。
	orig := []byte(`{"model":"gpt-5.4","input":"hi"}`)
	body2, model2, matched2 := stripCompactModelSuffixFromBody(orig)
	if matched2 {
		t.Fatalf("plain model should not be rewritten")
	}
	if model2 != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", model2)
	}
	if string(body2) != string(orig) {
		t.Fatalf("body should be untouched, got %s", body2)
	}
}
