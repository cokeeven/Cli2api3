package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestRecordAttemptCarriesProviderPhaseAndObservedHTTPStatus(t *testing.T) {
	observedStatus := http.StatusOK
	var got accounts.RequestAttempt
	executor := ChatExecutor{OnAttempt: func(attempt accounts.RequestAttempt) { got = attempt }}
	err := &providers.Error{
		Kind: accounts.KindAuth, Status: http.StatusUnauthorized, Code: "12153",
		Message: "WorkBuddy 会话已被上游拒绝，请检查登录状态。",
		Phase:   "chat", Source: "workbuddy_chat_response", HTTPStatus: &observedStatus,
	}
	executor.recordAttempt(context.Background(), accounts.RequestAttempt{
		ID: "attempt-synthetic-auth", RequestID: "request-synthetic-auth", Status: accounts.AttemptStatusError,
	}, err)
	if got.ErrorKind != accounts.KindAuth || got.ErrorCode != "12153" {
		t.Fatalf("classification evidence=%+v", got)
	}
	if got.HTTPStatus == nil || *got.HTTPStatus != http.StatusOK {
		t.Fatalf("observed HTTP status=%v, want 200", got.HTTPStatus)
	}
	if got.ErrorPhase != "chat" || got.ErrorSource != "workbuddy_chat_response" {
		t.Fatalf("phase/source=%q/%q", got.ErrorPhase, got.ErrorSource)
	}
}
