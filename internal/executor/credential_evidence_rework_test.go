package executor

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type evidenceTestChat struct {
	nonstream func(context.Context, string) (providers.ChatOutcome, error)
	stream    func(context.Context, string) (*http.Response, providers.ResolvedChat, error)
}

func (c evidenceTestChat) ChatNonStream(ctx context.Context, id string, _ translate.ChatRequest) (providers.ChatOutcome, error) {
	return c.nonstream(ctx, id)
}
func (c evidenceTestChat) ChatStream(ctx context.Context, id string, _ translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return c.stream(ctx, id)
}

type evidenceFixture struct {
	db       *store.Store
	pool     *Pool
	executor ChatExecutor
	account  accounts.Account
	old      []byte
}

func newEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a, err := db.Create(context.Background(), accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old"}`)
	if err := db.SaveCredentialPayload(context.Background(), a.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	p := NewPool(nil, nil)
	p.SetAuthCoordinator(db.AuthCoordinator())
	db.AuthCoordinator().SetRecoveryHook(p.RecoverAuthentication)
	db.AuthCoordinator().SetChangeHook(p.SyncAuthVersion)
	p.Upsert(Item{ID: a.ID, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
	e := NewChatExecutor(p, "")
	e.AuthRecovery = db
	e.Providers = providers.NewRegistry()
	return &evidenceFixture{db: db, pool: p, executor: e, account: a, old: old}
}
func (f *evidenceFixture) refresh(t *testing.T, ctx context.Context) {
	t.Helper()
	generation, version, _, err := f.db.AuthAttemptSnapshot(ctx, f.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.db.SaveCredentialPayloadAfterRefreshFenced(ctx, f.account.ID, "workbuddy-oauth-v1", f.old, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-refreshed"}`), generation, version)
	if err != nil || !saved {
		t.Fatalf("refresh saved=%v err=%v", saved, err)
	}
}
func authRejection() error {
	return &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: "synthetic rejection"}
}

func TestAttemptRefreshEvidenceDoesNotOverrideNewerEvents(t *testing.T) {
	for _, scenario := range []string{"login", "failure", "other_refresh", "same_payload_login"} {
		t.Run(scenario, func(t *testing.T) {
			f := newEvidenceFixture(t)
			f.executor.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: evidenceTestChat{nonstream: func(ctx context.Context, id string) (providers.ChatOutcome, error) {
				if scenario == "other_refresh" || scenario == "same_payload_login" {
					// The exact payload and version are read atomically, then frozen
					// before another request rotates or reuses those same bytes.
					if _, _, err := providers.LoadCredentialPayloadWithEvidence(ctx, f.db, id); err != nil {
						t.Fatal(err)
					}
					providers.FreezeCredentialEvidence(ctx, id)
					if scenario == "other_refresh" {
						f.refresh(t, context.Background())
					} else if err := f.db.SaveCredentialPayload(context.Background(), id, "workbuddy-oauth-v1", f.old); err != nil {
						t.Fatal(err)
					}
					// A nested catalog read must not overwrite chat's frozen evidence.
					if _, _, err := providers.LoadCredentialPayloadWithEvidence(ctx, f.db, id); err != nil {
						t.Fatal(err)
					}
				} else {
					f.refresh(t, ctx)
					if scenario == "login" {
						if err := f.db.SaveCredentialPayload(ctx, id, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-login"}`)); err != nil {
							t.Fatal(err)
						}
					} else {
						f.pool.MarkClassified(id, Classified{Kind: KindAuth, Scope: "account", Message: "newer failure", Cooldown: time.Minute})
					}
				}
				return providers.ChatOutcome{}, authRejection()
			}}})
			item, _ := f.pool.ByID(f.account.ID)
			_, _, err := f.executor.chatInProcessNonStreamAttempt(context.Background(), item, translate.ChatRequest{Model: "m"}, 0, false)
			if err == nil || ClassifyError(err).Kind != KindAuth {
				t.Fatalf("missing typed rejection: %v", err)
			}
			current, _ := f.pool.ByID(f.account.ID)
			if scenario == "failure" {
				if current.LastError != "newer failure" {
					t.Fatalf("newer failure lost: %+v", current)
				}
			} else if current.LastKind == KindAuth {
				t.Fatalf("stale failure applied: %+v", current)
			}
		})
	}
}

func TestStreamAttemptRefreshEvidenceAndNewerLogin(t *testing.T) {
	for _, laterLogin := range []bool{false, true} {
		f := newEvidenceFixture(t)
		f.executor.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: evidenceTestChat{stream: func(ctx context.Context, _ string) (*http.Response, providers.ResolvedChat, error) {
			f.refresh(t, ctx)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, providers.ResolvedChat{}, nil
		}}})
		result, err := f.executor.ChatStreamProxy(context.Background(), translate.ChatRequest{Model: "m"}, f.account.ID, "workbuddy")
		if err != nil || result.Finalize == nil {
			t.Fatalf("stream start err=%v", err)
		}
		if laterLogin {
			if err := f.db.SaveCredentialPayload(context.Background(), f.account.ID, "workbuddy-oauth-v1", []byte(`{"access_token":"synthetic-login"}`)); err != nil {
				t.Fatal(err)
			}
		}
		result.Finalize(StreamCompletion{Err: authRejection()})
		current, _ := f.pool.ByID(f.account.ID)
		if laterLogin && current.LastKind == KindAuth {
			t.Fatalf("old stream failure poisoned login: %+v", current)
		}
		if !laterLogin && current.LastKind != KindAuth {
			t.Fatalf("new credential rejection lost: %+v", current)
		}
	}
}

func TestStreamImmediateAuthFailureAfterOwnRefresh(t *testing.T) {
	f := newEvidenceFixture(t)
	f.executor.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: evidenceTestChat{stream: func(ctx context.Context, _ string) (*http.Response, providers.ResolvedChat, error) {
		f.refresh(t, ctx)
		return nil, providers.ResolvedChat{}, authRejection()
	}}})
	item, _ := f.pool.ByID(f.account.ID)
	_, _, err := f.executor.chatInProcessStreamAttempt(context.Background(), item, translate.ChatRequest{Model: "m"}, nil, 0, false, false)
	current, _ := f.pool.ByID(f.account.ID)
	if err == nil || current.LastKind != KindAuth {
		t.Fatalf("error=%v item=%+v", err, current)
	}
}

func TestLateStreamSuccessDoesNotClearNewerAuthAfterRefresh(t *testing.T) {
	f := newEvidenceFixture(t)
	f.executor.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: evidenceTestChat{stream: func(ctx context.Context, _ string) (*http.Response, providers.ResolvedChat, error) {
		f.refresh(t, ctx)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, providers.ResolvedChat{}, nil
	}}})
	result, err := f.executor.ChatStreamProxy(context.Background(), translate.ChatRequest{Model: "m"}, f.account.ID, "workbuddy")
	if err != nil || result.Finalize == nil {
		t.Fatalf("stream start err=%v", err)
	}
	f.pool.MarkClassified(f.account.ID, Classified{Kind: KindAuth, Scope: "account", Message: "newer failure", Cooldown: time.Minute})
	result.Finalize(StreamCompletion{Completed: true})
	current, _ := f.pool.ByID(f.account.ID)
	if current.LastKind != KindAuth || current.LastError != "newer failure" {
		t.Fatalf("late stream success cleared failure: %+v", current)
	}
}

func TestRefreshThenModelScopedAuthKeepsModelIsolation(t *testing.T) {
	f := newEvidenceFixture(t)
	f.executor.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: evidenceTestChat{nonstream: func(ctx context.Context, _ string) (providers.ChatOutcome, error) {
		f.refresh(t, ctx)
		return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 403, Scope: "model", Message: "synthetic model rejection"}
	}}})
	item, _ := f.pool.ByID(f.account.ID)
	_, _, err := f.executor.chatInProcessNonStreamAttempt(context.Background(), item, translate.ChatRequest{Model: "m"}, 0, false)
	current, _ := f.pool.ByID(f.account.ID)
	if err == nil || current.LastKind == KindAuth || current.ModelLastKind["m"] != KindAuth || !current.DownUntil.IsZero() {
		t.Fatalf("model scope widened: err=%v item=%+v", err, current)
	}
}
