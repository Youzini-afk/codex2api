package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/codex2api/security"
)

type ReasoningEffortModel struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// reasoningEffortAliasSuffixes is deliberately kept separate from the
// persisted model(effort) aliases.  The suffix aliases are a request/catalog
// convenience and are only valid for real, currently supported Codex models.
var reasoningEffortAliasSuffixes = []string{"none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"}

// resolveAutomaticReasoningEffortModelAlias resolves <model>-<effort>.  A
// model must be present in the current supported catalog; this prevents names
// for unrelated providers (and image models) from being rewritten. The effort
// must also belong to that model family's known capability set.
func resolveAutomaticReasoningEffortModelAlias(model string, supportedModels []string) (ReasoningEffortModel, bool) {
	model = strings.TrimSpace(model)
	if model == "" || len(supportedModels) == 0 {
		return ReasoningEffortModel{}, false
	}
	lower := strings.ToLower(model)
	for _, effort := range reasoningEffortAliasSuffixes {
		suffix := "-" + effort
		if !strings.HasSuffix(lower, suffix) {
			continue
		}
		base := strings.TrimSpace(model[:len(model)-len(suffix)])
		if !isRealSupportedReasoningModel(base, supportedModels) {
			continue
		}
		canonical := canonicalizeCodexModel(base, supportedModels)
		if !modelIDInList(canonical, supportedModels) || isImageReasoningModel(canonical) {
			continue
		}
		if !automaticReasoningEffortAllowed(canonical, effort) {
			continue
		}
		return ReasoningEffortModel{Model: canonical, Effort: effort}, true
	}
	return ReasoningEffortModel{}, false
}

func isImageReasoningModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "image")
}

func isRealSupportedReasoningModel(model string, supportedModels []string) bool {
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, "()") || isImageReasoningModel(model) {
		return false
	}
	if !modelIDInList(model, supportedModels) {
		return false
	}
	// Never recurse through an already generated suffix alias.  A real Codex
	// model ID ending in one of these words is still safe unless its own base is
	// also a supported model; the latter is precisely the recursive alias case.
	lower := strings.ToLower(model)
	for _, effort := range reasoningEffortAliasSuffixes {
		suffix := "-" + effort
		if strings.HasSuffix(lower, suffix) {
			underlying := strings.TrimSpace(model[:len(model)-len(suffix)])
			if underlying != "" && modelIDInList(underlying, supportedModels) {
				return false
			}
		}
	}
	return true
}

// automaticReasoningEffortAliases returns catalog aliases for a real Codex
// model.  Manual model(effort) aliases and aliases already synthesized by a
// previous pass are excluded by construction.
func automaticReasoningEffortAliases(model string) []ReasoningEffortModel {
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, "()") || isImageReasoningModel(model) {
		return nil
	}
	lower := strings.ToLower(model)
	for _, effort := range reasoningEffortAliasSuffixes {
		if strings.HasSuffix(lower, "-"+effort) {
			return nil
		}
	}
	efforts := automaticReasoningEffortsForModel(model)
	result := make([]ReasoningEffortModel, 0, len(efforts))
	for _, effort := range efforts {
		result = append(result, ReasoningEffortModel{Model: model, Effort: effort})
	}
	return result
}

// automaticReasoningEffortsForModel exposes only effort values known to work
// for the current Codex/OpenAI model families. The manual model(effort) editor
// remains available for experimental or future values such as minimal/ultra.
func automaticReasoningEffortsForModel(model string) []string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || isImageReasoningModel(model) {
		return nil
	}
	if model == "codex-auto-review" {
		return []string{"none", "low", "medium", "high", "xhigh"}
	}
	if strings.HasPrefix(model, "gpt-daybreak-") {
		return []string{"low", "medium", "high", "xhigh", "max"}
	}
	if model == "gpt-reserve" || !strings.HasPrefix(model, "gpt-") {
		return nil
	}
	if strings.Contains(model, "-pro") {
		return []string{"medium", "high", "xhigh"}
	}

	version := strings.TrimPrefix(model, "gpt-")
	if dash := strings.IndexByte(version, '-'); dash >= 0 {
		version = version[:dash]
	}
	parts := strings.Split(version, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	if major > 5 {
		return []string{"low", "medium", "high", "xhigh", "max"}
	}
	if major != 5 || len(parts) < 2 {
		return nil
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	if minor == 3 && strings.Contains(model, "codex") {
		return []string{"low", "medium", "high", "xhigh"}
	}
	if minor < 4 {
		return nil
	}
	efforts := []string{"none", "low", "medium", "high", "xhigh"}
	if minor >= 6 {
		efforts = append(efforts, "max")
	}
	return efforts
}

func automaticReasoningEffortAllowed(model, effort string) bool {
	for _, allowed := range automaticReasoningEffortsForModel(model) {
		if allowed == effort {
			return true
		}
	}
	return false
}

func automaticReasoningEffortModelAlias(entry ReasoningEffortModel) string {
	if strings.TrimSpace(entry.Model) == "" || strings.TrimSpace(entry.Effort) == "" {
		return ""
	}
	return strings.TrimSpace(entry.Model) + "-" + strings.ToLower(strings.TrimSpace(entry.Effort))
}

func ReasoningEffortModelAlias(model, effort string) string {
	model = strings.TrimSpace(model)
	effort = normalizeConfiguredReasoningEffort(effort, model)
	if model == "" || effort == "" {
		return ""
	}
	return fmt.Sprintf("%s(%s)", model, effort)
}

func NormalizeReasoningEffortModelsJSON(value string, supportedModels []string) (string, error) {
	entries, err := parseReasoningEffortModelEntries(value, supportedModels, true)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "[]", nil
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseReasoningEffortModelEntries(value string, supportedModels []string, strict bool) ([]ReasoningEffortModel, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "[]" {
		return nil, nil
	}

	var raw []ReasoningEffortModel
	dec := json.NewDecoder(strings.NewReader(value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		if strict {
			return nil, fmt.Errorf("reasoning_effort_models 必须是 JSON 数组: %w", err)
		}
		return nil, nil
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if strict {
			if err == nil {
				return nil, fmt.Errorf("reasoning_effort_models 只能包含一个 JSON 数组")
			}
			return nil, fmt.Errorf("reasoning_effort_models JSON 无效: %w", err)
		}
		return nil, nil
	}

	entries := make([]ReasoningEffortModel, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, entry := range raw {
		model := normalizeReasoningEffortBaseModel(entry.Model, supportedModels)
		effort := normalizeConfiguredReasoningEffort(entry.Effort, model)
		if model == "" || effort == "" {
			if strict {
				return nil, fmt.Errorf("reasoning_effort_models[%d] 需要非空 model 且 effort 必须是 none/minimal/low/medium/high/xhigh/ultra", i)
			}
			continue
		}
		alias := ReasoningEffortModelAlias(model, effort)
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, ReasoningEffortModel{Model: model, Effort: effort})
	}
	return entries, nil
}

func normalizeReasoningEffortBaseModel(model string, supportedModels []string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	canonical := canonicalizeCodexModel(model, supportedModels)
	if err := security.ValidateModelName(canonical); err != nil {
		return ""
	}
	if canonical != "" {
		return canonical
	}
	return strings.ToLower(model)
}

// normalizeConfiguredReasoningEffort 归一化设置里配置的思考强度档位。
// max 仅 gpt-5.6 起的模型放行,旧模型配置 max 会被钳到 xhigh(上游不接受)。
func normalizeConfiguredReasoningEffort(effort, model string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "ultra":
		return strings.ToLower(strings.TrimSpace(effort))
	case "max":
		if modelSupportsMaxReasoningEffort(model) {
			return "max"
		}
		return "xhigh"
	default:
		return ""
	}
}

func resolveReasoningEffortModelAlias(model string, settingsJSON string, supportedModels []string) (ReasoningEffortModel, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ReasoningEffortModel{}, false
	}
	entries, _ := parseReasoningEffortModelEntries(settingsJSON, supportedModels, false)
	for _, entry := range entries {
		if strings.EqualFold(model, ReasoningEffortModelAlias(entry.Model, entry.Effort)) {
			return entry, true
		}
	}
	return ReasoningEffortModel{}, false
}
