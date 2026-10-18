package workbuddy

import (
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestKnownQuotaEnvelopePreservesRawBusinessCode(t *testing.T) {
	got := Classify(200, `{"code":14018,"msg":"insufficient credit"}`)
	if got.Code != "14018" || got.Kind != accounts.KindQuota || got.Scope != "account" || !got.RetrySafe {
		t.Fatalf("classification lost raw code or semantic meaning: %+v", got)
	}
}

func TestQuotaEnvelopeWithoutValidCodeKeepsSemanticFallback(t *testing.T) {
	for _, body := range []string{
		`{"msg":"insufficient credit"}`,
		`{"code":null,"msg":"insufficient credit"}`,
		`{"code":"","msg":"insufficient credit"}`,
		`{"code":{"unexpected":"shape"},"msg":"insufficient credit"}`,
	} {
		got := Classify(200, body)
		if got.Code != "insufficient_credits" || got.Kind != accounts.KindQuota || got.Scope != "account" || !got.RetrySafe {
			t.Fatalf("semantic fallback changed for %s: %+v", body, got)
		}
	}
}
