package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type queuedBudgetFailoverChat struct{ calls []string }

func (f *queuedBudgetFailoverChat) ChatNonStream(_ context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	f.calls = append(f.calls, accountID)
	if accountID == "account-a" {
		return providers.ChatOutcome{}, &providers.Error{
			Kind: accounts.KindAuth, Status: http.StatusUnauthorized, Scope: "account",
			RetrySafe: true, Code: "synthetic_auth_rejected", Message: "synthetic account rejection",
		}
	}
	return providers.ChatOutcome{Model: req.Model, Content: "success from account-b", FinishReason: "stop"}, nil
}

func (f *queuedBudgetFailoverChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, providers.ErrUnsupported
}

func TestQueuedRequestRetainsEligibleFailoverCandidates(t *testing.T) {
	p := NewPool(nil, nil)
	for _, id := range []string{"account-a", "account-b"} {
		p.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	}
	query := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}
	var releaseA, releaseB func()
	for _, held := range []struct {
		id      string
		release *func()
	}{{"account-a", &releaseA}, {"account-b", &releaseB}} {
		q := query
		q.PreferAccount = held.id
		_, release, ok := p.acquireRoute(q, routingIdentity{})
		if !ok {
			t.Fatalf("failed to hold synthetic capacity for %s", held.id)
		}
		*held.release = release
	}
	defer releaseA()
	defer releaseB()

	provider := &queuedBudgetFailoverChat{}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Chat: provider})
	e := NewChatExecutor(p, "")
	e.Providers = registry
	e.MaxAttempts = 4
	e.CapacityQueueWait = time.Second
	requestID := "synthetic-queued-failover-budget"
	ctx := WithRequestID(context.Background(), requestID)
	var attempts []accounts.RequestAttempt
	e.OnAttempt = func(attempt accounts.RequestAttempt) { attempts = append(attempts, attempt) }
	var route accounts.RequestRoutingDiagnostic
	e.OnRoute = func(d accounts.RequestRoutingDiagnostic) { route = d }

	type result struct {
		value ChatResult
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := e.ChatNonStream(ctx, translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}}, "account-a", "workbuddy")
		done <- result{value: value, err: err}
	}()
	waitForCapacityWaiters(t, p, 1)
	for _, id := range []string{"account-a", "account-b"} {
		item, ok := p.ByID(id)
		if !ok || item.LiveInFlight != 1 {
			t.Fatalf("expected held slot for %s: %+v", id, item)
		}
	}
	releaseA()
	releaseB()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("executor result: %v", got.err)
		}
		if got.value.Content != "success from account-b" || got.value.AccountID != "account-b" || got.value.AttemptCount != 2 {
			t.Fatalf("unexpected final result: %+v", got.value)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("queued request did not complete after capacity release")
	}
	if len(provider.calls) != 2 || provider.calls[0] != "account-a" || provider.calls[1] != "account-b" {
		t.Fatalf("provider call order = %v, want [account-a account-b]", provider.calls)
	}
	if route.InitialCandidates != 2 || route.AttemptBudget < 2 || route.Attempted != 2 {
		t.Fatalf("route budget lost initially saturated candidate: %+v", route)
	}
	if len(attempts) != 2 || attempts[0].Status != accounts.AttemptStatusFailover || attempts[0].ErrorCode != "synthetic_auth_rejected" || attempts[1].Status != accounts.AttemptStatusOK {
		t.Fatalf("attempt log mismatch: %+v", attempts)
	}
	for _, item := range p.Items() {
		if item.LiveInFlight != 0 {
			t.Fatalf("capacity lease leaked for %s: %+v", item.ID, item)
		}
	}
	if p.capacityWaiterCount() != 0 {
		t.Fatalf("capacity queue leaked waiters: %d", p.capacityWaiterCount())
	}
}
