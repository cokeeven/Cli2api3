package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	applogs "github.com/caigee-cmd/cli2api/internal/logs"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/providers/workbuddy"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type workBuddyBusinessCodeChat struct{}

func (workBuddyBusinessCodeChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	upstream := workbuddy.Classify(http.StatusOK, `{"code":14018,"msg":"insufficient credit"}`)
	return providers.ChatOutcome{}, &providers.Error{
		Kind: upstream.Kind, Status: upstream.Status, Message: upstream.Message,
		Scope: upstream.Scope, RetrySafe: upstream.RetrySafe, Code: upstream.Code,
	}
}

func (workBuddyBusinessCodeChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, providers.ErrUnsupported
}

func TestWorkBuddyRawBusinessCodeReachesRequestAndAttemptLogs(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "business-code.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := applogs.NewRequestRecorder(db)
	pool := executor.NewPool(nil, nil)
	pool.Upsert(executor.Item{ID: "synthetic-workbuddy", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Chat: workBuddyBusinessCodeChat{}})
	e := executor.NewChatExecutor(pool, "")
	e.Providers = registry
	e.OnAttempt = recorder.Attempt
	e.OnRoute = recorder.RoutingDiagnostic
	h := &Handler{Executor: e, Pool: pool, Recorder: recorder}
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "synthetic-key", AllowedProviders: []string{"workbuddy:cn"}})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"workbuddy/m","messages":[{"role":"user","content":"synthetic"}]}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleChatCompletions(w, r)
	recorder.Close()

	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("gateway did not expose the synthetic request id")
	}
	request, err := db.GetRequestLog(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusTooManyRequests || request.Status != accounts.RequestStatusError || request.ErrorKind != accounts.KindQuota || request.ErrorCode != "route_candidates_exhausted" {
		t.Fatalf("HTTP/request log did not preserve final route semantics: http=%d request=%+v body=%s", w.Code, request, w.Body.String())
	}
	if request.AttemptCount != 1 || len(request.Attempts) != 1 || request.Attempts[0].ErrorCode != "14018" || request.Attempts[0].ErrorKind != accounts.KindQuota || request.Attempts[0].ErrorScope != "account" {
		t.Fatalf("attempt log did not preserve classified upstream code: %+v", request.Attempts)
	}
	if request.Attempts[0].RequestID != requestID {
		t.Fatalf("attempt request id %q does not match request %q", request.Attempts[0].RequestID, requestID)
	}
	if request.RoutingDiagnostic == nil || request.RoutingDiagnostic.LastUpstreamCode != "14018" {
		t.Fatalf("routing diagnostic did not preserve raw code: %+v", request.RoutingDiagnostic)
	}
	if request.RoutingDiagnostic.FinalSelectionCode != "route_candidates_exhausted" {
		t.Fatalf("routing diagnostic lost the final route stop code: %+v", request.RoutingDiagnostic)
	}
	for _, item := range pool.Items() {
		if item.LiveInFlight != 0 {
			t.Fatalf("capacity leaked after quota rejection: %+v", item)
		}
	}
}
