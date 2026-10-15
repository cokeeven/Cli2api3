package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type lifecycleBudget struct {
	mu         sync.Mutex
	reserved   []accounts.UsageReservation
	dispatched []string
	settled    []accounts.UsageSettlement
}

func (b *lifecycleBudget) ReserveUsage(_ context.Context, r accounts.UsageReservation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved = append(b.reserved, r)
	return nil
}
func (b *lifecycleBudget) MarkUsageDispatched(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dispatched = append(b.dispatched, id)
	return nil
}
func (b *lifecycleBudget) SettleUsage(_ context.Context, s accounts.UsageSettlement) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settled = append(b.settled, s)
	return nil
}
func (b *lifecycleBudget) RecoverUsageReservations(context.Context) error { return nil }

type lifecycleChat struct {
	body   io.ReadCloser
	opened chan struct{}
}

func (c lifecycleChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	return providers.ChatOutcome{}, errors.New("unexpected non-stream")
}
func (c lifecycleChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	if c.opened != nil {
		close(c.opened)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: c.body}, providers.ResolvedChat{}, nil
}

func TestPublicStreamBudgetSettlesExactlyOnceOnSuccessTruncationAndCancellation(t *testing.T) {
	protocols := []struct {
		path, body string
		serve      func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"/v1/chat/completions", `{"model":"workbuddy/lifecycle-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":true}`, (*Handler).HandleChatCompletions},
		{"/v1/messages", `{"model":"workbuddy/lifecycle-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":true}`, (*Handler).HandleAnthropicMessages},
		{"/v1/responses", `{"model":"workbuddy/lifecycle-model","input":"hello","max_output_tokens":32,"stream":true}`, (*Handler).HandleResponses},
	}
	for _, protocol := range protocols {
		for _, outcome := range []string{"complete", "truncated", "canceled"} {
			t.Run(protocol.path+"/"+outcome, func(t *testing.T) {
				body := io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"credits\":2.5}}\n\ndata: [DONE]\n\n"))
				if outcome == "truncated" {
					body = io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
				}
				var pipeWriter *io.PipeWriter
				if outcome == "canceled" {
					var reader *io.PipeReader
					reader, pipeWriter = io.Pipe()
					body = reader
					defer pipeWriter.Close()
				}
				defer body.Close()
				opened := make(chan struct{})
				pool := executor.NewPool(nil, nil)
				pool.Upsert(executor.Item{ID: "account", Provider: "workbuddy", Region: "cn", Runtime: "inprocess", MaxInFlight: 1, Models: []string{"lifecycle-model"}})
				e := executor.NewChatExecutor(pool, "")
				e.Providers = providers.NewRegistry()
				e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: lifecycleChat{body: body, opened: opened}})
				budgets := &lifecycleBudget{}
				e.Budgets = budgets
				var mu sync.Mutex
				var attempts []accounts.RequestAttempt
				e.OnAttempt = func(a accounts.RequestAttempt) { mu.Lock(); defer mu.Unlock(); attempts = append(attempts, a) }
				h := &Handler{Executor: e, Pool: pool}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = auth.WithIdentity(ctx, auth.Identity{Kind: auth.KindKey, KeyID: "key", AllowedProviders: []string{"workbuddy:cn"}})
				r := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.body)).WithContext(ctx)
				done := make(chan any, 1)
				go func() {
					var panicValue any
					defer func() {
						if p := recover(); p != nil {
							panicValue = p
						}
						done <- panicValue
					}()
					protocol.serve(h, httptest.NewRecorder(), r)
				}()
				select {
				case <-opened:
				case <-time.After(5 * time.Second):
					t.Fatal("adapter did not open")
				}
				if outcome == "canceled" {
					cancel()
				}
				select {
				case p := <-done:
					if p != nil && p != http.ErrAbortHandler {
						t.Fatalf("unexpected panic: %v", p)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("handler did not finish")
				}
				// Cancel completion may be handled by the executor's callback rather
				// than the relay; wait for that one settlement, without reading live state.
				deadline := time.Now().Add(5 * time.Second)
				for {
					budgets.mu.Lock()
					n := len(budgets.settled)
					budgets.mu.Unlock()
					item, _ := pool.ByID("account")
					if n > 0 && item.LiveInFlight == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("budget lease leaked")
					}
					time.Sleep(time.Millisecond)
				}
				budgets.mu.Lock()
				defer budgets.mu.Unlock()
				if len(budgets.reserved) != 1 || len(budgets.dispatched) != 1 || len(budgets.settled) != 1 {
					t.Fatalf("budget lifecycle reserved=%d dispatched=%d settled=%d", len(budgets.reserved), len(budgets.dispatched), len(budgets.settled))
				}
				s := budgets.settled[0]
				if s.ID != budgets.reserved[0].ID || s.NonBillable {
					t.Fatalf("incorrect settlement: %+v", s)
				}
				if outcome == "complete" {
					if s.ActualTokens == nil || *s.ActualTokens != 4 || s.ActualCredits == nil || *s.ActualCredits != 2.5 {
						t.Fatalf("actual usage lost: %+v", s)
					}
				} else if s.ActualTokens != nil || s.ActualCredits != nil {
					t.Fatalf("unreported usage fabricated: %+v", s)
				}
				item, _ := pool.ByID("account")
				if item.LiveInFlight != 0 || len(pool.PlatformInFlight()) != 0 {
					t.Fatalf("capacity leaked: %+v", pool.PlatformInFlight())
				}
				if outcome == "canceled" && (!item.DownUntil.IsZero() || len(item.ModelDownUntil) > 0) {
					t.Fatal("client cancellation cooled account")
				}
				mu.Lock()
				defer mu.Unlock()
				if len(attempts) != 1 {
					t.Fatalf("terminal attempts=%d", len(attempts))
				}
				if (attempts[0].Status == accounts.AttemptStatusOK) != (outcome == "complete") {
					t.Fatalf("wrong terminal attempt: %+v", attempts[0])
				}
			})
		}
	}
}
