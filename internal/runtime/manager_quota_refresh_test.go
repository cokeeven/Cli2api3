package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
)

type quotaRefreshProber struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
	result    *providers.QuotaInfo
	err       error
}

func (p *quotaRefreshProber) Probe(context.Context, string) (providers.AccountHealth, error) {
	return providers.AccountHealth{Ready: true, Hot: true}, nil
}

func (p *quotaRefreshProber) Quota(ctx context.Context, _ string) (*providers.QuotaInfo, error) {
	p.mu.Lock()
	p.calls++
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.result, p.err
}

func TestQuotaRefreshDueUsesFetchedAtWithStableJitter(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	interval := 30 * time.Minute
	quota := &accounts.QuotaSnapshot{FetchedAt: now.Add(-interval).Format(time.RFC3339)}
	first := quotaRefreshDueAt(quota, "account-a", interval, now)
	if first.Before(now) || first.After(now.Add(interval/10)) {
		t.Fatalf("due time %s outside expected jitter window [%s, %s]", first, now, now.Add(interval/10))
	}
	if second := quotaRefreshDueAt(quota, "account-a", interval, now); !second.Equal(first) {
		t.Fatalf("jitter is not stable: first=%s second=%s", first, second)
	}
	if missing := quotaRefreshDueAt(nil, "new-account", interval, now); !missing.Equal(now) {
		t.Fatalf("missing snapshot should be immediately due, got %s", missing)
	}
}

func TestQuotaRefreshDeduplicatesAndFailurePreservesSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := &accounts.QuotaSnapshot{Total: 100, Remaining: 17, FetchedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if err := db.SaveQuota(ctx, account.ID, old); err != nil {
		t.Fatal(err)
	}
	prober := &quotaRefreshProber{started: make(chan struct{}, 2), release: make(chan struct{}), err: errors.New("synthetic quota outage")}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Prober: prober})
	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	manager.SetProviders(registry)
	manager.Pool().Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Runtime: string(providers.RuntimeInProcess), Quota: old})
	defer manager.Close()

	item, _ := manager.Pool().ByID(account.ID)
	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.refreshQuota(ctx, item, false, true, 0) }()
	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("first quota refresh did not start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- manager.refreshQuota(ctx, item, true, true, 0) }()
	deadline := time.Now().Add(time.Second)
	joined := false
	for time.Now().Before(deadline) {
		manager.quotaRefreshState().mu.Lock()
		call := manager.quotaRefreshState().running[account.ID]
		joined = call != nil && call.waiters == 1
		manager.quotaRefreshState().mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !joined {
		t.Fatal("manual refresh did not join the in-flight request")
	}
	close(prober.release)
	if err := <-firstDone; err == nil {
		t.Fatal("first refresh should report its synthetic failure")
	}
	if err := <-secondDone; err == nil {
		t.Fatal("joined refresh should observe the same failure")
	}
	prober.mu.Lock()
	calls := prober.calls
	prober.mu.Unlock()
	if calls != 1 {
		t.Fatalf("quota calls=%d, want one shared in-flight request", calls)
	}
	item, _ = manager.Pool().ByID(account.ID)
	stored, err := db.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Quota == nil || item.Quota.Remaining != 17 || stored.Quota == nil || stored.Quota.Remaining != 17 {
		t.Fatalf("quota failure cleared the previous snapshot: pool=%+v store=%+v", item.Quota, stored.Quota)
	}
}

func TestQuotaRefreshFailureBackoffAllowsManualRefresh(t *testing.T) {
	state := &quotaRefreshState{sem: make(chan struct{}, 1), running: map[string]*quotaRefreshCall{}, failures: map[string]quotaRefreshFailure{}}
	state.failures["a"] = quotaRefreshFailure{attempts: 3, until: time.Now().Add(time.Hour)}
	if state.backingOff("a", time.Now()) != true {
		t.Fatal("scheduled refresh should respect failure backoff")
	}
	if state.backingOff("a", time.Now(), true) {
		t.Fatal("manual refresh should bypass failure backoff")
	}
	if state.backingOff("a", time.Now().Add(2*time.Hour)) {
		t.Fatal("expired backoff still active")
	}
	if state.failures["a"].attempts != 3 {
		t.Fatal("elapsed backoff reset failure ladder")
	}
}

func TestDisabledQuotaScheduleDoesNotLeaveInflightRefresh(t *testing.T) {
	for _, setting := range []string{`{"credential_enabled":true,"credential_interval_minutes":15,"quota_enabled":false,"daily_enabled":false,"daily_time":"22:00"}`, `invalid synthetic setting`} {
		t.Run(setting, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "disabled.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SetSecret(ctx, accounts.MaintenanceScheduleSecret, setting); err != nil {
				t.Fatal(err)
			}
			m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
			defer m.Close()
			item := executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Runtime: string(providers.RuntimeInProcess)}
			m.Pool().Upsert(item)
			m.startScheduledQuotaRefresh(item)
			m.maintenanceDone.Wait()
			state := m.quotaRefreshState()
			state.mu.Lock()
			left := len(state.running)
			failures := len(state.failures)
			state.mu.Unlock()
			if left != 0 || failures != 0 {
				t.Fatalf("disabled/invalid schedule leaked running=%d failure=%d", left, failures)
			}
			// A manual request must be able to become the owner after a skip.
			call, owner, _ := state.begin(account.ID, time.Now(), true)
			if !owner {
				t.Fatal("manual refresh would wait forever on skipped work")
			}
			state.finish(account.ID, call, nil)
		})
	}
}

func TestPeriodicQuotaRefreshRunsWithoutCheckinOptIn(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "quota-periodic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSecret(ctx, accounts.QuotaRefreshIntervalSecret, "5"); err != nil {
		t.Fatal(err)
	}
	old := &accounts.QuotaSnapshot{Total: 100, Remaining: 80, FetchedAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)}
	ready, hot := true, true
	prober := &quotaRefreshProber{
		started: make(chan struct{}, 1),
		result:  &providers.QuotaInfo{Total: 100, Remaining: 90, Unit: "credits", FetchedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Prober: prober})
	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	manager.SetProviders(registry)
	manager.Pool().Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Runtime: string(providers.RuntimeInProcess), Ready: &ready, Hot: &hot, Quota: old})
	defer manager.Close()

	manager.refreshDueQuotas(ctx, time.Now())
	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("periodic quota refresh did not start for an account without check-in opt-in")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		item, _ := manager.Pool().ByID(account.ID)
		if item.Quota != nil && item.Quota.Remaining == 90 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	item, _ := manager.Pool().ByID(account.ID)
	t.Fatalf("periodic refresh did not replace the stale snapshot: %+v", item.Quota)
}

func TestQuotaRefreshProcessWideConcurrencyLimit(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "quota-concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := providers.NewRegistry()
	prober := &quotaRefreshProber{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
		result:  &providers.QuotaInfo{Total: 100, Remaining: 90, Unit: "credits"},
	}
	registry.Register(providers.Adapter{ID: "workbuddy", Prober: prober})
	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	manager.SetProviders(registry)
	defer manager.Close()
	ready, hot := true, true
	ids := make([]string, 0, maxConcurrentQuotaFetches+2)
	for i := 0; i < maxConcurrentQuotaFetches+2; i++ {
		account, err := db.Create(ctx, accounts.CreateAccount{Name: fmt.Sprintf("synthetic-%d", i), Provider: "workbuddy", Region: "cn", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, account.ID)
		manager.Pool().Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Runtime: string(providers.RuntimeInProcess), Ready: &ready, Hot: &hot})
	}
	for i := 0; i < maxConcurrentQuotaFetches+2; i++ {
		id := ids[i]
		item, _ := manager.Pool().ByID(id)
		manager.startScheduledQuotaRefresh(item)
	}
	for i := 0; i < maxConcurrentQuotaFetches; i++ {
		select {
		case <-prober.started:
		case <-time.After(time.Second):
			t.Fatalf("only %d quota fetches started", i)
		}
	}
	prober.mu.Lock()
	maxActive := prober.maxActive
	prober.mu.Unlock()
	if maxActive > maxConcurrentQuotaFetches {
		t.Fatalf("concurrent quota calls=%d, limit=%d", maxActive, maxConcurrentQuotaFetches)
	}
	close(prober.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		prober.mu.Lock()
		calls := prober.calls
		prober.mu.Unlock()
		if calls == maxConcurrentQuotaFetches+2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	prober.mu.Lock()
	calls := prober.calls
	prober.mu.Unlock()
	t.Fatalf("quota calls=%d, want %d", calls, maxConcurrentQuotaFetches+2)
}
