package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func capacityTestPool() (*Pool, func()) {
	p := NewPool([]string{"http://unused"}, []string{"account-a"})
	p.Upsert(Item{ID: "account-a", URL: "http://unused", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	query := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}
	_, release, ok := p.acquireRoute(query, routingIdentity{})
	if !ok {
		panic("failed to reserve synthetic capacity")
	}
	return p, release
}

func waitForCapacityWaiters(t *testing.T, p *Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.capacityWaiterCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("capacity waiter count=%d want=%d", p.capacityWaiterCount(), want)
}

func newSaturatedRouteLoop(ctx context.Context, e ChatExecutor) *routeLoop {
	request := translate.ChatRequest{Model: "m"}
	loop := e.newRouteLoop(ctx, "", "workbuddy", request)
	loop.requestID = "synthetic-capacity-test"
	return &loop
}

func TestCapacityWaitQueueFairlyAdmitsAndReleases(t *testing.T) {
	p, releaseHeld := capacityTestPool()
	defer releaseHeld()
	e := NewChatExecutor(p, "")
	e.CapacityQueueWait = time.Second
	first, second := newSaturatedRouteLoop(context.Background(), e), newSaturatedRouteLoop(context.Background(), e)
	type result struct {
		loop  *routeLoop
		item  Item
		index int
		err   error
	}
	results := make(chan result, 2)
	go func() { item, index, err := first.pickNextQueued(e, "m"); results <- result{first, item, index, err} }()
	waitForCapacityWaiters(t, p, 1)
	go func() { item, index, err := second.pickNextQueued(e, "m"); results <- result{second, item, index, err} }()
	waitForCapacityWaiters(t, p, 2)
	releaseHeld()
	firstResult := <-results
	if firstResult.loop != first || firstResult.err != nil || firstResult.item.ID != "account-a" {
		t.Fatalf("first waiter result = %+v", firstResult)
	}
	select {
	case got := <-results:
		t.Fatalf("later waiter overtook the first admitted lease: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	first.finishAttempt(e, nil, nil, false)
	secondResult := <-results
	if secondResult.loop != second || secondResult.err != nil || secondResult.item.ID != "account-a" {
		t.Fatalf("second waiter result = %+v", secondResult)
	}
	second.finishAttempt(e, nil, nil, false)
	if count := p.capacityWaiterCount(); count != 0 {
		t.Fatalf("waiters leaked: %d", count)
	}
}

func TestCapacityWaitQueueCancellationAndTimeoutRemoveWaiters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "cancel", cancel: true},
		{name: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, releaseHeld := capacityTestPool()
			defer releaseHeld()
			e := NewChatExecutor(p, "")
			e.CapacityQueueWait = 25 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loop := newSaturatedRouteLoop(ctx, e)
			result := make(chan error, 1)
			go func() { _, _, err := loop.pickNextQueued(e, "m"); result <- err }()
			waitForCapacityWaiters(t, p, 1)
			if tc.cancel {
				cancel()
			}
			err := <-result
			if tc.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error = %v", err)
				}
			} else {
				var classified *ExecutionError
				if !errors.As(err, &classified) || classified.Classified.Code != "capacity_queue_timeout" || classified.Classified.Kind != accounts.KindConcurrencyCapacity {
					t.Fatalf("timeout error = %#v", err)
				}
			}
			if count := p.capacityWaiterCount(); count != 0 {
				t.Fatalf("waiters leaked after %s: %d", tc.name, count)
			}
		})
	}
}

func TestCapacityWaitQueueFullIsDistinct(t *testing.T) {
	p, releaseHeld := capacityTestPool()
	defer releaseHeld()
	e := NewChatExecutor(p, "")
	e.CapacityQueueMaxWaiters = 1
	e.CapacityQueueWait = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newSaturatedRouteLoop(ctx, e)
	result := make(chan error, 1)
	go func() { _, _, err := first.pickNextQueued(e, "m"); result <- err }()
	waitForCapacityWaiters(t, p, 1)
	second := newSaturatedRouteLoop(context.Background(), e)
	_, _, err := second.pickNextQueued(e, "m")
	var classified *ExecutionError
	if !errors.As(err, &classified) || classified.Classified.Code != "capacity_queue_full" || classified.Classified.Kind != accounts.KindConcurrencyCapacity {
		t.Fatalf("queue-full result=%#v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter cleanup error=%v", err)
	}
	if count := p.capacityWaiterCount(); count != 0 {
		t.Fatalf("waiters leaked after queue-full cleanup: %d", count)
	}
}

func TestCapacityWaitQueueShutdownRemovesWaiter(t *testing.T) {
	p, releaseHeld := capacityTestPool()
	e := NewChatExecutor(p, "")
	loop := newSaturatedRouteLoop(context.Background(), e)
	result := make(chan error, 1)
	go func() { _, _, err := loop.pickNextQueued(e, "m"); result <- err }()
	waitForCapacityWaiters(t, p, 1)
	p.CloseCapacityWaitQueue()
	err := <-result
	var classified *ExecutionError
	if !errors.As(err, &classified) || classified.Classified.Status != 503 || classified.Classified.Code != "application_shutdown" || classified.Classified.Kind != accounts.KindConcurrencyCapacity {
		t.Fatalf("shutdown result=%#v", err)
	}
	if loop.capacityQueueStatus != "shutdown" || loop.stopReason != "application_shutdown" {
		t.Fatalf("shutdown diagnostics status=%q stop=%q", loop.capacityQueueStatus, loop.stopReason)
	}
	if count := p.capacityWaiterCount(); count != 0 {
		t.Fatalf("waiters leaked after shutdown: %d", count)
	}
	releaseHeld()
	item, ok := p.ByID("account-a")
	if !ok || item.InFlight != 0 || item.LiveInFlight != 0 {
		t.Fatalf("in-flight capacity leaked after shutdown: %+v", item)
	}
}

func TestCapacityAdmissionWithoutCandidatesReleasesRouteMarker(t *testing.T) {
	p := NewPool(nil, nil)
	e := NewChatExecutor(p, "")
	e.CapacityQueueWait = time.Second
	loop := e.newRouteLoop(context.Background(), "", "workbuddy", translate.ChatRequest{Model: "m"})
	if _, _, err := loop.pickNextQueued(e, "m"); err == nil {
		t.Fatal("route without candidates unexpectedly succeeded")
	}
	if count := p.capacityWaiterCount(); count != 0 {
		t.Fatalf("admission marker leaked after no-candidate result: %d", count)
	}
}

func TestPoolDoesNotPersistLocalCapacityAsAccountFailure(t *testing.T) {
	p := NewPool([]string{"http://unused"}, []string{"account-a"})
	p.Upsert(Item{ID: "account-a", URL: "http://unused", Provider: "workbuddy", Region: "cn", LastError: "previous upstream error", LastKind: accounts.KindRateLimit})
	before, ok := p.ByID("account-a")
	if !ok {
		t.Fatal("synthetic account was not inserted")
	}
	p.MarkClassified("account-a", Classified{
		Kind: accounts.KindConcurrencyCapacity, Status: 429, Code: "concurrency_capacity",
		Scope: "local_capacity", Message: "local request capacity is full",
	})
	after, ok := p.ByID("account-a")
	if !ok {
		t.Fatal("synthetic account disappeared")
	}
	if after.LastError != before.LastError || after.LastKind != before.LastKind || !after.DownUntil.Equal(before.DownUntil) || after.StateVersion != before.StateVersion {
		t.Fatalf("local capacity changed account state: before=%+v after=%+v", before, after)
	}
}
