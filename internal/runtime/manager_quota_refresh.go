package runtime

import (
	"context"
	"errors"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

const (
	maxConcurrentQuotaFetches = 4
	quotaRefreshBackoffBase   = 30 * time.Second
	quotaRefreshBackoffMax    = time.Hour
)

type quotaRefreshCall struct {
	done    chan struct{}
	err     error
	waiters int
}

type quotaRefreshFailure struct {
	attempts int
	until    time.Time
}

type quotaRefreshState struct {
	sem      chan struct{}
	mu       sync.Mutex
	running  map[string]*quotaRefreshCall
	failures map[string]quotaRefreshFailure
}

func (m *Manager) quotaRefreshState() *quotaRefreshState {
	m.quotaOnce.Do(func() {
		if m.quotaRefresh == nil {
			m.quotaRefresh = &quotaRefreshState{
				sem:      make(chan struct{}, maxConcurrentQuotaFetches),
				running:  map[string]*quotaRefreshCall{},
				failures: map[string]quotaRefreshFailure{},
			}
		}
	})
	return m.quotaRefresh
}

func (s *quotaRefreshState) begin(accountID string, now time.Time, bypassBackoff bool) (*quotaRefreshCall, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call := s.running[accountID]; call != nil {
		call.waiters++
		return call, false, false
	}
	if s.backingOffLocked(accountID, now, bypassBackoff) {
		return nil, false, true
	}
	call := &quotaRefreshCall{done: make(chan struct{})}
	s.running[accountID] = call
	return call, true, false
}

func (s *quotaRefreshState) leave(call *quotaRefreshCall) {
	s.mu.Lock()
	if call.waiters > 0 {
		call.waiters--
	}
	s.mu.Unlock()
}

func (s *quotaRefreshState) backingOff(accountID string, now time.Time, bypassBackoff ...bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backingOffLocked(accountID, now, len(bypassBackoff) > 0 && bypassBackoff[0])
}

func (s *quotaRefreshState) backingOffLocked(accountID string, now time.Time, bypass bool) bool {
	if bypass {
		return false
	}
	failure, ok := s.failures[accountID]
	if !ok {
		return false
	}
	if !now.Before(failure.until) {
		return false
	}
	return true
}

func (s *quotaRefreshState) finish(accountID string, call *quotaRefreshCall, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[accountID] == call {
		delete(s.running, accountID)
	}
	call.err = err
	if err == nil {
		delete(s.failures, accountID)
	} else if !errors.Is(err, context.Canceled) {
		previous := s.failures[accountID]
		attempts := previous.attempts + 1
		delay := quotaRefreshBackoff(attempts, quotaRetryAfter(err))
		s.failures[accountID] = quotaRefreshFailure{attempts: attempts, until: time.Now().Add(delay)}
	}
	close(call.done)
}

func quotaRefreshBackoff(attempts int, retryAfter time.Duration) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := quotaRefreshBackoffBase
	for i := 1; i < attempts && delay < quotaRefreshBackoffMax; i++ {
		delay *= 2
	}
	if delay > quotaRefreshBackoffMax {
		delay = quotaRefreshBackoffMax
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func quotaRetryAfter(err error) time.Duration {
	var providerErr *providers.Error
	if errors.As(err, &providerErr) {
		return providerErr.RetryAfter
	}
	var retryAfter interface{ RetryAfterDuration() time.Duration }
	if errors.As(err, &retryAfter) {
		return retryAfter.RetryAfterDuration()
	}
	return 0
}

func quotaRefreshDueAt(quota *QuotaSnapshot, accountID string, interval time.Duration, now time.Time) time.Time {
	if quota == nil || strings.TrimSpace(quota.FetchedAt) == "" {
		return now
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(quota.FetchedAt))
	if err != nil {
		return now
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(accountID))
	maxJitter := interval / 10
	var jitter time.Duration
	if maxJitter > 0 {
		jitter = time.Duration(uint64(hash.Sum32()) % uint64(maxJitter+1))
	}
	return fetchedAt.Add(interval).Add(jitter)
}

func (m *Manager) refreshDueQuotas(parent context.Context, now time.Time) {
	if m == nil || m.pool == nil || m.store == nil {
		return
	}
	ctx, cancel, enabled := m.automaticMaintenanceContext(parent)
	if !enabled {
		return
	}
	intervalMinutes, err := accounts.QuotaRefreshIntervalMinutes(ctx, m.store)
	if err != nil {
		log.Printf("quota refresh settings unavailable")
		cancel()
		return
	}
	items, err := m.store.List(ctx)
	cancel()
	if err != nil {
		log.Printf("quota refresh account list unavailable")
		return
	}
	poolItems := make(map[string]Item)
	for _, item := range m.pool.Items() {
		poolItems[item.ID] = item
	}
	interval := time.Duration(intervalMinutes) * time.Minute
	for _, account := range items {
		if !account.Enabled || !quotaRefreshProviderSupported(account.Provider, account.ProviderRegion) {
			continue
		}
		item, exists := poolItems[account.ID]
		if !exists || (item.Ready != nil && !*item.Ready && (item.Hot == nil || !*item.Hot)) {
			continue
		}
		if now.Before(quotaRefreshDueAt(item.Quota, item.ID, interval, now)) {
			continue
		}
		m.startScheduledQuotaRefresh(item)
	}
}

func quotaRefreshProviderSupported(provider, region string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "workbuddy":
		return true
	case "trae":
		return strings.EqualFold(strings.TrimSpace(region), "cn")
	default:
		return false
	}
}

func (m *Manager) startScheduledQuotaRefresh(item Item) {
	ctx, cancel, enabled := m.automaticMaintenanceContext(m.runCtx)
	if !enabled {
		return
	}
	// Skip before registering in-flight work. Otherwise a disabled/corrupt
	// schedule leaves an unclosed call that manual refreshes join forever.
	schedule, scheduleErr := accounts.LoadMaintenanceSchedule(ctx, m.store)
	if scheduleErr != nil || !schedule.QuotaEnabled {
		cancel()
		return
	}
	state := m.quotaRefreshState()
	call, owner, _ := state.begin(item.ID, time.Now(), false)
	if !owner {
		if call != nil {
			state.leave(call)
		}
		cancel()
		return
	}
	m.mu.Lock()
	if m.runCtx.Err() != nil {
		m.mu.Unlock()
		state.finish(item.ID, call, context.Canceled)
		cancel()
		return
	}
	epoch := m.accountEpoch[item.ID]
	m.maintenanceDone.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.maintenanceDone.Done()
		defer cancel()
		err := m.executeQuotaRefresh(ctx, item, false, epoch)
		state.finish(item.ID, call, err)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("scheduled quota refresh failed account=%s provider=%s", item.ID, item.Provider)
		}
	}()
}

// refreshQuota coordinates automatic, startup and manual requests for the same
// account. A manual request joins an in-flight refresh, bypasses due-time and
// failure backoff, and still uses the single process-wide concurrency cap.
func (m *Manager) refreshQuota(ctx context.Context, item Item, force, manual bool, expectedEpoch ...uint64) error {
	if m == nil || item.ID == "" {
		return nil
	}
	state := m.quotaRefreshState()
	call, owner, skipped := state.begin(item.ID, time.Now(), manual)
	if skipped {
		return nil
	}
	if !owner {
		defer state.leave(call)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			return call.err
		}
	}
	epoch := uint64(0)
	if len(expectedEpoch) > 0 {
		epoch = expectedEpoch[0]
	} else {
		m.mu.Lock()
		epoch = m.accountEpoch[item.ID]
		m.mu.Unlock()
	}
	err := m.executeQuotaRefresh(ctx, item, force, epoch, manual)
	state.finish(item.ID, call, err)
	return err
}

func (m *Manager) executeQuotaRefresh(ctx context.Context, item Item, force bool, expectedEpoch uint64, manual ...bool) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	state := m.quotaRefreshState()
	select {
	case state.sem <- struct{}{}:
		defer func() { <-state.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if quotaRefreshProviderSupported(item.Provider, item.Region) {
		run, err := m.claimMaintenance(ctx, item.ID, "quota", len(manual) > 0 && manual[0])
		if errors.Is(err, accounts.ErrDailyTasksNotDue) || (errors.Is(err, accounts.ErrDailyTasksBusy) && (len(manual) == 0 || !manual[0])) {
			return nil
		}
		if err != nil {
			return err
		}
		interval, _ := accounts.QuotaRefreshIntervalMinutes(ctx, m.store)
		defer func() { result = m.finishMaintenance(ctx, run, result, time.Duration(interval)*time.Minute) }()
	}
	var (
		quota *QuotaSnapshot
		err   error
	)
	if item.Runtime == string(providers.RuntimeInProcess) || strings.TrimSpace(item.URL) == "" {
		if m.providers == nil {
			if quotaRefreshProviderSupported(item.Provider, item.Region) {
				return providers.ErrUnsupported
			}
			return nil
		}
		adapter, ok := m.providers.Get(item.Provider)
		if !ok || adapter.Prober == nil {
			if quotaRefreshProviderSupported(item.Provider, item.Region) {
				return providers.ErrUnsupported
			}
			return nil
		}
		quota, err = quotaSnapshotFromProvider(ctx, item.ID, adapter.Prober)
	} else {
		quota, err = m.fetchQuotaSnapshot(ctx, item.ID, item.URL, force)
	}
	if err != nil {
		return &QuotaRefreshError{Err: err}
	}
	if quota == nil {
		if quotaRefreshProviderSupported(item.Provider, item.Region) {
			return &QuotaRefreshError{Err: errors.New("upstream quota was not reported")}
		}
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.runCtx.Err() != nil || m.accountEpoch[item.ID] != expectedEpoch {
		return context.Canceled
	}
	current, ok := m.pool.ByID(item.ID)
	if !ok || current.Provider != item.Provider || current.URL != item.URL || current.Runtime != item.Runtime {
		return context.Canceled
	}
	m.persistQuota(ctx, item.ID, quota)
	return nil
}
