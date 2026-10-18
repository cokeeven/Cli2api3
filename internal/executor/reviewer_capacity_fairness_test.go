package executor

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

// Regression for the initial-pick fast path: a later equivalent request must
// not acquire a lease ahead of a request already waiting for that route.
func TestNewArrivalCannotOvertakeExistingCapacityWaiter(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	p, releaseHeld := capacityTestPool()
	defer releaseHeld()
	e := NewChatExecutor(p, "")
	e.CapacityQueueWait = time.Second

	olderCtx, cancelOlder := context.WithCancel(context.Background())
	defer cancelOlder()
	older := newSaturatedRouteLoop(olderCtx, e)
	olderDone := make(chan error, 1)
	go func() {
		_, _, err := older.pickNextQueued(e, "m")
		olderDone <- err
	}()
	waitForCapacityWaiters(t, p, 1)
	p.Upsert(Item{ID: "trae-b", Provider: "trae", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1})
	independent := e.newRouteLoop(context.Background(), "", "trae", translate.ChatRequest{Model: "m"})
	independentItem, _, independentErr := independent.pickNextQueued(e, "m")
	if independentErr != nil || independentItem.ID != "trae-b" {
		t.Fatalf("independent route was blocked by another route's waiter: item=%+v err=%v", independentItem, independentErr)
	}
	independent.finishAttempt(e, nil, nil, false)
	releaseHeld()

	newerCtx, cancelNewer := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancelNewer()
	newer := newSaturatedRouteLoop(newerCtx, e)
	item, _, newerErr := newer.pickNextQueued(e, "m")
	overtook := newerErr == nil && item.ID == "account-a"
	if newerErr == nil {
		newer.finishAttempt(e, nil, nil, false)
	}
	select {
	case olderErr := <-olderDone:
		if olderErr == nil {
			older.finishAttempt(e, nil, nil, false)
		}
		if olderErr != nil {
			t.Fatalf("older waiter failed during cleanup: %v", olderErr)
		}
	case <-time.After(time.Second):
		cancelOlder()
		t.Fatal("older waiter did not finish")
	}
	if p.capacityWaiterCount() != 0 {
		t.Fatal("probe cleanup leaked waiters")
	}
	if overtook {
		t.Fatal("new arrival acquired account-a before an already queued equivalent route")
	}
}
