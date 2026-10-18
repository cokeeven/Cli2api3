package trae

import (
	"strings"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

// outputBudgetEvidence reflects only dated, model-specific observations from
// the Trae CN llm_utils_chat evidence set. Unknown models/scenes stay unmarked.
func outputBudgetEvidence(model, scene string, effectiveCeiling int) *providers.OutputBudgetEvidence {
	model = strings.ToLower(strings.TrimSpace(model))
	scene = strings.TrimSpace(scene)
	basis := ""
	wantScene := "chat_v3"
	switch model {
	case "glm-5.3", "kimi-k3", "minimax-m3":
		basis = "completion_tokens"
	case "qwen3.8-flash", "doubao-seed-code":
		basis = "content_tokens"
	case "kimi-k2.6":
		basis = "completion_tokens"
		wantScene = "solo_work_lite"
	default:
		return nil
	}
	if scene != wantScene {
		return nil
	}
	evidence := &providers.OutputBudgetEvidence{
		Status: "observed_ignored", Provider: "trae", Region: "cn",
		EndpointPath: "/api/agent/v3/llm_utils_chat", Function: scene,
		RequestParameter: "max_tokens", TokenBasis: basis,
		EvidenceDate: "2026-10-18", EvidenceSource: "20261018-trae-budget-agent-analysis",
		ClientVersion: "3.4.4", ClientBuild: "2.3.93438", AppVersion: "1.107.1",
	}
	if effectiveCeiling > 0 {
		value := effectiveCeiling
		evidence.EffectiveCeiling = &value
	}
	return evidence
}
