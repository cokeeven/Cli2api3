package runtime_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	accountruntime "github.com/caigee-cmd/cli2api/internal/runtime"
	sqlstore "github.com/caigee-cmd/cli2api/internal/store"
)

func TestMainQuotaWithoutResetRetainsItsOwnRestrictionEvidence(t *testing.T) {
	for _, priorAuth := range []bool{false, true} {
		name := "no-prior-cooldown"
		if priorAuth {
			name = "prior-auth-cooldown"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "synthetic-no-reset.db")
			db, err := sqlstore.OpenStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic-only", Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			otherAccount, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic-other", Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			legacyQuota := &accounts.QuotaSnapshot{Exceeded: true, FetchedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := db.SaveQuota(ctx, otherAccount.ID, legacyQuota); err != nil {
				t.Fatal(err)
			}
			otherAccount, err = db.Get(ctx, otherAccount.ID)
			if err != nil {
				t.Fatal(err)
			}
			if otherAccount.Quota == nil || otherAccount.Quota.RestrictionEvidence != nil {
				t.Fatalf("legacy quota snapshot must remain unknown, got %+v", otherAccount.Quota)
			}

			// Seed a prior successful balance so the quota classification must
			// persist its evidence alongside existing quota JSON.
			if err := db.SaveQuota(ctx, account.ID, &accounts.QuotaSnapshot{
				Remaining: 25, Unit: "credits", FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}); err != nil {
				t.Fatal(err)
			}
			account, err = db.Get(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}

			manager := accountruntime.NewManager(accountruntime.ManagerConfig{DataDir: t.TempDir()}, db, &fakeStarter{})
			managerClosed := false
			defer func() {
				if !managerClosed {
					_ = manager.Close()
				}
			}()
			ready := true
			pool := manager.Pool()
			pool.Upsert(executor.Item{ID: account.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}, Ready: &ready, Quota: account.Quota})
			pool.Upsert(executor.Item{ID: otherAccount.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}, Ready: &ready, Quota: otherAccount.Quota})

			var authDownUntil time.Time
			if priorAuth {
				auth := executor.ClassifyError(&providers.Error{
					Kind: accounts.KindAuth, Status: 401, Code: "1001", Scope: "account", Message: "synthetic earlier auth rejection",
				})
				pool.MarkClassified(account.ID, auth)
				beforeQuota, ok := pool.ByID(account.ID)
				if !ok {
					t.Fatal("synthetic account missing from pool")
				}
				authDownUntil = beforeQuota.DownUntil
			}

			quota := executor.ClassifyError(&providers.Error{
				Kind: accounts.KindQuota, Status: 402, Code: "14018", Scope: "account", RetrySafe: true, Message: "synthetic insufficient credit",
			})
			if quota.Cooldown != 0 || quota.UpstreamResetAt != nil {
				t.Fatalf("fixture must have unknown reset and no timed cooldown: %+v", quota)
			}
			pool.MarkClassified(account.ID, quota)
			first, ok := pool.ByID(account.ID)
			if !ok || first.Quota == nil || first.Quota.RestrictionEvidence == nil {
				t.Fatalf("missing first quota restriction evidence: %+v", first.Quota)
			}
			firstObservedAt := first.Quota.RestrictionEvidence.FirstObservedAt
			time.Sleep(2 * time.Millisecond)
			pool.MarkClassified(account.ID, quota)
			manager.Flush()

			rows := pool.InspectRoute(executor.RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "model-a"})
			byID := make(map[string]executor.RouteCandidate, len(rows))
			for _, row := range rows {
				byID[row.ID] = row
			}
			quotaRoute, ok := byID[account.ID]
			if !ok || quotaRoute.Reason != "quota_exhausted" || quotaRoute.AccountQuotaEvidence == nil {
				t.Fatalf("expected quota exclusion with independent evidence: %+v", quotaRoute)
			}
			evidence := quotaRoute.AccountQuotaEvidence
			if evidence.Code != "14018" || evidence.Source != "provider_adapter_error" || evidence.Scope != "account" || evidence.ReportedScope != "account" {
				t.Fatalf("quota evidence does not describe this account restriction: %+v", evidence)
			}
			if evidence.UpstreamResetAt != nil {
				t.Fatalf("unknown upstream reset was fabricated: %+v", evidence.UpstreamResetAt)
			}
			encodedRoute, err := json.Marshal(quotaRoute)
			if err != nil {
				t.Fatal(err)
			}
			var routeFields map[string]json.RawMessage
			if err := json.Unmarshal(encodedRoute, &routeFields); err != nil {
				t.Fatal(err)
			}
			var quotaFields map[string]json.RawMessage
			if err := json.Unmarshal(routeFields["account_quota_evidence"], &quotaFields); err != nil {
				t.Fatal(err)
			}
			if _, hasLocalDeadline := quotaFields["local_backoff_until"]; hasLocalDeadline || strings.Contains(string(routeFields["account_quota_evidence"]), "synthetic insufficient credit") {
				t.Fatalf("quota diagnostics exposed provider message or a fabricated local deadline: %s", routeFields["account_quota_evidence"])
			}
			if evidence.FirstObservedAt == nil || firstObservedAt == nil || !evidence.FirstObservedAt.Equal(*firstObservedAt) || evidence.LastObservedAt == nil || !evidence.LastObservedAt.After(*evidence.FirstObservedAt) {
				t.Fatalf("quota evidence observation window is inconsistent: %+v", evidence)
			}
			otherRoute, ok := byID[otherAccount.ID]
			if !ok || otherRoute.Reason != "quota_exhausted" || otherRoute.AccountQuotaEvidence != nil {
				t.Fatalf("legacy/account-isolated quota evidence was guessed or crossed accounts: %+v", otherRoute)
			}
			if priorAuth {
				if quotaRoute.AccountCooldownUntil == nil || !quotaRoute.AccountCooldownUntil.Equal(authDownUntil) || quotaRoute.AccountCooldownEvidence == nil || quotaRoute.AccountCooldownEvidence.Code != "1001" {
					t.Fatalf("quota observation replaced the earlier auth cooldown: %+v", quotaRoute)
				}
			} else if quotaRoute.AccountCooldownUntil != nil || quotaRoute.AccountCooldownEvidence != nil {
				t.Fatalf("no timer was reported as a cooldown: %+v", quotaRoute)
			}

			persistedAccount, err := db.Get(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			persistedEvidence := persistedAccount.Quota.RestrictionEvidence
			if persistedEvidence == nil || persistedEvidence.Code != "14018" || persistedEvidence.Source != "provider_adapter_error" || persistedEvidence.UpstreamResetAt != nil || persistedEvidence.FirstObservedAt == nil || persistedEvidence.LastObservedAt == nil || !persistedEvidence.FirstObservedAt.Equal(*firstObservedAt) || !persistedEvidence.LastObservedAt.After(*persistedEvidence.FirstObservedAt) {
				t.Fatalf("quota evidence did not persist in quota snapshot: %+v", persistedEvidence)
			}

			cooldowns, err := db.LoadCooldowns(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var accountCooldown *accounts.CooldownRow
			for i := range cooldowns {
				if cooldowns[i].AccountID == account.ID && cooldowns[i].Model == "" {
					accountCooldown = &cooldowns[i]
					break
				}
			}
			if priorAuth {
				if accountCooldown == nil || accountCooldown.Kind != accounts.KindAuth || accountCooldown.Message != "synthetic earlier auth rejection" || accountCooldown.Evidence.Code != "1001" || !accountCooldown.DownUntil.Equal(authDownUntil) {
					t.Fatalf("quota kind/evidence was incorrectly paired with the existing auth timer: %+v", accountCooldown)
				}
			} else if accountCooldown != nil {
				t.Fatalf("no-reset quota created a local cooldown row: %+v", accountCooldown)
			}

			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			managerClosed = true
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := sqlstore.OpenStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restartedAccount, err := reopened.Get(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if restartedAccount.Quota == nil || restartedAccount.Quota.RestrictionEvidence == nil || restartedAccount.Quota.RestrictionEvidence.Code != "14018" {
				t.Fatalf("restart did not recover quota restriction evidence: %+v", restartedAccount.Quota)
			}
			restarted := accountruntime.NewManager(accountruntime.ManagerConfig{DataDir: t.TempDir()}, reopened, &fakeStarter{})
			defer restarted.Close()
			restarted.Pool().Upsert(executor.Item{ID: restartedAccount.ID, Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}, Ready: &ready, Quota: restartedAccount.Quota})
			restarted.TestRestoreCooldowns(ctx)
			restartedRows := restarted.Pool().InspectRoute(executor.RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "model-a"})
			if len(restartedRows) != 1 || restartedRows[0].Reason != "quota_exhausted" || restartedRows[0].AccountQuotaEvidence == nil || restartedRows[0].AccountQuotaEvidence.Code != "14018" {
				t.Fatalf("restarted route lost quota evidence: %+v", restartedRows)
			}
		})
	}
}
