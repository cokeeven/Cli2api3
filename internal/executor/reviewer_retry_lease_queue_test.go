package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type retryLeaseBudget struct {
	mu           sync.Mutex
	reservations []accounts.UsageReservation
	dispatched   map[string]int
	settlements  map[string][]accounts.UsageSettlement
}

func (b *retryLeaseBudget) ReserveUsage(_ context.Context, reservation accounts.UsageReservation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, existing := range b.reservations {
		if existing.ID == reservation.ID {
			return errors.New("duplicate synthetic reservation")
		}
	}
	b.reservations = append(b.reservations, reservation)
	return nil
}

func (b *retryLeaseBudget) MarkUsageDispatched(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dispatched == nil {
		b.dispatched = map[string]int{}
	}
	b.dispatched[id]++
	return nil
}

func (b *retryLeaseBudget) SettleUsage(_ context.Context, settlement accounts.UsageSettlement) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settlements == nil {
		b.settlements = map[string][]accounts.UsageSettlement{}
	}
	b.settlements[settlement.ID] = append(b.settlements[settlement.ID], settlement)
	return nil
}

func (*retryLeaseBudget) RecoverUsageReservations(context.Context) error { return nil }

func (b *retryLeaseBudget) assertBalanced(t *testing.T) []accounts.UsageReservation {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.reservations) != len(b.settlements) {
		t.Fatalf("reservation/settlement count mismatch: reservations=%d settlements=%d", len(b.reservations), len(b.settlements))
	}
	for _, reservation := range b.reservations {
		if b.dispatched[reservation.ID] != 1 {
			t.Errorf("reservation %s dispatched %d times", reservation.ID, b.dispatched[reservation.ID])
		}
		settlements := b.settlements[reservation.ID]
		if len(settlements) != 1 {
			t.Errorf("reservation %s settled %d times", reservation.ID, len(settlements))
		}
	}
	return append([]accounts.UsageReservation(nil), b.reservations...)
}

func (b *retryLeaseBudget) settlement(id string) (accounts.UsageSettlement, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	settlements := b.settlements[id]
	if len(settlements) == 0 {
		return accounts.UsageSettlement{}, 0
	}
	return settlements[0], len(settlements)
}

type retryLeaseProbeChat struct {
	firstStarted chan struct{}
	allowFailure chan struct{}
	startOnce    sync.Once
	failOnce     sync.Once
	mu           sync.Mutex
	calls        []string
}

func newRetryLeaseProbeChat() *retryLeaseProbeChat {
	return &retryLeaseProbeChat{firstStarted: make(chan struct{}), allowFailure: make(chan struct{})}
}

func (p *retryLeaseProbeChat) record(accountID string) {
	p.mu.Lock()
	p.calls = append(p.calls, accountID)
	p.mu.Unlock()
}

func (p *retryLeaseProbeChat) callOrder() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *retryLeaseProbeChat) releaseFirstFailure() {
	p.failOnce.Do(func() { close(p.allowFailure) })
}

func retrySafeSyntheticFailure() error {
	return &providers.Error{
		Kind: accounts.KindUnavailable, Status: http.StatusBadGateway, Scope: "transient",
		RetrySafe: true, Code: "upstream_empty", Message: "synthetic empty upstream result",
	}
}

func (p *retryLeaseProbeChat) failFirst(ctx context.Context) error {
	p.startOnce.Do(func() { close(p.firstStarted) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.allowFailure:
		return retrySafeSyntheticFailure()
	}
}

func (p *retryLeaseProbeChat) ChatNonStream(ctx context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	p.record(accountID)
	if accountID == "account-a" {
		return providers.ChatOutcome{}, p.failFirst(ctx)
	}
	return providers.ChatOutcome{Model: req.Model, Content: "synthetic success", PromptTokens: 2, CompletionTokens: 3, UsageSource: "provider", FinishReason: "stop"}, nil
}

func (p *retryLeaseProbeChat) ChatStream(ctx context.Context, accountID string, req translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	p.record(accountID)
	if accountID == "account-a" {
		return nil, providers.ResolvedChat{}, p.failFirst(ctx)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("synthetic stream")),
	}, providers.ResolvedChat{}, nil
}

func TestRetryReleasesPriorLeaseBeforeFairQueueForNonStreamAndStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "nonstream"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) { runRetryLeaseSuccessScenario(t, streaming) })
	}
}

func runRetryLeaseSuccessScenario(t *testing.T, streaming bool) {
	t.Helper()
	p := NewPool(nil, nil)
	for _, id := range []string{"account-a", "account-b"} {
		p.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	}
	query := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m", PreferAccount: "account-b"}
	_, releaseB, ok := p.acquireRoute(query, routingIdentity{})
	if !ok {
		t.Fatal("failed to hold synthetic account-b slot")
	}
	var releaseBOnce sync.Once
	releaseHeldB := func() { releaseBOnce.Do(releaseB) }

	provider := newRetryLeaseProbeChat()
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Chat: provider})
	budget := &retryLeaseBudget{}
	e := NewChatExecutor(p, "")
	e.Providers = registry
	e.Budgets = budget
	e.MaxAttempts = 3
	e.RequestTimeout = 5 * time.Second
	e.CapacityQueueWait = 2 * time.Second
	request := translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}}
	requestID := "synthetic-r4-x"
	ctxX, cancelX := context.WithCancel(WithRequestID(context.Background(), requestID))
	ctxOlder, cancelOlder := context.WithCancel(context.Background())
	olderLoop := e.newRouteLoop(ctxOlder, "account-a", "workbuddy", request)
	older := &olderLoop
	older.requestID = "synthetic-r4-older"

	var workers sync.WaitGroup
	olderDone := make(chan struct {
		item  Item
		index int
		err   error
	}, 1)
	type chatResult struct {
		account string
		content string
		body    string
		err     error
	}
	chatDone := make(chan chatResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		if !streaming {
			value, err := e.ChatNonStream(ctxX, request, "account-a", "workbuddy")
			chatDone <- chatResult{account: value.AccountID, content: value.Content, err: err}
			return
		}
		value, err := e.ChatStreamProxy(ctxX, request, "account-a", "workbuddy")
		if err != nil {
			chatDone <- chatResult{err: err}
			return
		}
		body, readErr := io.ReadAll(value.Response.Body)
		completion := StreamCompletion{PromptTokens: ptrInt(2), CompletionTokens: ptrInt(3), UsageSource: "provider", Completed: readErr == nil, Err: readErr}
		value.Finalize(completion)
		chatDone <- chatResult{account: value.AccountID, body: string(body), err: readErr}
	}()
	t.Cleanup(func() {
		cancelX()
		cancelOlder()
		provider.releaseFirstFailure()
		releaseHeldB()
		workers.Wait()
		older.finishAttempt(e, nil, nil, false)
	})

	select {
	case <-provider.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first synthetic provider attempt did not start")
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		item, index, err := older.pickNextQueued(e, "m")
		olderDone <- struct {
			item  Item
			index int
			err   error
		}{item: item, index: index, err: err}
	}()
	waitForCapacityWaiters(t, p, 1)
	provider.releaseFirstFailure()

	var olderResult struct {
		item  Item
		index int
		err   error
	}
	select {
	case olderResult = <-olderDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("older same-route waiter could not use account-a while the retry waited; retry still held its prior lease")
	}
	if olderResult.err != nil || olderResult.item.ID != "account-a" {
		t.Fatalf("older waiter admission = item %q error %v, want account-a", olderResult.item.ID, olderResult.err)
	}

	var xPrior accounts.UsageReservation
	for _, reservation := range budget.assertBalancedPrefix(t, 1) {
		if reservation.RequestID == requestID && reservation.AccountID == "account-a" {
			xPrior = reservation
		}
	}
	if xPrior.ID == "" {
		t.Fatal("retry's first usage reservation is missing before its queue wait")
	}
	priorSettlement, settleCount := budget.settlement(xPrior.ID)
	if settleCount != 1 || !priorSettlement.NonBillable || priorSettlement.ActualTokens != nil {
		t.Fatalf("retry's failed attempt settlement = %+v count=%d; want one retry-safe non-billable settlement without invented usage", priorSettlement, settleCount)
	}

	older.finishAttempt(e, nil, nil, false)
	waitForCapacityWaiters(t, p, 1)
	releaseHeldB()

	var completed chatResult
	select {
	case completed = <-chatDone:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not complete on account-b after capacity was released")
	}
	if completed.err != nil || completed.account != "account-b" {
		t.Fatalf("retry result account=%q error=%v", completed.account, completed.err)
	}
	if streaming && completed.body != "synthetic stream" {
		t.Fatalf("stream body=%q", completed.body)
	}
	if !streaming && completed.content != "synthetic success" {
		t.Fatalf("nonstream content=%q", completed.content)
	}
	if calls := provider.callOrder(); len(calls) != 2 || calls[0] != "account-a" || calls[1] != "account-b" {
		t.Fatalf("provider calls=%v, want one attempt on each account", calls)
	}

	reservations := budget.assertBalanced(t)
	if len(reservations) != 3 {
		t.Fatalf("reservation count=%d, want prior X, older waiter, and retry on B", len(reservations))
	}
	var xRetry accounts.UsageReservation
	for _, reservation := range reservations {
		if reservation.RequestID == requestID && reservation.AccountID == "account-b" {
			xRetry = reservation
		}
	}
	if xRetry.ID == "" {
		t.Fatal("account-b retry reservation is missing")
	}
	retrySettlement, settleCount := budget.settlement(xRetry.ID)
	if settleCount != 1 || retrySettlement.ActualTokens == nil || *retrySettlement.ActualTokens != 5 || retrySettlement.UsageSource != "provider" {
		t.Fatalf("successful retry usage settlement=%+v count=%d", retrySettlement, settleCount)
	}
	if p.capacityWaiterCount() != 0 {
		t.Fatalf("capacity waiters leaked: %d", p.capacityWaiterCount())
	}
	for _, item := range p.Items() {
		if item.LiveInFlight != 0 || item.InFlight != 0 {
			t.Fatalf("capacity lease leaked for %s: %+v", item.ID, item)
		}
	}
}

func (b *retryLeaseBudget) assertBalancedPrefix(t *testing.T, wantSettled int) []accounts.UsageReservation {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	settled := 0
	for _, values := range b.settlements {
		settled += len(values)
	}
	if settled != wantSettled {
		t.Fatalf("settled reservation count before reattempt admission=%d, want %d", settled, wantSettled)
	}
	return append([]accounts.UsageReservation(nil), b.reservations...)
}

func TestRetryLeaseQueueCancellationShutdownAndTimeoutSettleOnce(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown", "timeout"} {
		t.Run(mode, func(t *testing.T) { runRetryLeaseQueueExitScenario(t, mode) })
	}
}

func runRetryLeaseQueueExitScenario(t *testing.T, mode string) {
	t.Helper()
	p := NewPool(nil, nil)
	for _, id := range []string{"account-a", "account-b"} {
		p.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	}
	query := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m", PreferAccount: "account-b"}
	_, releaseB, ok := p.acquireRoute(query, routingIdentity{})
	if !ok {
		t.Fatal("failed to hold synthetic account-b slot")
	}
	var releaseBOnce sync.Once
	releaseHeldB := func() { releaseBOnce.Do(releaseB) }
	budget := &retryLeaseBudget{}
	e := NewChatExecutor(p, "")
	e.Budgets = budget
	e.CapacityQueueWait = time.Second
	if mode == "timeout" {
		e.CapacityQueueWait = 60 * time.Millisecond
	}
	activeCtx, cancelActive := context.WithCancel(context.Background())
	olderCtx, cancelOlder := context.WithCancel(context.Background())
	active := newSaturatedRouteLoop(activeCtx, e)
	active.requestID = "synthetic-r4-active-" + mode
	older := newSaturatedRouteLoop(olderCtx, e)
	older.requestID = "synthetic-r4-older-" + mode

	item, _, err := active.pickNextQueued(e, "m")
	if err != nil || item.ID != "account-a" {
		t.Fatalf("initial active admission item=%q error=%v", item.ID, err)
	}
	active.lastErr = retrySafeSyntheticFailure()
	active.exclude(item)

	var workers sync.WaitGroup
	olderDone := make(chan struct {
		item Item
		err  error
	}, 1)
	retryDone := make(chan error, 1)
	workers.Add(2)
	go func() {
		defer workers.Done()
		item, _, err := older.pickNextQueued(e, "m")
		olderDone <- struct {
			item Item
			err  error
		}{item: item, err: err}
	}()
	waitForCapacityWaiters(t, p, 1)
	go func() {
		defer workers.Done()
		_, _, err := active.pickNextQueued(e, "m")
		retryDone <- err
	}()
	t.Cleanup(func() {
		cancelActive()
		cancelOlder()
		releaseHeldB()
		workers.Wait()
		active.finishAttempt(e, nil, nil, false)
		older.finishAttempt(e, nil, nil, false)
	})

	var admitted struct {
		item Item
		err  error
	}
	select {
	case admitted = <-olderDone:
	case <-time.After(time.Second):
		t.Fatal("older waiter did not advance after the retry attempt was released")
	}
	if admitted.err != nil || admitted.item.ID != "account-a" {
		t.Fatalf("older waiter admission item=%q error=%v", admitted.item.ID, admitted.err)
	}
	waitForCapacityWaiters(t, p, 1)

	reservations := budget.assertBalancedPrefix(t, 1)
	if len(reservations) != 2 {
		t.Fatalf("reservations before queue exit=%d, want active prior attempt and older admission", len(reservations))
	}
	var prior accounts.UsageReservation
	for _, reservation := range reservations {
		if reservation.RequestID == active.requestID && reservation.AccountID == "account-a" {
			prior = reservation
		}
	}
	if prior.ID == "" {
		t.Fatal("active request prior reservation is missing")
	}
	settlement, count := budget.settlement(prior.ID)
	if count != 1 || !settlement.NonBillable {
		t.Fatalf("prior attempt was not settled once before queue exit: %+v count=%d", settlement, count)
	}

	switch mode {
	case "cancel":
		cancelActive()
	case "shutdown":
		p.CloseCapacityWaitQueue()
	case "timeout":
		// The held account-b and active request's exclusion of account-a make
		// the retry wait terminate through the configured queue timeout.
	}
	var retryErr error
	select {
	case retryErr = <-retryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("retry queue exit did not return")
	}
	switch mode {
	case "cancel":
		if !errors.Is(retryErr, context.Canceled) {
			t.Fatalf("retry cancellation error=%v", retryErr)
		}
	case "shutdown":
		var classified *ExecutionError
		if !errors.As(retryErr, &classified) || classified.Classified.Code != "application_shutdown" {
			t.Fatalf("retry shutdown error=%v", retryErr)
		}
	case "timeout":
		var classified *ExecutionError
		if !errors.As(retryErr, &classified) || classified.Classified.Code != "capacity_queue_timeout" {
			t.Fatalf("retry timeout error=%v", retryErr)
		}
	}

	active.finishAttempt(e, nil, nil, false)
	older.finishAttempt(e, nil, nil, false)
	releaseHeldB()
	reservations = budget.assertBalanced(t)
	if len(reservations) != 2 {
		t.Fatalf("final reservation count=%d, want 2", len(reservations))
	}
	if p.capacityWaiterCount() != 0 {
		t.Fatalf("capacity waiters leaked after %s: %d", mode, p.capacityWaiterCount())
	}
	for _, account := range p.Items() {
		if account.LiveInFlight != 0 || account.InFlight != 0 {
			t.Fatalf("capacity lease leaked after %s for %s: %+v", mode, account.ID, account)
		}
	}
}
