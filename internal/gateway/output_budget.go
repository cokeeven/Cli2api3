package gateway

import (
	"strings"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func outputBudgetEnforcement(provider string, evidence *providers.OutputBudgetEvidence, requested, completion *int, usageSource string) string {
	if !strings.EqualFold(strings.TrimSpace(provider), "trae") {
		return ""
	}
	if evidence == nil || evidence.Status == "" || evidence.Status == "not_tested" {
		return "not_tested"
	}
	if !strings.EqualFold(strings.TrimSpace(evidence.Provider), "trae") ||
		!strings.EqualFold(strings.TrimSpace(evidence.Region), "cn") ||
		strings.TrimSpace(evidence.EndpointPath) != "/api/agent/v3/llm_utils_chat" ||
		strings.TrimSpace(evidence.RequestParameter) != "max_tokens" {
		return "not_tested"
	}
	if evidence.Status != "observed_ignored" {
		return "unknown"
	}
	if requested == nil {
		return "not_requested"
	}
	if completion == nil || (!strings.EqualFold(strings.TrimSpace(usageSource), "upstream") && !strings.EqualFold(strings.TrimSpace(usageSource), "provider")) {
		return "unknown"
	}
	if evidence.TokenBasis != "completion_tokens" {
		return "non_comparable"
	}
	if *completion > *requested {
		return "observed_ignored"
	}
	return "not_exceeded"
}
