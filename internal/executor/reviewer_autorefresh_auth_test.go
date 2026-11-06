package executor

import (
	"context"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"path/filepath"
	"testing"
)

func TestReviewerAutomaticRefreshThenGenuineAuthFailure(t *testing.T) {
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
	if err = db.SaveCredentialPayload(ctx, a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	p := NewPool(nil, nil)
	p.SetAuthCoordinator(db.AuthCoordinator())
	db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
	db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
	p.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
	item, _ := p.ByID(a.ID)
	e := NewChatExecutor(p, "")
	e.AuthRecovery = db
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{run: func(callCtx context.Context, _ string) (providers.ChatOutcome, error) {
		gen, ver, _, x := db.AuthAttemptSnapshot(callCtx, a.ID)
		if x != nil {
			t.Fatal(x)
		}
		saved, x := db.SaveCredentialPayloadAfterRefreshFenced(callCtx, a.ID, "workbuddy-oauth-v1", old, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-new"}`), gen, ver)
		if x != nil || !saved {
			t.Fatalf("refresh saved=%v err=%v", saved, x)
		}
		return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Scope: "account", Status: 401, Message: "new credential rejected"}
	}}})
	_, _, err = e.chatInProcessNonStreamAttempt(ctx, item, translate.ChatRequest{Model: "m"}, 0, false)
	if err == nil {
		t.Fatal("expected genuine auth error")
	}
	after, _ := p.ByID(a.ID)
	if after.LastKind != KindAuth {
		t.Fatalf("genuine auth failure after same-attempt refresh was lost: kind=%q error=%q", after.LastKind, after.LastError)
	}
}
