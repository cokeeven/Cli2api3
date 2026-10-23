package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestCredentialPayloadCompareAndSwapPreservesNewLogin(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}

	old := []byte(`{"access_token":"old","refresh_token":"old-refresh","uid":"synthetic"}`)
	login := []byte(`{"access_token":"new-login","refresh_token":"new-refresh","uid":"synthetic"}`)
	refreshed := []byte(`{"access_token":"late-refresh","refresh_token":"late-rotated","uid":"synthetic"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	format, snapshot, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", login); err != nil {
		t.Fatal(err)
	}

	saved, err := database.SaveCredentialPayloadIfCurrent(ctx, account.ID, format, snapshot, "workbuddy-oauth-v1", refreshed)
	if err != nil || saved {
		t.Fatalf("stale refresh saved=%v err=%v, want compare conflict", saved, err)
	}
	_, got, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil || string(got) != string(login) {
		t.Fatalf("new login payload=%q err=%v", got, err)
	}

	saved, err = database.SaveCredentialPayloadIfCurrent(ctx, account.ID, "workbuddy-oauth-v1", login, "workbuddy-oauth-v1", refreshed)
	if err != nil || !saved {
		t.Fatalf("current refresh saved=%v err=%v", saved, err)
	}
}

func TestCredentialRefreshCASRecoversAuthOnlyForCurrentCredentialAndState(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-refresh-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	refreshed := []byte(`{"access_token":"synthetic-refreshed","refresh_token":"synthetic-next"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	if err := database.Observe(ctx, account.ID, "synthetic", "login_required", "synthetic auth failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, refreshed, snapshot.CredentialGeneration)
	if err != nil || !saved {
		t.Fatalf("current successful refresh saved=%v err=%v", saved, err)
	}
	current, err := database.Get(ctx, account.ID)
	if err != nil || current.Status != "ready" || current.LastErrorKind != "" || current.LastError != "" {
		t.Fatalf("auth recovery state=%+v err=%v", current, err)
	}
	_, stored, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil || string(stored) != string(refreshed) {
		t.Fatalf("refreshed payload=%q err=%v", stored, err)
	}
}

func TestCredentialRefreshCASDoesNotOverwriteLaterAuthFailureOrLogin(t *testing.T) {
	for _, scenario := range []string{"later_auth_failure", "new_login_same_payload"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			database, err := OpenStore(filepath.Join(t.TempDir(), "credential-refresh-cas.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
			refreshed := []byte(`{"access_token":"synthetic-stale-refresh","refresh_token":"synthetic-stale-next"}`)
			if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
				t.Fatal(err)
			}
			if err := database.Observe(ctx, account.ID, "synthetic", "login_required", "synthetic auth failure", accounts.KindAuth); err != nil {
				t.Fatal(err)
			}
			format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := database.Get(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "later_auth_failure" {
				if err := database.Observe(ctx, account.ID, "synthetic", "login_required", "newer synthetic auth failure", accounts.KindAuth); err != nil {
					t.Fatal(err)
				}
			} else {
				// Even if a fresh login happens to serialize the same credential bytes,
				// its account generation changes and rejects the in-flight refresh.
				if err := database.SaveCredentialPayload(ctx, account.ID, format, old); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, refreshed, snapshot.CredentialGeneration)
			if err != nil || saved {
				t.Fatalf("stale refresh saved=%v err=%v, want CAS conflict", saved, err)
			}
			_, stored, err := database.LoadCredentialPayload(ctx, account.ID)
			if err != nil || string(stored) != string(old) {
				t.Fatalf("stale refresh changed credential=%q err=%v", stored, err)
			}
			current, err := database.Get(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "later_auth_failure" && (current.Status != "login_required" || current.LastError != "newer synthetic auth failure" || current.LastErrorKind != accounts.KindAuth) {
				t.Fatalf("later auth failure overwritten: %+v", current)
			}
		})
	}
}

func TestObserveAdvancesCredentialGeneration(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-refresh-generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Observe(ctx, account.ID, "synthetic", "login_required", "newer synthetic auth failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	current, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialGeneration != snapshot.CredentialGeneration+1 {
		t.Fatalf("new auth observation did not advance credential generation: before=%d after=%d", snapshot.CredentialGeneration, current.CredentialGeneration)
	}
	format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, []byte(`{"access_token":"stale-refresh"}`), snapshot.CredentialGeneration)
	if err != nil || saved {
		t.Fatalf("stale refresh saved=%v err=%v after newer auth observation", saved, err)
	}
	current, err = database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "login_required" || current.LastError != "newer synthetic auth failure" || current.LastErrorKind != accounts.KindAuth {
		t.Fatalf("new auth observation was overwritten: %+v", current)
	}
}

func TestRecordPoolAuthFailureAdvancesCredentialGeneration(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "pool-auth-generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RecordPoolState(ctx, accounts.PoolState{ID: account.ID, LastError: "newer synthetic auth failure", LastErrorKind: accounts.KindAuth}); err != nil {
		t.Fatal(err)
	}
	current, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialGeneration != snapshot.CredentialGeneration+1 {
		t.Fatalf("pool auth failure did not advance credential generation: before=%d after=%d", snapshot.CredentialGeneration, current.CredentialGeneration)
	}
	format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, []byte(`{"access_token":"stale-refresh"}`), snapshot.CredentialGeneration)
	if err != nil || saved {
		t.Fatalf("stale refresh saved=%v err=%v after runtime auth failure", saved, err)
	}
	current, err = database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastError != "newer synthetic auth failure" || current.LastErrorKind != accounts.KindAuth {
		t.Fatalf("runtime auth failure was overwritten: %+v", current)
	}
}

func TestCredentialRefreshCASClearsAuthWhilePreservingExhaustedQuota(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-quota-auth-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	refreshed := []byte(`{"access_token":"synthetic-refreshed"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	if err := database.Observe(ctx, account.ID, "synthetic", "login_required", "synthetic auth failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveQuota(ctx, account.ID, &accounts.QuotaSnapshot{
		Used: 4, Total: 4, Remaining: 0, Percentage: 100, Unit: "credits", Exceeded: true,
		FetchedAt: "2026-10-23T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	cooldownUntil := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := database.SaveCooldowns(ctx, account.ID, []accounts.CooldownRow{{
		AccountID: account.ID, DownUntil: cooldownUntil, BackoffLevel: 2,
		Kind: accounts.KindRateLimit, Message: "synthetic independent cooldown",
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != "quota_exhausted" || before.LastErrorKind != accounts.KindAuth {
		t.Fatalf("fixture lost independent facts: status=%q kind=%q", before.Status, before.LastErrorKind)
	}
	format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, refreshed, before.CredentialGeneration)
	if err != nil || !saved {
		t.Fatalf("current successful refresh saved=%v err=%v", saved, err)
	}
	after, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "quota_exhausted" {
		t.Errorf("confirmed refresh granted quota: status=%q", after.Status)
	}
	if after.LastErrorKind == accounts.KindAuth {
		t.Errorf("confirmed refresh left stale auth kind; later quota recovery will remain blocked: status=%q kind=%q", after.Status, after.LastErrorKind)
	}
	if after.LastError != "" {
		t.Errorf("confirmed refresh retained stale auth message: %q", after.LastError)
	}
	if after.Quota == nil || !after.Quota.Exceeded || after.Quota.Used != 4 || after.Quota.Total != 4 {
		t.Errorf("confirmed refresh changed exhausted quota snapshot: %+v", after.Quota)
	}
	cooldowns, err := database.LoadCooldowns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cooldowns) != 1 || cooldowns[0].Kind != accounts.KindRateLimit || !cooldowns[0].DownUntil.Equal(cooldownUntil) {
		t.Errorf("confirmed refresh changed independent cooldown: %+v", cooldowns)
	}

	if err := database.SaveQuota(ctx, account.ID, &accounts.QuotaSnapshot{
		Used: 4, Total: 20, Remaining: 16, Percentage: 20, Unit: "credits", Exceeded: false,
		FetchedAt: "2026-10-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "ready" || recovered.LastErrorKind == accounts.KindAuth {
		t.Errorf("quota recovery remains blocked by stale auth: status=%q kind=%q", recovered.Status, recovered.LastErrorKind)
	}
	if recovered.Quota == nil || recovered.Quota.Exceeded || recovered.Quota.Total != 20 {
		t.Errorf("quota recovery snapshot was not applied: %+v", recovered.Quota)
	}
}

func TestCredentialRefreshCASPreservesIndependentErrorWithExhaustedQuota(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-quota-independent-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh"}`)
	refreshed := []byte(`{"access_token":"synthetic-refreshed"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveQuota(ctx, account.ID, &accounts.QuotaSnapshot{
		Used: 4, Total: 4, Remaining: 0, Percentage: 100, Unit: "credits", Exceeded: true,
		FetchedAt: "2026-10-23T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Observe(ctx, account.ID, "synthetic", "quota_exhausted", "synthetic independent provider error", accounts.KindUnavailable); err != nil {
		t.Fatal(err)
	}
	before, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	format, payload, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, account.ID, format, payload, format, refreshed, before.CredentialGeneration)
	if err != nil || !saved {
		t.Fatalf("current successful refresh saved=%v err=%v", saved, err)
	}
	after, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "quota_exhausted" || after.LastError != "synthetic independent provider error" || after.LastErrorKind != accounts.KindUnavailable {
		t.Errorf("confirmed refresh cleared or replaced independent error: status=%q message=%q kind=%q", after.Status, after.LastError, after.LastErrorKind)
	}
	if err := database.SaveQuota(ctx, account.ID, &accounts.QuotaSnapshot{
		Used: 4, Total: 20, Remaining: 16, Percentage: 20, Unit: "credits", Exceeded: false,
		FetchedAt: "2026-10-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "ready" || recovered.LastError != "synthetic independent provider error" || recovered.LastErrorKind != accounts.KindUnavailable {
		t.Errorf("quota recovery changed independent error: status=%q message=%q kind=%q", recovered.Status, recovered.LastError, recovered.LastErrorKind)
	}
}
