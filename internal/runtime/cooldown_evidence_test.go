package runtime_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	accountruntime "github.com/caigee-cmd/cli2api/internal/runtime"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

func TestCooldownEvidencePersistsPerScopeAndRestoresAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cooldown-evidence.db")
	store, err := sqlstore.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	accountA, err := store.Create(ctx, accounts.CreateAccount{Name: "synthetic-a", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	accountB, err := store.Create(ctx, accounts.CreateAccount{Name: "synthetic-b", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	manager := accountruntime.NewManager(accountruntime.ManagerConfig{DataDir: t.TempDir()}, store, &fakeStarter{})
	managerClosed := false
	defer func() {
		if !managerClosed {
			_ = manager.Close()
		}
	}()
	manager.Pool().Upsert(executor.Item{ID: accountA.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a", "model-b"}})
	manager.Pool().Upsert(executor.Item{ID: accountB.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}})

	resetA := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)
	manager.Pool().MarkClassified(accountA.ID, executor.Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_A_FIRST",
		EvidenceSource: "provider_adapter_error", UpstreamResetAt: &resetA, Cooldown: time.Hour, Message: "model-a-first",
	})
	first, _ := manager.Pool().ByID(accountA.ID)
	firstObserved := first.CooldownEvidence["model-a"].FirstObservedAt
	time.Sleep(3 * time.Millisecond)
	manager.Pool().MarkClassified(accountA.ID, executor.Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_A_LATEST",
		EvidenceSource: "provider_adapter_error", Cooldown: time.Hour, Message: "model-a-latest-no-reset",
	})
	resetB := time.Now().UTC().Add(5 * time.Hour).Truncate(time.Second)
	manager.Pool().MarkClassified(accountA.ID, executor.Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-b", Code: "SYNTHETIC_B",
		EvidenceSource: "provider_adapter_error", UpstreamResetAt: &resetB, Cooldown: 2 * time.Hour, Message: "model-b-with-reset",
	})
	manager.Pool().MarkClassified(accountA.ID, executor.Classified{
		Kind: accounts.KindUnavailable, Scope: "account", Code: "SYNTHETIC_ACCOUNT_A",
		EvidenceSource: "local_runtime", Cooldown: 3 * time.Hour, Message: "account-a-only",
	})
	manager.Pool().MarkClassified(accountB.ID, executor.Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_OTHER_ACCOUNT",
		EvidenceSource: "provider_adapter_error", Cooldown: 4 * time.Hour, Message: "account-b-model-a",
	})
	manager.Flush()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	managerClosed = true
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlstore.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.LoadCooldowns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]accounts.CooldownRow{}
	for _, row := range rows {
		byKey[row.AccountID+"/"+row.Model] = row
	}
	rowA := byKey[accountA.ID+"/model-a"]
	if rowA.Message != "model-a-latest-no-reset" || rowA.Evidence.Code != "SYNTHETIC_A_LATEST" || rowA.Evidence.Scope != "model" || rowA.Evidence.Source != "provider_adapter_error" {
		t.Fatalf("model-a evidence was overwritten or lost: %+v", rowA)
	}
	if !rowA.Evidence.FirstObservedAt.Equal(firstObserved) || !rowA.Evidence.LastObservedAt.After(firstObserved) || rowA.Evidence.UpstreamResetAt != nil || !rowA.Evidence.LocalBackoffUntil.Equal(rowA.DownUntil) {
		t.Fatalf("model-a timestamps/reset/local deadline are inconsistent: %+v", rowA.Evidence)
	}
	rowB := byKey[accountA.ID+"/model-b"]
	if rowB.Message != "model-b-with-reset" || rowB.Evidence.Code != "SYNTHETIC_B" || rowB.Evidence.Scope != "model" || rowB.Evidence.UpstreamResetAt == nil || !rowB.Evidence.UpstreamResetAt.Equal(resetB) {
		t.Fatalf("model-b evidence was overwritten or lost: %+v", rowB)
	}
	rowAccount := byKey[accountA.ID+"/"]
	if rowAccount.Message != "account-a-only" || rowAccount.Evidence.Code != "SYNTHETIC_ACCOUNT_A" || rowAccount.Evidence.Scope != "account" || rowAccount.Evidence.Source != "local_runtime" {
		t.Fatalf("account-scoped evidence was not preserved separately: %+v", rowAccount)
	}
	rowOtherAccount := byKey[accountB.ID+"/model-a"]
	if rowOtherAccount.Message != "account-b-model-a" || rowOtherAccount.Evidence.Code != "SYNTHETIC_OTHER_ACCOUNT" {
		t.Fatalf("account B evidence crossed into account A: %+v", rowOtherAccount)
	}

	restored := accountruntime.NewManager(accountruntime.ManagerConfig{DataDir: t.TempDir()}, reopened, &fakeStarter{})
	restoredClosed := false
	defer func() {
		if !restoredClosed {
			_ = restored.Close()
		}
	}()
	restored.Pool().Upsert(executor.Item{ID: accountA.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a", "model-b"}})
	restored.Pool().Upsert(executor.Item{ID: accountB.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}})
	restored.TestRestoreCooldowns(ctx)
	itemA, _ := restored.Pool().ByID(accountA.ID)
	if got := itemA.CooldownEvidence["model-a"]; got.Code != "SYNTHETIC_A_LATEST" || got.Message != "model-a-latest-no-reset" || got.Scope != "model" || got.UpstreamResetAt != nil {
		t.Fatalf("restored model-a evidence mismatch: %+v", got)
	}
	if got := itemA.CooldownEvidence[""]; got.Code != "SYNTHETIC_ACCOUNT_A" || got.Message != "account-a-only" {
		t.Fatalf("restored account evidence mismatch: %+v", got)
	}

	// Ordinary quota refresh and model-b success cannot clear model-a. A
	// success carrying an older state version cannot clear a later rejection.
	staleVersion := itemA.StateVersion
	restored.Pool().MergeQuota(accountA.ID, &executor.QuotaSnapshot{FetchedAt: time.Now().UTC().Format(time.RFC3339)})
	restored.Pool().MarkOK(accountA.ID, "model-b")
	restored.Pool().MarkClassified(accountA.ID, executor.Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_A_NEWER",
		EvidenceSource: "provider_adapter_error", Cooldown: time.Hour, Message: "newer-model-a",
	})
	restored.Pool().MarkOKVersion(accountA.ID, "model-a", staleVersion)
	itemA, _ = restored.Pool().ByID(accountA.ID)
	if got := itemA.CooldownEvidence["model-a"]; got.Code != "SYNTHETIC_A_NEWER" || got.Message != "newer-model-a" || itemA.ModelDownUntil["model-a"].IsZero() {
		t.Fatalf("quota refresh, another-model success, or stale success cleared newer model-a state: %+v", itemA)
	}
	if _, exists := itemA.CooldownEvidence["model-b"]; exists {
		t.Fatal("model-b success did not clear its matching evidence")
	}

	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	restoredClosed = true
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
