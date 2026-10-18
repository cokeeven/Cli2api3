package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type capacityWaiter struct {
	id      uint64
	key     string
	counted bool
}

type capacityWaitQueue struct {
	mu      sync.Mutex
	queues  map[string][]*capacityWaiter
	count   int
	nextID  uint64
	changed chan struct{}
	closed  bool
}

type capacityQueueObservation struct {
	Status   string
	WaitMs   int64
	Position int
}

type capacityQueueTry func() (Item, int, error, bool)

func newCapacityWaitQueue() *capacityWaitQueue {
	return &capacityWaitQueue{queues: map[string][]*capacityWaiter{}, changed: make(chan struct{})}
}

func (q *capacityWaitQueue) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *capacityWaitQueue) signal() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.signalLocked()
	q.mu.Unlock()
}

func (q *capacityWaitQueue) close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.signalLocked()
	}
	q.mu.Unlock()
}

func (q *capacityWaitQueue) removeLocked(waiter *capacityWaiter) {
	queue := q.queues[waiter.key]
	for index, item := range queue {
		if item.id != waiter.id {
			continue
		}
		queue = append(queue[:index], queue[index+1:]...)
		if waiter.counted {
			q.count--
		}
		break
	}
	if len(queue) == 0 {
		delete(q.queues, waiter.key)
	} else {
		q.queues[waiter.key] = queue
	}
	q.signalLocked()
}

// admit preserves a per-route FIFO boundary around the initial fast attempt.
// The first caller installs an uncounted admission marker before trying; later
// equivalent calls queue behind it. If the route is saturated, the marker is
// promoted to a counted waiter. Different route keys retain independent fast
// attempts, and the queue lock is never held while selecting a route.
func (q *capacityWaitQueue) admit(ctx context.Context, key string, maxWaiters int, maxWait time.Duration, try capacityQueueTry) (Item, int, error, capacityQueueObservation) {
	started := time.Now()
	observation := capacityQueueObservation{}
	if q == nil || maxWaiters <= 0 || maxWait <= 0 || strings.TrimSpace(key) == "" {
		item, index, err, retry := try()
		if !retry {
			return item, index, err, observation
		}
		observation.Status = "disabled"
		observation.WaitMs = time.Since(started).Milliseconds()
		return Item{}, index, localCapacityError("concurrency_capacity", "local request capacity is full"), observation
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		observation.Status = "shutdown"
		observation.WaitMs = time.Since(started).Milliseconds()
		return Item{}, 0, localCapacityShutdownError(), observation
	}
	q.nextID++
	waiter := &capacityWaiter{id: q.nextID, key: key}
	queue := q.queues[key]
	if len(queue) > 0 {
		// An uncounted head means no global waiter slot was available when its
		// initial attempt began. Keep later equivalent arrivals behind that
		// head without exceeding the configured queue bound.
		if !queue[0].counted || q.count >= maxWaiters {
			q.mu.Unlock()
			observation.Status = "full"
			observation.WaitMs = time.Since(started).Milliseconds()
			return Item{}, 0, localCapacityError("capacity_queue_full", "local capacity wait queue is full"), observation
		}
		waiter.counted = true
		q.count++
		q.queues[key] = append(queue, waiter)
		observation.Position = len(queue) + 1
		q.signalLocked()
		q.mu.Unlock()
		return q.await(ctx, waiter, maxWait, started, try, observation)
	}
	// Reserve the route's head while the first admission attempt runs. When
	// possible it also reserves one bounded waiter slot, so a concurrent
	// equivalent arrival cannot turn a failed fast attempt into an overfull
	// queue. The selection callback runs without the queue lock.
	if q.count < maxWaiters {
		waiter.counted = true
		q.count++
	}
	q.queues[key] = []*capacityWaiter{waiter}
	observation.Position = 1
	q.signalLocked()
	q.mu.Unlock()

	item, index, err, retry := try()
	if !retry {
		q.mu.Lock()
		q.removeLocked(waiter)
		q.mu.Unlock()
		// Preserve the old diagnostics for calls that never had to queue.
		observation.Position = 0
		return item, index, err, observation
	}

	q.mu.Lock()
	if err := ctx.Err(); err != nil {
		q.removeLocked(waiter)
		q.mu.Unlock()
		observation.Status = "canceled"
		observation.WaitMs = time.Since(started).Milliseconds()
		return Item{}, 0, err, observation
	}
	if q.closed {
		q.removeLocked(waiter)
		q.mu.Unlock()
		observation.Status = "shutdown"
		observation.WaitMs = time.Since(started).Milliseconds()
		return Item{}, 0, localCapacityShutdownError(), observation
	}
	if !waiter.counted && q.count >= maxWaiters {
		q.removeLocked(waiter)
		q.mu.Unlock()
		observation.Status = "full"
		observation.WaitMs = time.Since(started).Milliseconds()
		return Item{}, 0, localCapacityError("capacity_queue_full", "local capacity wait queue is full"), observation
	}
	if !waiter.counted {
		waiter.counted = true
		q.count++
	}
	observation.Position = len(q.queues[key])
	q.signalLocked()
	q.mu.Unlock()
	return q.await(ctx, waiter, maxWait, started, try, observation)
}

func (q *capacityWaitQueue) await(ctx context.Context, waiter *capacityWaiter, maxWait time.Duration, started time.Time, try capacityQueueTry, observation capacityQueueObservation) (Item, int, error, capacityQueueObservation) {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			q.mu.Lock()
			q.removeLocked(waiter)
			q.mu.Unlock()
			observation.Status = "canceled"
			observation.WaitMs = time.Since(started).Milliseconds()
			return Item{}, 0, err, observation
		}
		q.mu.Lock()
		if q.closed {
			q.removeLocked(waiter)
			q.mu.Unlock()
			observation.Status = "shutdown"
			observation.WaitMs = time.Since(started).Milliseconds()
			return Item{}, 0, localCapacityShutdownError(), observation
		}
		queue := q.queues[waiter.key]
		isHead := len(queue) > 0 && queue[0].id == waiter.id
		changed := q.changed
		q.mu.Unlock()
		if isHead {
			item, index, err, retry := try()
			if !retry {
				q.mu.Lock()
				q.removeLocked(waiter)
				q.mu.Unlock()
				if err == nil {
					observation.Status = "admitted"
				} else {
					observation.Status = "unavailable"
				}
				observation.WaitMs = time.Since(started).Milliseconds()
				return item, index, err, observation
			}
		}
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.removeLocked(waiter)
			q.mu.Unlock()
			observation.Status = "canceled"
			observation.WaitMs = time.Since(started).Milliseconds()
			return Item{}, 0, ctx.Err(), observation
		case <-timer.C:
			q.mu.Lock()
			q.removeLocked(waiter)
			q.mu.Unlock()
			observation.Status = "timeout"
			observation.WaitMs = time.Since(started).Milliseconds()
			return Item{}, 0, localCapacityError("capacity_queue_timeout", "local capacity wait timed out"), observation
		case <-changed:
		}
	}
}

func localCapacityError(code, message string) error {
	return NewExecutionError(Classified{
		Kind: KindConcurrencyCapacity, Status: 429, Code: code, Type: "api_error",
		Scope: "local_capacity", Message: message, RetryAfter: 5 * time.Second,
	}, nil)
}

func localCapacityShutdownError() error {
	return NewExecutionError(Classified{
		Kind: KindConcurrencyCapacity, Status: 503, Code: "application_shutdown", Type: "api_error",
		Scope: "local_capacity", Message: "application is shutting down",
	}, nil)
}

func isConcurrencyCapacityError(err error) bool {
	var classified *ExecutionError
	return errors.As(err, &classified) && classified != nil && classified.Classified.Kind == KindConcurrencyCapacity
}

func capacityRouteKey(loop *routeLoop, model string) string {
	if loop == nil {
		return ""
	}
	allowed := append([]string(nil), loop.allowed...)
	platforms := append([]string(nil), loop.platforms...)
	for i := range allowed {
		allowed[i] = strings.ToLower(strings.TrimSpace(allowed[i]))
	}
	for i := range platforms {
		platforms[i] = strings.ToLower(strings.TrimSpace(platforms[i]))
	}
	// Eligibility sets, rather than scheduling order, define who may wait
	// together. Sorting keeps equivalent routes in the same fair queue.
	sortStrings(allowed)
	sortStrings(platforms)
	shape := struct {
		KeyID        string
		Policy       accounts.KeyRoutingPolicy
		Model        string
		HardProvider string
		Strict       string
		Pinned       string
		Allowed      []string
		Platforms    []string
	}{
		KeyID: loop.identity.KeyID, Policy: loop.identity.Policy,
		Model: routeModel(model), HardProvider: strings.ToLower(loop.hardProvider),
		Strict: loop.strictAccount, Pinned: loop.pinned, Allowed: allowed, Platforms: platforms,
	}
	data, _ := json.Marshal(shape)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (p *Pool) waitForCapacity(ctx context.Context, key string, maxWaiters int, maxWait time.Duration, try capacityQueueTry) (Item, int, error, capacityQueueObservation) {
	if p == nil {
		return Item{}, 0, localCapacityError("concurrency_capacity", "local request capacity is full"), capacityQueueObservation{Status: "disabled"}
	}
	return p.capacityWait.admit(ctx, key, maxWaiters, maxWait, try)
}

func (p *Pool) signalCapacityChanged() {
	if p != nil && p.capacityWait != nil {
		p.capacityWait.signal()
	}
}

func (p *Pool) CloseCapacityWaitQueue() {
	if p != nil && p.capacityWait != nil {
		p.capacityWait.close()
	}
}

func (p *Pool) capacityWaiterCount() int {
	if p == nil || p.capacityWait == nil {
		return 0
	}
	p.capacityWait.mu.Lock()
	defer p.capacityWait.mu.Unlock()
	return p.capacityWait.count
}
