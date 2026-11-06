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

// These exercises execute the ordinary in-process success settlement, not a
// simulated manual call to MarkProbeOK. The upstream adapter is synthetic.
func TestProbeSettlementDoesNotCrossNewAuthOrCredential(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, change := range []string{"auth", "credential_with_hook", "credential_before_hook", "credential_aba"} {
			name := map[bool]string{false: "nonstream", true: "stream"}[stream] + "/" + change
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err = db.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-old"}`)); err != nil {
					t.Fatal(err)
				}
				p := NewPool(nil, nil)
				p.SetAuthCoordinator(db.AuthCoordinator())
				db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
				if change != "credential_before_hook" {
					db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
				}
				oldUntil := time.Now().Add(-time.Second)
				p.Upsert(Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m", "other"}, ModelDownUntil: map[string]time.Time{"m": oldUntil}})
				changeAuth := func() {
					if change == "auth" {
						p.MarkClassified(account.ID, Classified{Kind: KindAuth, Scope: "account", Cooldown: time.Minute, Message: "synthetic auth"})
						return
					}
					if err := db.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-new"}`)); err != nil {
						t.Fatal(err)
					}
					if change == "credential_aba" {
						if err := db.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-old"}`)); err != nil {
							t.Fatal(err)
						}
					}
				}
				e := NewChatExecutor(p, "")
				e.AuthRecovery = db
				e.Providers = providers.NewRegistry()
				chat := admissionChat{}
				if !stream {
					chat.run = func(context.Context, string) (providers.ChatOutcome, error) {
						changeAuth()
						return providers.ChatOutcome{Content: "synthetic"}, nil
					}
				}
				e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: chat})
				if stream {
					result, err := e.ChatStreamProxy(ctx, translate.ChatRequest{Model: "m", Stream: true}, "", "workbuddy")
					if err != nil {
						t.Fatal(err)
					}
					changeAuth()
					result.Finalize(StreamCompletion{Completed: true})
				} else {
					if _, err := e.ChatNonStream(ctx, translate.ChatRequest{Model: "m"}, "", "workbuddy"); err != nil {
						t.Fatal(err)
					}
				}
				got, _ := p.ByID(account.ID)
				if !got.ModelDownUntil["m"].Equal(oldUntil) {
					t.Fatalf("stale success cleared model restriction after %s: %+v", change, got.ModelDownUntil)
				}
				if change == "auth" && got.LastKind != KindAuth {
					t.Fatalf("new auth event lost: %+v", got)
				}
				p.mu.Lock()
				busy := p.modelProbes[modelProbeKey(account.ID, "m")]
				p.mu.Unlock()
				if busy {
					t.Fatal("completed stale probe leaked admission lease")
				}
			})
		}
	}
}

// The coordinator is optional for standalone pools; a newer account-auth
// rejection must still fence a probe, while a different model does not.
func TestProbeAuthFenceWithoutCoordinator(t *testing.T) {
	for _, change := range []string{"auth", "other_model"} {
		t.Run(change, func(t *testing.T) {
			p := NewPool(nil, nil)
			old := time.Now().Add(-time.Second)
			p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m", "n"}, ModelDownUntil: map[string]time.Time{"m": old}})
			item, release, ok := p.acquireRoute(RouteQuery{PublicModel: "m"}, routingIdentity{})
			if !ok || !item.modelProbe {
				t.Fatal("missing probe")
			}
			defer release()
			if change == "auth" {
				p.MarkClassified("a", Classified{Kind: KindAuth, Scope: "account", Cooldown: time.Minute})
				// A subsequent account refresh must not roll back the local fence.
				p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m", "n"}})
			} else {
				p.MarkClassified("a", Classified{Kind: KindRateLimit, Scope: "model", Model: "n", Cooldown: time.Minute})
			}
			p.MarkProbeOK(item, "m")
			got, _ := p.ByID("a")
			if change == "auth" && (!got.ModelDownUntil["m"].Equal(old) || got.LastKind != KindAuth) {
				t.Fatalf("new auth did not fence probe: %+v", got)
			}
			if change == "other_model" && (!got.ModelDownUntil["m"].IsZero() || !got.ModelDownUntil["n"].After(time.Now())) {
				t.Fatalf("unrelated model prevented recovery: %+v", got.ModelDownUntil)
			}
		})
	}
}

func TestProbeSuccessReleasesAuthGuardBeforeObserver(t *testing.T) {
	p := NewPool(nil, nil)
	coordinator := &accounts.AuthCoordinator{}
	p.SetAuthCoordinator(coordinator)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
	item, release, ok := p.acquireRoute(RouteQuery{PublicModel: "m"}, routingIdentity{})
	if !ok {
		t.Fatal("missing probe")
	}
	defer release()
	p.SetObserver(func(Item) { _ = coordinator.Version("a") })
	done := make(chan struct{})
	go func() { p.MarkProbeOK(item, "m"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer was called under the auth guard")
	}
}
func TestProbeRecoveryWithCoordinatorKeepsModelIsolation(t *testing.T) {
	for _, target := range []string{"m", "n"} {
		t.Run(target, func(t *testing.T) {
			p := NewPool(nil, nil)
			p.SetAuthCoordinator(&accounts.AuthCoordinator{})
			p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m", "n"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
			item, release, ok := p.acquireRoute(RouteQuery{PublicModel: "m"}, routingIdentity{})
			if !ok || !item.modelProbe {
				t.Fatal("missing probe")
			}
			defer release()
			p.MarkClassified("a", Classified{Kind: KindRateLimit, Scope: "model", Model: target, Cooldown: time.Minute})
			p.MarkProbeOK(item, "m")
			got, _ := p.ByID("a")
			if target == "m" && !got.ModelDownUntil["m"].After(time.Now()) {
				t.Fatal("newer same-model restriction cleared")
			}
			if target == "n" && (!got.ModelDownUntil["m"].IsZero() || !got.ModelDownUntil["n"].After(time.Now())) {
				t.Fatalf("unrelated restriction interfered: %+v", got.ModelDownUntil)
			}
		})
	}
}
