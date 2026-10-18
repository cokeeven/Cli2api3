package trae

import "testing"

func TestOutputBudgetEvidenceIsLimitedToObservedModelsAndScenes(t *testing.T) {
	cases := []struct {
		model   string
		scene   string
		basis   string
		ceiling int
	}{
		{"glm-5.3", "chat_v3", "completion_tokens", 16000},
		{"kimi-k2.6", "solo_work_lite", "completion_tokens", 0},
		{"kimi-k3", "chat_v3", "completion_tokens", 32000},
		{"minimax-m3", "chat_v3", "completion_tokens", 16000},
		{"qwen3.8-flash", "chat_v3", "content_tokens", 32000},
		{"Doubao-Seed-Code", "chat_v3", "content_tokens", 32000},
	}
	for _, test := range cases {
		t.Run(test.model, func(t *testing.T) {
			got := outputBudgetEvidence(test.model, test.scene, test.ceiling)
			if got == nil {
				t.Fatal("expected narrowly-scoped evidence")
			}
			if got.Status != "observed_ignored" || got.Provider != "trae" || got.Region != "cn" ||
				got.EndpointPath != "/api/agent/v3/llm_utils_chat" || got.Function != test.scene ||
				got.RequestParameter != "max_tokens" || got.TokenBasis != test.basis ||
				got.EvidenceDate != "2026-10-18" || got.ClientVersion != "3.4.4" ||
				got.ClientBuild != "2.3.93438" || got.AppVersion != "1.107.1" {
				t.Fatalf("evidence scope = %+v", got)
			}
			if test.ceiling == 0 && got.EffectiveCeiling != nil {
				t.Fatalf("unknown ceiling was invented: %+v", got.EffectiveCeiling)
			}
			if test.ceiling > 0 && (got.EffectiveCeiling == nil || *got.EffectiveCeiling != test.ceiling) {
				t.Fatalf("effective ceiling=%v, want %d", got.EffectiveCeiling, test.ceiling)
			}
		})
	}
	for _, test := range []struct{ model, scene string }{
		{"glm-5.3-flash", "chat_v3"}, // named in a report summary, but no case row proves it
		{"glm-5.3", "solo_work_lite"},
		{"future-model", "chat_v3"},
	} {
		if got := outputBudgetEvidence(test.model, test.scene, 64000); got != nil {
			t.Fatalf("unverified model/scene %q/%q received evidence: %+v", test.model, test.scene, got)
		}
	}
}
