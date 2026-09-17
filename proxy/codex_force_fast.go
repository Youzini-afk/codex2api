package proxy

import "github.com/tidwall/sjson"

// applyCodexForceFast 仅由 Codex 生成执行器调用，避免跨渠道改写 tier。
// 使用 priority（Fast 的上游值），并覆盖客户端、别名和 payload 规则的档位。
func applyCodexForceFast(body []byte) []byte {
	if !CurrentRuntimeSettings().CodexForceFastEnabled {
		return body
	}
	body, _ = sjson.DeleteBytes(body, "serviceTier")
	body, _ = sjson.SetBytes(body, "service_tier", "priority")
	return body
}
