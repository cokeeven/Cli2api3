package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
)

type delayedAuthPersist struct {
	*store.Store
	started, release chan struct{}
}

type delayedOtherPersist struct {
	*store.Store
	blockedID        string
	started, release chan struct{}
}

func (s *delayedOtherPersist) RecordPoolState(ctx context.Context, state accounts.PoolState) error {
	if state.ID == s.blockedID {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		<-s.release
	}
	return s.Store.RecordPoolState(ctx, state)
}

type forceTestMaintainer struct {
	started chan string
	release chan struct{}
	err     error
}

func (f *forceTestMaintainer) Keepalive(context.Context, string) error { return nil }
func (f *forceTestMaintainer) ForceCredentialRefresh(ctx context.Context, id string) error {
	f.started <- id
	select {
	case <-f.release:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestLateMaintenanceRejectionCannotInvalidateNewLogin(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"old"}`)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn"})
	fake := &forceTestMaintainer{started: make(chan string, 1), release: make(chan struct{}), err: &providers.Error{Kind: accounts.KindAuth, Status: 401, Message: "old rejection"}}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", CredentialMaintenance: fake})
	m.SetProviders(registry)
	done := make(chan error, 1)
	go func() { done <- m.ForceCredentialAccount(ctx, a.ID) }()
	<-fake.started
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"new"}`)); err != nil {
		t.Fatal(err)
	}
	close(fake.release)
	if err := <-done; !errors.Is(err, providers.ErrCredentialSuperseded) {
		t.Fatalf("stale result=%v", err)
	}
	item, _ := m.pool.ByID(a.ID)
	if item.LastKind == accounts.KindAuth {
		t.Fatalf("old rejection poisoned new login: %+v", item)
	}
}

func TestManualForceUsesAccountLease(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "a", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Create(ctx, accounts.CreateAccount{Name: "b", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	fake := &forceTestMaintainer{started: make(chan string, 2), release: make(chan struct{})}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", CredentialMaintenance: fake})
	m.SetProviders(registry)
	done := make(chan error, 2)
	go func() { done <- m.ForceCredentialAccount(ctx, a.ID) }()
	if got := <-fake.started; got != a.ID {
		t.Fatalf("started %s", got)
	}
	if err := m.ForceCredentialAccount(ctx, a.ID); !errors.Is(err, accounts.ErrDailyTasksBusy) {
		t.Fatalf("concurrent same account: %v", err)
	}
	go func() { done <- m.ForceCredentialAccount(ctx, b.ID) }()
	select {
	case got := <-fake.started:
		if got != b.ID {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("other account blocked")
	}
	close(fake.release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func (s *delayedAuthPersist) RecordPoolState(ctx context.Context, state accounts.PoolState) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-s.release
	return s.Store.RecordPoolState(ctx, state)
}

func TestFencedRefreshRejectsPendingNewerPoolAuth(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := db.AuthCoordinator().Version(a.ID)
	delayed := &delayedAuthPersist{Store: db, started: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, delayed, nil)
	manager.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn"})
	manager.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "synthetic newer failure", Cooldown: time.Minute})
	<-delayed.started
	type outcome struct {
		saved bool
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		saved, err := db.SaveCredentialPayloadAfterRefreshFenced(ctx, a.ID, "workbuddy-oauth-v1", old, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-stale"}`), snapshot.CredentialGeneration, version)
		done <- outcome{saved, err}
	}()
	other := make(chan struct{})
	go func() { guard := db.AuthCoordinator().Lock("synthetic-other"); guard.Unlock(); close(other) }()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("unrelated account was blocked")
	}
	close(delayed.release)
	got := <-done
	manager.Flush()
	_ = manager.Close()
	if got.err != nil || got.saved {
		t.Fatalf("stale refresh saved=%v err=%v", got.saved, got.err)
	}
	current, err := db.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastErrorKind != accounts.KindAuth {
		t.Fatalf("auth failure lost: %+v", current)
	}
	_, payload, err := db.LoadCredentialPayload(ctx, a.ID)
	if err != nil || string(payload) != string(old) {
		t.Fatalf("credential changed: err=%v", err)
	}
}

func TestSuccessfulRefreshSupersedesOlderQueuedAuthSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "a", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Create(ctx, accounts.CreateAccount{Name: "b", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old"}`)
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	delayed := &delayedOtherPersist{Store: db, blockedID: b.ID, started: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, delayed, nil)
	defer m.Close()
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", RuntimeState: "auth_failed"})
	m.pool.Upsert(Item{ID: b.ID, Provider: "workbuddy", Region: "cn"})
	m.pool.MarkClassified(b.ID, executor.Classified{Kind: accounts.KindUnavailable, Message: "hold writer", Cooldown: time.Minute})
	<-delayed.started
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "prior auth", Cooldown: time.Minute})
	generation, version, _, err := db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := db.SaveCredentialPayloadAfterRefreshFenced(ctx, a.ID, "workbuddy-oauth-v1", old, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-fresh"}`), generation, version)
	if err != nil || !ok {
		close(delayed.release)
		t.Fatalf("refresh saved=%v err=%v", ok, err)
	}
	close(delayed.release)
	m.Flush()
	item, _ := m.pool.ByID(a.ID)
	current, err := db.Get(ctx, a.ID)
	if err != nil || item.LastKind == accounts.KindAuth || current.LastErrorKind == accounts.KindAuth || current.Status != "ready" {
		t.Fatalf("old snapshot replayed: item=%+v account=%+v err=%v", item, current, err)
	}
}

func TestFencedRefreshRecoversAuthAndPreservesOtherRestrictions(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	ready := false
	until := time.Now().Add(time.Hour)
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Ready: &ready, RuntimeState: "auth_failed", ModelDownUntil: map[string]time.Time{"other-model": until}, ModelLastKind: map[string]string{"other-model": accounts.KindRateLimit}, Quota: &accounts.QuotaSnapshot{Exceeded: true}})
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "synthetic auth failure", Cooldown: time.Minute})
	m.Flush()
	snapshot, err := db.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := db.AuthCoordinator().Version(a.ID)
	saved, err := db.SaveCredentialPayloadAfterRefreshFenced(ctx, a.ID, "workbuddy-oauth-v1", old, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-fresh"}`), snapshot.CredentialGeneration, version)
	if err != nil || !saved {
		t.Fatalf("saved=%v err=%v", saved, err)
	}
	m.Flush()
	item, ok := m.pool.ByID(a.ID)
	if !ok {
		t.Fatal("missing account")
	}
	if item.LastKind == accounts.KindAuth || !item.DownUntil.IsZero() || item.ModelDownUntil["other-model"] != until || item.Quota == nil || !item.Quota.Exceeded {
		t.Fatalf("recovery lost restrictions: %+v", item)
	}
	current, err := db.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastErrorKind == accounts.KindAuth || current.Status != "quota_exhausted" {
		t.Fatalf("database recovery: %+v", current)
	}
	// A later rejection must win even if the successful refresh's observer is
	// still queued; both memory and SQLite converge on the later auth event.
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "later rejection", Cooldown: time.Minute})
	m.Flush()
	item, _ = m.pool.ByID(a.ID)
	current, err = db.Get(ctx, a.ID)
	if err != nil || item.LastKind != accounts.KindAuth || current.LastErrorKind != accounts.KindAuth {
		t.Fatalf("later rejection lost: pool=%+v account=%+v err=%v", item, current, err)
	}
}

func TestSuccessfulAttemptCannotClearNewerAuthOrLogin(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old"}`)
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn"})
	generation, version, _, err := db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "newer auth", Cooldown: time.Minute})
	if saved, err := db.RecoverAuthAfterSuccess(ctx, a.ID, generation, version); err != nil || saved {
		t.Fatalf("late success saved=%v err=%v", saved, err)
	}
	m.Flush()
	generation, version, _, err = db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-login"}`)); err != nil {
		t.Fatal(err)
	}
	m.pool.MarkClassifiedAuthVersion(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "stale failure", Cooldown: time.Minute}, version)
	item, _ := m.pool.ByID(a.ID)
	if item.LastError == "stale failure" {
		t.Fatal("old failure contaminated newer login")
	}
	if saved, err := db.RecoverAuthAfterSuccess(ctx, a.ID, generation, version); err != nil || saved {
		t.Fatalf("old success after login saved=%v err=%v", saved, err)
	}
}

func TestStaleLocalProbeCannotOverwriteNewerFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn"})
	generation, version, _, err := db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "newer auth", Cooldown: time.Minute})
	applied := false
	ok, err := db.ApplyProbeFenced(ctx, a.ID, generation, version, "", "ready", "", "", func() { applied = true })
	if err != nil || ok || applied {
		t.Fatalf("stale probe applied=%v callback=%v err=%v", ok, applied, err)
	}
	m.Flush()
	item, _ := m.pool.ByID(a.ID)
	current, err := db.Get(ctx, a.ID)
	if err != nil || item.LastKind != accounts.KindAuth || current.LastErrorKind != accounts.KindAuth {
		t.Fatalf("auth failure lost: %+v %+v %v", item, current, err)
	}
}

func TestVerifiedLoginAndRefreshCannotOverwriteEachOther(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "login.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old"}`)
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn"})
	generation, version, _, err := db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	newLogin := []byte(`{"access_token":"synthetic-login"}`)
	ok, err := db.SaveVerifiedLoginFenced(ctx, a.ID, "workbuddy-oauth-v1", newLogin, "synthetic", generation, version)
	if err != nil || !ok {
		t.Fatalf("login=%v err=%v", ok, err)
	}
	ok, err = db.SaveCredentialPayloadAfterRefreshFenced(ctx, a.ID, "workbuddy-oauth-v1", old, "workbuddy-oauth-v1", []byte(`{"access_token":"stale"}`), generation, version)
	if err != nil || ok {
		t.Fatalf("old refresh overwrote login: saved=%v err=%v", ok, err)
	}
	generation, version, _, err = db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.pool.MarkClassified(a.ID, executor.Classified{Kind: accounts.KindAuth, Scope: "account", Message: "newer auth", Cooldown: time.Minute})
	ok, err = db.SaveVerifiedLoginFenced(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"stale-login"}`), "synthetic", generation, version)
	if err != nil || ok {
		t.Fatalf("stale login saved=%v err=%v", ok, err)
	}
	_, current, err := db.LoadCredentialPayload(ctx, a.ID)
	if err != nil || string(current) != string(newLogin) {
		t.Fatalf("login credential overwritten: %v", err)
	}
}

func TestVerifiedLoginDoesNotClearNonAuthRuntimeFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "nonauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Observe(ctx, a.ID, "", "error", "network failure", accounts.KindUnavailable); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	ready := false
	m.pool.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", RuntimeState: "auth_failed", Ready: &ready, LastKind: accounts.KindUnavailable, LastError: "network failure"})
	generation, version, _, err := db.AuthAttemptSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := db.SaveVerifiedLoginFenced(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic"}`), "", generation, version)
	if err != nil || !ok {
		t.Fatalf("login saved=%v err=%v", ok, err)
	}
	m.Flush()
	item, _ := m.pool.ByID(a.ID)
	current, err := db.Get(ctx, a.ID)
	if err != nil || item.RuntimeState != "auth_failed" || item.LastKind != accounts.KindUnavailable || current.Status != "error" || current.LastErrorKind != accounts.KindUnavailable {
		t.Fatalf("non-auth failure cleared: %+v %+v %v", item, current, err)
	}
}
