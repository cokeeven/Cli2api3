package executor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestSuccessfulSameAccountAttemptRecoversOnlyMatchingAuth(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic"}`)); err != nil {
		t.Fatal(err)
	}
	p := NewPool(nil, nil)
	p.SetAuthCoordinator(db.AuthCoordinator())
	db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
	db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
	until := time.Now().Add(time.Hour)
	p.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process", ModelDownUntil: map[string]time.Time{"other": until, "m": until}, Quota: &QuotaSnapshot{Exceeded: true}})
	p.MarkClassified(a.ID, Classified{Kind: KindAuth, Scope: "account", Message: "prior auth", Cooldown: time.Minute})
	item, _ := p.ByID(a.ID)
	e := NewChatExecutor(p, "")
	e.AuthRecovery = db
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{}})
	if _, _, err := e.chatInProcessNonStreamAttempt(ctx, item, translate.ChatRequest{Model: "m"}, 0, false); err != nil {
		t.Fatal(err)
	}
	after, _ := p.ByID(a.ID)
	if after.LastKind == KindAuth || !after.DownUntil.IsZero() || after.ModelDownUntil["other"] != until || after.ModelDownUntil["m"] != until || after.Quota == nil || !after.Quota.Exceeded {
		t.Fatalf("bad recovery: %+v", after)
	}
}

func TestLateSuccessfulAttemptDoesNotEraseNewFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic"}`)); err != nil {
		t.Fatal(err)
	}
	p := NewPool(nil, nil)
	p.SetAuthCoordinator(db.AuthCoordinator())
	db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
	db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
	p.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
	item, _ := p.ByID(a.ID)
	started, release := make(chan struct{}), make(chan struct{})
	e := NewChatExecutor(p, "")
	e.AuthRecovery = db
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		close(started)
		<-release
		return providers.ChatOutcome{Content: "synthetic"}, nil
	}}})
	done := make(chan error, 1)
	go func() {
		_, _, err := e.chatInProcessNonStreamAttempt(ctx, item, translate.ChatRequest{Model: "m"}, 0, false)
		done <- err
	}()
	<-started
	p.MarkClassified(a.ID, Classified{Kind: KindAuth, Scope: "account", Message: "new failure", Cooldown: time.Minute})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, _ := p.ByID(a.ID)
	if after.LastKind != KindAuth || after.LastError != "new failure" {
		t.Fatalf("late success cleared failure: %+v", after)
	}
}

func TestLateAuthFailureCannotPoisonNewLogin(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-old"}`)); err != nil {
		t.Fatal(err)
	}
	p := NewPool(nil, nil)
	p.SetAuthCoordinator(db.AuthCoordinator())
	db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
	db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
	p.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
	item, _ := p.ByID(a.ID)
	started, release := make(chan struct{}), make(chan struct{})
	e := NewChatExecutor(p, "")
	e.AuthRecovery = db
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		close(started)
		<-release
		return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Scope: "account", Status: 401, Message: "stale rejection"}
	}}})
	done := make(chan error, 1)
	go func() {
		_, _, err := e.chatInProcessNonStreamAttempt(ctx, item, translate.ChatRequest{Model: "m"}, 0, false)
		done <- err
	}()
	<-started
	if err := db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-new-login"}`)); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("upstream rejection was hidden")
	}
	after, _ := p.ByID(a.ID)
	if after.LastKind == KindAuth {
		t.Fatalf("old failure poisoned new login: %+v", after)
	}
}
