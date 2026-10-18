package runtime_test

import (
	"context"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	accountruntime "github.com/caigee-cmd/cli2api/internal/runtime"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestMainIndependentModelCooldownReasonDoesNotCrossModels(t *testing.T) {
	ctx := context.Background()
	db, err := sqlstore.OpenStore(filepath.Join(t.TempDir(), "synthetic-cooldown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic-only", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	manager := accountruntime.NewManager(accountruntime.ManagerConfig{DataDir: t.TempDir()}, db, &fakeStarter{})
	defer manager.Close()
	pool := manager.Pool()
	pool.Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a", "model-b"}})
	pool.MarkClassified(account.ID, executor.Classified{Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "6004", Cooldown: time.Hour, Message: "synthetic-model-a-limit"})
	pool.MarkClassified(account.ID, executor.Classified{Kind: accounts.KindRateLimit, Scope: "model", Model: "model-b", Code: "3004", Cooldown: 2 * time.Hour, Message: "synthetic-model-b-limit"})
	manager.Flush()
	rows, err := db.LoadCooldowns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.AccountID == account.ID && row.Model == "model-a" {
			found = true
			if row.Message != "synthetic-model-a-limit" {
				t.Fatalf("model-a persisted wrong failure reason: got %q; expected its own message; later model-b overwrote it", row.Message)
			}
		}
	}
	if !found {
		t.Fatal("missing synthetic model-a cooldown")
	}
}
