package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func usageStore(t *testing.T) (*Store, accounts.Account) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := s.Create(context.Background(), accounts.CreateAccount{Name: "usage", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}
func reserve(t *testing.T, s *Store, a accounts.Account, id string, tokens int64, at time.Time) {
	t.Helper()
	if err := s.ReserveUsage(context.Background(), accounts.UsageReservation{ID: id, AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: tokens, At: at}); err != nil {
		t.Fatal(err)
	}
}
func guardReason(t *testing.T, err error, want string) {
	t.Helper()
	var guard *accounts.UsageGuardError
	if !errors.As(err, &guard) || guard.Reason != want {
		t.Fatalf("guard error=%v, want %s", err, want)
	}
	switch want {
	case "daily_tokens", "model_daily_tokens", "daily_credits":
		if guard.ResetAt.IsZero() || guard.ResetAt.In(accounts.ChinaTime).Hour() != 0 {
			t.Fatalf("daily guard reset=%v", guard.ResetAt)
		}
	default:
		if !guard.ResetAt.IsZero() {
			t.Fatalf("non-daily guard %s invented reset=%v", want, guard.ResetAt)
		}
	}
}

func TestUsageAtomicDailyLimitAcrossKeys(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyTokens: 100}); err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int32
	var unexpected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: fmt.Sprint(i), KeyID: fmt.Sprint(i % 2), AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 30})
			if err == nil {
				allowed.Add(1)
			} else {
				var g *accounts.UsageGuardError
				if !errors.As(err, &g) {
					unexpected.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 3 || unexpected.Load() != 0 {
		t.Fatalf("allowed=%d unexpected=%d", allowed.Load(), unexpected.Load())
	}
}
func TestUsageSettlementAndRecoveryStayConservative(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now()
	reserve(t, s, a, "before-dispatch", 70, now)
	reserve(t, s, a, "interrupted", 40, now)
	if err := s.MarkUsageDispatched(ctx, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverUsageReservations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyTokens: 100}); err != nil {
		t.Fatal(err)
	}
	reserve(t, s, a, "remaining", 60, now)
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "blocked", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, At: now}), "daily_tokens")
	actual := int64(20)
	credits := 1.0
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "remaining", ActualTokens: &actual, ActualCredits: &credits, UsageSource: "upstream"}); err != nil {
		t.Fatal(err)
	}
	// Finalization is idempotent: a late duplicate cannot erase usage.
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "remaining", NonBillable: true}); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.Today.TokensActual != 20 || view.Today.TokensReserved != 40 || view.Today.UnknownAttempts != 1 {
		t.Fatalf("today=%+v", view.Today)
	}
}
func TestUsageCreditGuardsUnknownCostAndFreeModels(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SaveQuota(ctx, a.ID, &accounts.QuotaSnapshot{Remaining: 100, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ReserveCredits: 20, DailyCredits: 60}); err != nil {
		t.Fatal(err)
	}
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "unknown", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, At: now}), "credit_upper_bound_unknown")
	bound := 50.0
	if err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: "paid", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, CreditUpperBound: &bound, At: now}); err != nil {
		t.Fatal(err)
	}
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "paid2", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, CreditUpperBound: &bound, At: now}), "daily_credits")
	if err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: "free", AccountID: a.ID, Model: "free", EstimatedTokens: 1, FreeModel: true, At: now}); err != nil {
		t.Fatalf("verified free model blocked: %v", err)
	}
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "paid", UsageSource: "unknown"}); err != nil {
		t.Fatal(err)
	}
	view, _ := s.GetUsageAnalysis(ctx, a.ID, 7)
	if view.Today.CreditsActual != 0 || view.Today.CreditsReserved != 50 || view.Today.UnknownAttempts != 1 {
		t.Fatalf("today=%+v", view.Today)
	}
}
func TestUsageReserveBalanceAndUnknownTokens(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SaveQuota(ctx, a.ID, &accounts.QuotaSnapshot{Remaining: 100, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ReserveCredits: 20, DailyTokens: 100}); err != nil {
		t.Fatal(err)
	}
	bound := 81.0
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "too-costly", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, CreditUpperBound: &bound, At: now}), "reserve_credits")
	bound = 1
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "unbounded-tokens", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: -1, CreditUpperBound: &bound, At: now}), "daily_tokens")
}
func TestUsageCSTDayAndModelLimit(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ModelDailyTokens: map[string]int64{"kimi-k3": 10}}); err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 15, 15, 59, 0, 0, time.UTC)
	reserve(t, s, a, "day1", 10, before)
	guardReason(t, s.ReserveUsage(ctx, accounts.UsageReservation{ID: "day1full", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, At: before}), "model_daily_tokens")
	reserve(t, s, a, "day2", 10, before.Add(2*time.Minute))
	if err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: "othermodel", AccountID: a.ID, Model: "other", EstimatedTokens: 20, At: before}); err != nil {
		t.Fatal(err)
	}
}

func TestTraeUsageAnalysisKeepsUnknownAndConfirmedZeroCreditsDistinct(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(context.Background(), accounts.CreateAccount{Name: "trae usage", Provider: "trae", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	view, err := s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertTraeCreditsView(t, view, "no_data", 0, 0, 0)

	now := time.Now()
	reserve(t, s, a, "confirmed-zero", 1, now)
	zero := 0.0
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "confirmed-zero", ActualCredits: &zero, UsageSource: "upstream", At: now}); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertTraeCreditsView(t, view, "complete", 1, 0, 0)

	reserve(t, s, a, "unknown-cost", 1, now)
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "unknown-cost", UsageSource: "unknown", At: now}); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertTraeCreditsView(t, view, "partial", 1, 1, 0)
}

func assertTraeCreditsView(t *testing.T, view accounts.UsageAnalysis, coverage string, known, unknown int, amount float64) {
	t.Helper()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	today, ok := payload["today"].(map[string]any)
	if !ok {
		t.Fatalf("missing today usage in %s", raw)
	}
	if got := today["credits_coverage"]; got != coverage {
		t.Fatalf("credits coverage=%v, want %q; payload=%s", got, coverage, raw)
	}
	if got := today["credits_known_attempts"]; got != float64(known) {
		t.Fatalf("known credit attempts=%v, want %d; payload=%s", got, known, raw)
	}
	if got := today["credits_unknown_attempts"]; got != float64(unknown) {
		t.Fatalf("unknown credit attempts=%v, want %d; payload=%s", got, unknown, raw)
	}
	if got := today["credits_actual"]; got != amount {
		t.Fatalf("confirmed credit subtotal=%v, want %v; payload=%s", got, amount, raw)
	}
	official, ok := payload["trae_official_credits"].(map[string]any)
	if !ok || official["status"] != "unknown" {
		t.Fatalf("official account credits must remain unknown: %s", raw)
	}
	if _, gotOfficialAmount := official["credits"]; gotOfficialAmount {
		t.Fatalf("official account amount must remain unknown; payload=%s", raw)
	}
}

func TestTraeOfficialCreditsCanRepresentExplicitZero(t *testing.T) {
	zero := 0.0
	raw, err := json.Marshal(accounts.TraeOfficialCredits{Status: "complete", Credits: &zero})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got, ok := payload["credits"]; !ok || got != float64(0) {
		t.Fatalf("explicit official zero was not preserved: %s", raw)
	}
}

func TestWorkBuddyUsageAnalysisDoesNotExposeTraeLedger(t *testing.T) {
	s, a := usageStore(t)
	view, err := s.GetUsageAnalysis(context.Background(), a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.TraeOfficialCredits != nil {
		t.Fatalf("WorkBuddy usage unexpectedly contains a Trae ledger: %+v", view.TraeOfficialCredits)
	}
	if view.Today.CreditsCoverage != "no_data" {
		t.Fatalf("empty WorkBuddy usage credit coverage=%q", view.Today.CreditsCoverage)
	}
}
func TestUsageTwoDatabasesRemainIndependent(t *testing.T) {
	s, a := usageStore(t)
	other, b := usageStore(t)
	ctx := context.Background()
	if _, err := other.DB().Exec(`UPDATE accounts SET id=? WHERE id=?`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyTokens: 1}); err != nil {
		t.Fatal(err)
	}
	reserve(t, s, a, "same-attempt", 1, time.Now())
	reserve(t, other, a, "same-attempt", 50, time.Now())
	p, err := other.GetUsagePolicy(ctx, a.ID)
	if err != nil || p.DailyTokens != 0 {
		t.Fatalf("other policy=%+v err=%v", p, err)
	}
}
func TestUsageExpiryAnalysisRejectsBillingCycleAsExpiration(t *testing.T) {
	now := time.Now()
	q := &accounts.QuotaSnapshot{FetchedAt: now.Format(time.RFC3339Nano), Packages: []accounts.QuotaPackage{{ID: "old", Remain: 3, EndsAt: now.Add(-time.Hour).Unix(), ExpirySource: "deduction"}, {ID: "soon", Remain: 5, EndsAt: now.Add(time.Hour).Unix(), ExpirySource: "deduction"}, {ID: "cycle", Remain: 7, EndsAt: now.Add(-time.Hour).Unix(), ExpirySource: "cycle"}, {ID: "inactive", Remain: 9, Status: "inactive"}, {ID: "exhausted", Remain: 9, Status: "exhausted"}, {ID: "empty", Remain: 0}}}
	out := analyzeQuota(q, now)
	if out.Available != 12 || out.Expired != 3 || out.Expiring7d != 5 || out.UnknownExpiry != 7 {
		t.Fatalf("quota=%+v", out)
	}
}
func TestKeyRoutingPolicyRoundTripAndPartialUpdates(t *testing.T) {
	s, _ := usageStore(t)
	ctx := context.Background()
	policy := accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", Priority: 10, MaxInFlight: 2}}, CrossProviderFailover: true}
	k, err := s.InsertAPIKey(ctx, accounts.StoredAPIKey{Name: "key", KeyHash: accounts.HashAPIKey("test-secret"), Enabled: true, Providers: []string{"workbuddy:cn"}, RoutingPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LookupAPIKey(ctx, "test-secret")
	if err != nil || !ok || len(got.RoutingPolicy.Platforms) != 1 || !got.RoutingPolicy.CrossProviderFailover {
		t.Fatalf("key=%+v ok=%v err=%v", got, ok, err)
	}
	merged, err := accounts.ApplyAPIKeyUpdate(got, accounts.UpdateAPIKey{Name: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAPIKey(ctx, accounts.StoredAPIKey{ID: k.ID, Name: merged.Name, Providers: merged.Providers, Enabled: merged.Enabled, RoutingPolicy: merged.RoutingPolicy}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAPIKey(ctx, k.ID)
	if got.Name != "renamed" || got.RoutingPolicy.Platforms[0].MaxInFlight != 2 || len(got.Providers) != 1 {
		t.Fatalf("updated=%+v", got)
	}
}

func TestQuotaHistoryRejectsStaleSnapshotsAndDeduplicates(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	newer := &accounts.QuotaSnapshot{Remaining: 50, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}
	if err := s.SaveQuota(ctx, a.ID, newer); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQuota(ctx, a.ID, newer); err != nil {
		t.Fatal(err)
	}
	older := &accounts.QuotaSnapshot{Remaining: 100, Unit: "credits", FetchedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}
	if err := s.SaveQuota(ctx, a.ID, older); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil || got.Quota.Remaining != 50 {
		t.Fatalf("quota=%+v err=%v", got.Quota, err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM quota_history WHERE account_id=?`, a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("history=%d err=%v", count, err)
	}
	// A confirmed exhausted marker with the same fetch timestamp must persist.
	copy := *newer
	copy.Exceeded = true
	if err := s.SaveQuota(ctx, a.ID, &copy); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, a.ID)
	if !got.Quota.Exceeded {
		t.Fatal("exhaustion marker lost")
	}
}

func TestAttemptUsageRoutingFieldsRoundTrip(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	if err := s.InsertRequestLog(ctx, accounts.RequestLog{ID: "routing-log", RequestedModel: "kimi-k3"}); err != nil {
		t.Fatal(err)
	}
	credit := 2.5
	want := accounts.RequestAttempt{ID: "routing-attempt", RequestID: "routing-log", AccountID: a.ID, Provider: "workbuddy", Model: "kimi-k3", KeyID: "key1", Credits: &credit, UsageState: "actual", ErrorScope: "model", RouteReason: "quota_migration"}
	if err := s.InsertRequestAttempt(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.listRequestAttempts(ctx, want.RequestID)
	if err != nil || len(got) != 1 {
		t.Fatalf("attempts=%v err=%v", got, err)
	}
	if got[0].Provider != want.Provider || got[0].Model != want.Model || got[0].KeyID != want.KeyID || got[0].Credits == nil || *got[0].Credits != credit || got[0].UsageState != want.UsageState || got[0].RouteReason != want.RouteReason || got[0].ErrorScope != want.ErrorScope {
		t.Fatalf("attempt=%+v", got[0])
	}
}

func TestUsageBalanceReconciliationPreservesUnknownCostAndDailyGuard(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bound := 50.0
	if err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: "unknown-cost", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 10, CreditUpperBound: &bound, At: now.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUsageDispatched(ctx, "unknown-cost"); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleUsage(ctx, accounts.UsageSettlement{ID: "unknown-cost", At: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	q := &accounts.QuotaSnapshot{Remaining: 80, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}
	if err := s.SaveQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ReserveCredits: 20}); err != nil {
		t.Fatal(err)
	}
	newBound := 30.0
	r := accounts.UsageReservation{ID: "new", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, CreditUpperBound: &newBound, At: now}
	guardReason(t, s.ReserveUsage(ctx, r), "reserve_credits")
	if _, err := s.ReconcileUsageBalance(ctx, a.ID, now.Add(-time.Hour).Format(time.RFC3339Nano)); err == nil {
		t.Fatal("stale snapshot acknowledged")
	}
	if _, err := s.ReconcileUsageBalance(ctx, a.ID, now.Add(-time.Minute).Format(time.RFC3339Nano)); err == nil {
		t.Fatal("mismatched snapshot acknowledged")
	}
	count, err := s.ReconcileUsageBalance(ctx, a.ID, q.FetchedAt)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	view, err := s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.Today.UnknownAttempts != 1 || view.Today.CreditsActual != 0 || view.Today.CreditsReserved != 50 || view.CreditBalancePending != 0 {
		t.Fatalf("analysis=%+v", view)
	}
	if err = s.ReserveUsage(ctx, r); err != nil {
		t.Fatalf("balance liability not released: %v", err)
	}
	if err = s.SettleUsage(ctx, accounts.UsageSettlement{ID: r.ID, NonBillable: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyCredits: 60}); err != nil {
		t.Fatal(err)
	}
	r.ID = "daily-still-reserved"
	guardReason(t, s.ReserveUsage(ctx, r), "daily_credits")
}

func TestUsageAnalysisIncludesBalanceHistoryAndTruthfulGuardRecovery(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bound := 5.0
	if err := s.ReserveUsage(ctx, accounts.UsageReservation{ID: "held", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 10, CreditUpperBound: &bound, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQuota(ctx, a.ID, &accounts.QuotaSnapshot{Remaining: 20, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ReserveCredits: 20, DailyTokens: 10, DailyCredits: 5, ModelDailyTokens: map[string]int64{"kimi-k3": 5}}); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.QuotaHistory) != 1 || !view.QuotaHistory[0].Known || view.QuotaHistory[0].Available != 20 || view.QuotaHistory[0].Unit != "credits" {
		t.Fatalf("history=%+v", view.QuotaHistory)
	}
	states := map[string]accounts.UsageGuardState{}
	for _, state := range view.GuardStates {
		states[state.Reason] = state
	}
	for _, reason := range []string{"daily_tokens", "daily_credits", "model_daily_tokens"} {
		state, ok := states[reason]
		if !ok || state.ResetAt == "" {
			t.Fatalf("missing daily recovery %s: %+v", reason, view.GuardStates)
		}
		reset, err := time.Parse(time.RFC3339, state.ResetAt)
		if err != nil || reset.In(accounts.ChinaTime).Hour() != 0 {
			t.Fatalf("reset=%s err=%v", state.ResetAt, err)
		}
	}
	state, ok := states["reserve_credits"]
	if !ok || state.ResetAt != "" || state.Scope != "paid_models" {
		t.Fatalf("invented balance reset: %+v", state)
	}
	if view.Today.CreditsActual != 0 || view.Today.CreditsReserved != 5 {
		t.Fatal("balance history was treated as request cost")
	}
}

func TestUsageBalanceHistoryUsesSnapshotTimeForExpiration(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now()
	q := &accounts.QuotaSnapshot{Unit: "credits", FetchedAt: now.Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano), Packages: []accounts.QuotaPackage{{Remain: 5, EndsAt: now.Add(-24 * time.Hour).Unix(), ExpirySource: "deduction"}}}
	if err := s.SaveQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetUsageAnalysis(ctx, a.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.Quota.Available != 0 || len(view.QuotaHistory) != 1 || view.QuotaHistory[0].Available != 5 {
		t.Fatalf("current=%+v history=%+v", view.Quota, view.QuotaHistory)
	}
}

func TestUsageGuardErrorOnlyDailyBudgetsDeclareMidnightReset(t *testing.T) {
	s, a := usageStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bound := 2.0
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{ReserveCredits: 5}); err != nil {
		t.Fatal(err)
	}
	r := accounts.UsageReservation{ID: "unknown-balance", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 1, CreditUpperBound: &bound, At: now}
	guardReason(t, s.ReserveUsage(ctx, r), "credit_balance_unknown")
	q := &accounts.QuotaSnapshot{Remaining: 6, Unit: "credits", FetchedAt: now.Add(-30 * time.Minute).Format(time.RFC3339Nano)}
	if err := s.SaveQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	r.ID = "stale-balance"
	guardReason(t, s.ReserveUsage(ctx, r), "credit_balance_stale")
	q.FetchedAt = now.Format(time.RFC3339Nano)
	if err := s.SaveQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	r.ID = "reserve-floor"
	guardReason(t, s.ReserveUsage(ctx, r), "reserve_credits")
	r.ID = "unknown-cost"
	r.CreditUpperBound = nil
	guardReason(t, s.ReserveUsage(ctx, r), "credit_upper_bound_unknown")
	if err := s.SetUsagePolicy(ctx, a.ID, accounts.UsagePolicy{DailyTokens: 1}); err != nil {
		t.Fatal(err)
	}
	r.ID = "daily-tokens"
	r.EstimatedTokens = 2
	guardReason(t, s.ReserveUsage(ctx, r), "daily_tokens")
}
