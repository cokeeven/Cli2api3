package gateway

import (
	"context"
	"fmt"
	"io"
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
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type routingFailureChat struct{ seen []string }

func (c *routingFailureChat) reject(id string) error {
	c.seen = append(c.seen, id)
	if len(c.seen) <= 4 {
		return &providers.Error{Kind: accounts.KindAuth, Status: 401, Scope: "account", RetrySafe: true, Message: "synthetic authentication rejected"}
	}
	return nil
}
func (c *routingFailureChat) ChatNonStream(_ context.Context, id string, _ translate.ChatRequest) (providers.ChatOutcome, error) {
	if err := c.reject(id); err != nil {
		return providers.ChatOutcome{}, err
	}
	return providers.ChatOutcome{Content: "healthy fifth", FinishReason: "stop", PromptTokens: 2, CompletionTokens: 3, UsageSource: "upstream"}, nil
}
func (c *routingFailureChat) ChatStream(_ context.Context, id string, _ translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	if err := c.reject(id); err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"healthy fifth\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"))}, providers.ResolvedChat{}, nil
}

func TestRoutingFifthCandidateHTTPLogsAndExactlyOnceSettlement(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "routing.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			recorder := applogs.NewRequestRecorder(db)
			pool := executor.NewPool(nil, nil)
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				pool.Upsert(executor.Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "inprocess", Models: []string{"m"}, MaxInFlight: 1})
			}
			adapter := &routingFailureChat{}
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: adapter})
			e.OnAttempt = recorder.Attempt
			e.OnRoute = recorder.RoutingDiagnostic
			budget := &lifecycleBudget{}
			e.Budgets = budget
			h := &Handler{Executor: e, Pool: pool, Recorder: recorder}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "synthetic", AllowedProviders: []string{"workbuddy:cn"}})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"workbuddy/m","stream":%t,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`, stream))).WithContext(ctx)
			w := httptest.NewRecorder()
			h.HandleChatCompletions(w, r)
			recorder.Close()
			log, err := db.GetRequestLog(context.Background(), w.Header().Get("X-Request-ID"))
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), "healthy fifth") || log.Status != accounts.RequestStatusOK || log.AttemptCount != 5 || len(log.Attempts) != 5 {
				t.Fatalf("HTTP/log mismatch: code=%d log=%+v body=%s", w.Code, log, w.Body.String())
			}
			if log.RoutingDiagnostic == nil || log.RoutingDiagnostic.InitialCandidates != 5 || log.RoutingDiagnostic.Attempted != 5 || log.RoutingDiagnostic.LastUpstreamKind != accounts.KindAuth {
				t.Fatalf("route diagnostic=%+v", log.RoutingDiagnostic)
			}
			for i, a := range log.Attempts {
				expected := accounts.AttemptStatusFailover
				if i == 4 {
					expected = accounts.AttemptStatusOK
				}
				if a.Status != expected {
					t.Fatalf("attempt[%d]=%+v", i, a)
				}
			}
			if len(budget.reserved) != 5 || len(budget.settled) != 5 {
				t.Fatalf("reservation=%d settlement=%d", len(budget.reserved), len(budget.settled))
			}
			seen := map[string]bool{}
			for _, a := range budget.settled {
				if seen[a.ID] {
					t.Fatal("settled twice")
				}
				seen[a.ID] = true
			}
			for _, item := range pool.Items() {
				if item.LiveInFlight != 0 {
					t.Fatal("slot leaked")
				}
			}
		})
	}
}
