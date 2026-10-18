package executor

import "testing"

func TestLegacyEstimatedTokenLogEvidenceIsPreservedWithoutBecomingActual(t *testing.T) {
	r := ChatResult{PromptTokens: 7, CompletionTokens: 3, UsageSource: "estimate"}
	prompt, completion := r.UsageTokenEvidence()
	if prompt == nil || completion == nil || *prompt != 7 || *completion != 3 {
		t.Fatal("legacy estimated log counts were lost")
	}
	if r.HasUsageEvidence() {
		t.Fatal("estimate promoted to confirmed usage")
	}
}
