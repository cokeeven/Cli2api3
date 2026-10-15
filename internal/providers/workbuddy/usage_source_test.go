package workbuddy

import (
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"testing"
)

func TestMissingUsageIsUnknownInsteadOfActualZero(t *testing.T) {
	for _, usage := range []any{nil, map[string]any{}, map[string]any{"prompt_tokens": 5}} {
		out, err := outcomeFromAggregate(map[string]any{"usage": usage, "choices": []any{}})
		if err != nil || out.UsageSource != "unknown" {
			t.Fatalf("usage=%v outcome=%+v err=%v", usage, out, err)
		}
	}
	out, err := outcomeFromAggregate(map[string]any{"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0}, "choices": []any{}})
	if err != nil || out.UsageSource != "upstream" {
		t.Fatalf("explicit upstream zeros must remain actual: %+v %v", out, err)
	}
}

func TestModelLimitAndCreditRejectionCarrySafeScope(t *testing.T) {
	for _, status := range []int{200, 400, 429} {
		got := Classify(status, `{"code":6004,"msg":"model usage is exhausted"}`)
		if got.Kind != accounts.KindRateLimit || got.Scope != "model" || !got.RetrySafe || got.Code != "6004" {
			t.Fatalf("status=%d got=%+v", status, got)
		}
	}
	got := Classify(402, `{"msg":"insufficient credits"}`)
	if got.Kind != accounts.KindQuota || got.Scope != "account" || !got.RetrySafe {
		t.Fatalf("balance=%+v", got)
	}
	got = Classify(402, `{"msg":"payment required"}`)
	if got.RetrySafe || got.Scope != "" {
		t.Fatalf("bare 402 falsely permits retry: %+v", got)
	}
}
