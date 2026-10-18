package workbuddy

import (
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestUnknownHTTP200BusinessCodeIsPreservedAsUnknown(t *testing.T) {
	got := Classify(200, `{"code":9057,"msg":"synthetic unrecognized business response"}`)
	if got.Kind != accounts.KindUnavailable || got.Status != 502 {
		t.Fatalf("unknown business envelope classification = %+v", got)
	}
	if got.Code != "9057" {
		t.Fatalf("business code = %q, want preserved upstream code 9057", got.Code)
	}
	if got.Scope != "" || got.RetrySafe {
		t.Fatalf("unknown code must retain unknown scope/retry safety, got %+v", got)
	}
}
