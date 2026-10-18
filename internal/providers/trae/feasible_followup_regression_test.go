package trae

import (
	"errors"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestWrapClassifiedPreservesRetryScopeAndSafety(t *testing.T) {
	classified := providers.ClassifiedError{
		Kind: accounts.KindRateLimit, Status: 429, Message: "synthetic limit",
		Code: "3004", Scope: "model", RetrySafe: true,
	}
	err := wrapClassified(classified, classified.Code)
	var got *providers.Error
	if !errors.As(err, &got) {
		t.Fatalf("wrapped error type = %T", err)
	}
	if got.Scope != classified.Scope || got.RetrySafe != classified.RetrySafe || got.Code != classified.Code || got.Kind != classified.Kind {
		t.Fatalf("wrapped error lost classification fields: got %+v want %+v", got, classified)
	}
}
