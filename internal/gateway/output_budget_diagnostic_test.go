package gateway

import (
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestOutputBudgetEnforcementRequiresComparableObservedUsage(t *testing.T) {
	limit, exceeded, within := 64, 16000, 64
	evidence := func(basis string) *providers.OutputBudgetEvidence {
		return &providers.OutputBudgetEvidence{
			Status: "observed_ignored", Provider: "trae", Region: "cn",
			EndpointPath: "/api/agent/v3/llm_utils_chat", RequestParameter: "max_tokens", TokenBasis: basis,
		}
	}
	completionEvidence := evidence("completion_tokens")
	contentEvidence := evidence("content_tokens")
	tests := []struct {
		name, provider, want string
		evidence             *providers.OutputBudgetEvidence
		limit, completion    *int
		usageSource          string
	}{
		{"exceeded comparable usage", "trae", "observed_ignored", completionEvidence, &limit, &exceeded, "upstream"},
		{"equal budget is only not exceeded", "trae", "not_exceeded", completionEvidence, &limit, &within, "upstream"},
		{"reasoning-inclusive count is not comparable to content basis", "trae", "non_comparable", contentEvidence, &limit, &exceeded, "upstream"},
		{"missing usage stays unknown", "trae", "unknown", completionEvidence, &limit, nil, "unknown"},
		{"estimated usage stays unknown", "trae", "unknown", completionEvidence, &limit, &exceeded, "estimate"},
		{"unverified model stays untested", "trae", "not_tested", nil, &limit, &exceeded, "upstream"},
		{"evidence for another endpoint stays untested", "trae", "not_tested", &providers.OutputBudgetEvidence{Status: "observed_ignored", Provider: "trae", Region: "cn", EndpointPath: "/api/other", RequestParameter: "max_tokens", TokenBasis: "completion_tokens"}, &limit, &exceeded, "upstream"},
		{"non Trae provider is not evaluated", "workbuddy", "", completionEvidence, &limit, &exceeded, "upstream"},
		{"no caller budget", "trae", "not_requested", completionEvidence, nil, &exceeded, "upstream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outputBudgetEnforcement(test.provider, test.evidence, test.limit, test.completion, test.usageSource); got != test.want {
				t.Fatalf("enforcement=%q, want %q", got, test.want)
			}
		})
	}
}
