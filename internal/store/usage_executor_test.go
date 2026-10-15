package store

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type usageTestChat struct{}

func (usageTestChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	credit := 2.5
	return providers.ChatOutcome{Content: "ok", UsageSource: "upstream", PromptTokens: 2, CompletionTokens: 1, Credits: &credit}, nil
}
func (usageTestChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, providers.ResolvedChat{}, nil
}

// This deliberately uses synthetic accounts and an in-memory provider. The
// executor, SQLite admission and finalization paths are the production ones.
func TestUsageExecutorSkipsGuardedAccountAndPersistsFinalStreamUsage(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, accounts.CreateAccount{Name: "second", Provider: "workbuddy", Region: "cn", MaxInFlight: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUsagePolicy(ctx, b.ID, accounts.UsagePolicy{DailyTokens: 10000}); err != nil {
		t.Fatal(err)
	}
	pool := executor.NewPool(nil, nil)
	for _, account := range []accounts.Account{a, b} {
		pool.Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process", MaxInFlight: 1, Models: []string{"kimi-k3"}})
	}
	e := executor.NewChatExecutor(pool, "")
	e.Budgets = s
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: usageTestChat{}})
	e.OnAttempt = func(attempt accounts.RequestAttempt) {
		if err := s.InsertRequestAttempt(ctx, attempt); err != nil {
			t.Error(err)
		}
	}
	if err = s.InsertRequestLog(ctx, accounts.RequestLog{ID: "nonstream", RequestedModel: "kimi-k3"}); err != nil {
		t.Fatal(err)
	}
	requestCtx := executor.WithRoutingPolicy(executor.WithRequestID(ctx, "nonstream"), "key-test", accounts.KeyRoutingPolicy{})
	result, err := e.ChatNonStream(requestCtx, translate.ChatRequest{Model: "kimi-k3", MaxTokens: []byte("5")}, a.ID, "workbuddy")
	if err != nil || result.AccountID != b.ID || result.AttemptCount != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err = s.InsertRequestLog(ctx, accounts.RequestLog{ID: "stream", RequestedModel: "kimi-k3", Stream: true}); err != nil {
		t.Fatal(err)
	}
	requestCtx = executor.WithRoutingPolicy(executor.WithRequestID(ctx, "stream"), "key-test", accounts.KeyRoutingPolicy{})
	stream, err := e.ChatStreamProxy(requestCtx, translate.ChatRequest{Model: "kimi-k3", MaxTokens: []byte("5"), Stream: true}, a.ID, "workbuddy")
	if err != nil || stream.AccountID != b.ID || stream.AttemptCount != 1 {
		t.Fatalf("stream=%+v err=%v", stream, err)
	}
	view, err := s.GetUsageAnalysis(ctx, b.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.Today.TokensActual != 3 || view.Today.TokensReserved == 0 {
		t.Fatalf("stream settled before completion: %+v", view.Today)
	}
	prompt, completion := 2, 1
	credit := 2.5
	stream.Finalize(executor.StreamCompletion{PromptTokens: &prompt, CompletionTokens: &completion, Credits: &credit, UsageSource: "upstream", Completed: true})
	stream.Response.Body.Close()
	stream.Response.Body.Close()
	view, err = s.GetUsageAnalysis(ctx, b.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.Today.TokensActual != 6 || view.Today.CreditsActual != 5 || view.Today.TokensReserved != 0 || view.Today.CreditsReserved != 0 {
		t.Fatalf("final usage=%+v", view.Today)
	}
	var linked int
	if err = s.DB().QueryRow(`SELECT COUNT(*) FROM usage_reservations u JOIN request_attempts a ON a.id=u.id WHERE u.account_id=? AND u.key_id='key-test'`, b.ID).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("linked attempts=%d err=%v", linked, err)
	}
	for _, item := range pool.Items() {
		if item.LiveInFlight != 0 {
			t.Fatal("finalization leaked concurrency slot")
		}
	}
}
