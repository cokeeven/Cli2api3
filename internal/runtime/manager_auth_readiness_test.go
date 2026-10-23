package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

type localCredentialReadyProber struct{}

func (localCredentialReadyProber) Probe(context.Context, string) (providers.AccountHealth, error) {
	return providers.AccountHealth{Ready: true, Hot: true}, nil
}
func (localCredentialReadyProber) Quota(context.Context, string) (*providers.QuotaInfo, error) {
	return nil, nil
}

func TestBackgroundCredentialProbeDoesNotClearAuthFailureButReloginCan(t *testing.T) {
	for _, providerID := range []string{"workbuddy", "trae"} {
		t.Run(providerID, func(t *testing.T) {
			testBackgroundCredentialProbeDoesNotClearAuthFailureButReloginCan(t, providerID)
		})
	}
}

func testBackgroundCredentialProbeDoesNotClearAuthFailureButReloginCan(t *testing.T, providerID string) {
	ctx := context.Background()
	store, err := sqlstore.OpenStore(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.Create(ctx, accounts.CreateAccount{Name: "synthetic-" + providerID, Provider: providerID, Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, account.ID, "synthetic-uid", "login_required", "synthetic upstream auth rejection", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, store, nil)
	defer manager.Close()
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: providerID, Prober: localCredentialReadyProber{}})
	manager.SetProviders(registry)
	ready := false
	manager.Pool().Upsert(executor.Item{
		ID: account.ID, Provider: providerID, Region: "cn", Runtime: string(providers.RuntimeInProcess),
		Ready: &ready, Hot: &ready, RuntimeState: "starting",
	})

	item, _ := manager.Pool().ByID(account.ID)
	if err := manager.refreshInProcessAt(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	afterProbe, _ := manager.Pool().ByID(account.ID)
	if afterProbe.Ready == nil || *afterProbe.Ready || afterProbe.Hot == nil || *afterProbe.Hot {
		t.Fatalf("local credential presence cleared upstream auth failure: %+v", afterProbe)
	}
	stored, err := store.Get(ctx, account.ID)
	if err != nil || stored.Status != "login_required" || stored.LastErrorKind != accounts.KindAuth {
		t.Fatalf("auth state was overwritten: status=%q kind=%q err=%v", stored.Status, stored.LastErrorKind, err)
	}

	// A completed login writes an explicit ready observation. The same local
	// credential probe may then restore runtime readiness without a sticky lock.
	if err := store.Observe(ctx, account.ID, "synthetic-uid", "ready", "", ""); err != nil {
		t.Fatal(err)
	}
	item, _ = manager.Pool().ByID(account.ID)
	if err := manager.refreshInProcessAt(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	afterLogin, _ := manager.Pool().ByID(account.ID)
	if afterLogin.Ready == nil || !*afterLogin.Ready || afterLogin.Hot == nil || !*afterLogin.Hot {
		t.Fatalf("explicit relogin did not restore readiness: %+v", afterLogin)
	}
	stored, err = store.Get(ctx, account.ID)
	if err != nil || stored.Status != "ready" || stored.LastErrorKind != "" {
		t.Fatalf("relogin state not retained: status=%q kind=%q err=%v", stored.Status, stored.LastErrorKind, err)
	}
}
