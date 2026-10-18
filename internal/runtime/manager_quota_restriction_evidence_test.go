package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

func TestPersistQuotaRefreshRetainsRestrictionEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := sqlstore.OpenStore(filepath.Join(t.TempDir(), "quota-restriction-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic-quota", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	firstObserved := time.Now().UTC().Add(-time.Minute)
	lastObserved := firstObserved.Add(30 * time.Second)
	initial := &accounts.QuotaSnapshot{
		Exceeded:  true,
		FetchedAt: firstObserved.Format(time.RFC3339Nano),
		RestrictionEvidence: &accounts.QuotaRestrictionEvidence{
			FirstObservedAt: &firstObserved,
			LastObservedAt:  &lastObserved,
			Code:            "14018",
			Source:          "provider_adapter_error",
			Scope:           "account",
			ReportedScope:   "account",
		},
	}
	if err := db.SaveQuota(ctx, account.ID, initial); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer manager.Close()
	manager.Pool().Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Quota: initial})
	refreshedAt := time.Now().UTC().Format(time.RFC3339Nano)
	manager.persistQuota(ctx, account.ID, &accounts.QuotaSnapshot{
		Remaining: 42, Unit: "credits", FetchedAt: refreshedAt,
	})
	manager.Flush()

	persisted, err := db.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Quota == nil || persisted.Quota.Remaining != 42 || persisted.Quota.FetchedAt != refreshedAt {
		t.Fatalf("new provider quota snapshot was not persisted: %+v", persisted.Quota)
	}
	evidence := persisted.Quota.RestrictionEvidence
	if evidence == nil || evidence.Code != "14018" || evidence.FirstObservedAt == nil || evidence.LastObservedAt == nil {
		t.Fatalf("provider refresh erased gateway quota restriction evidence: %+v", evidence)
	}
	if !evidence.FirstObservedAt.Equal(firstObserved) || !evidence.LastObservedAt.Equal(lastObserved) || evidence.UpstreamResetAt != nil {
		t.Fatalf("provider refresh changed observed timestamps or fabricated reset: %+v", evidence)
	}
}
