package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// The top-priority upgrade guarantee: a snapshot must carry every piece of
// user state that matters 闁?accounts, credentials, API keys, provider grants,
// routing policy, usage guards, proxy settings, model settings, logs, and
// maintenance history 闁?and restoring it must bring all of it back. Nothing
// here touches the production data directory: every database is a temp file.
func TestBackupRestoreRoundTripKeepsAllUserState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	source, err := OpenStore(filepath.Join(root, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	account, err := source.Create(ctx, accounts.CreateAccount{
		Name: "Trae CN", Provider: "trae", Region: "cn", Enabled: true,
		MaxInFlight: 6, Priority: 80, ProxyURL: "socks5://127.0.0.1:1080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SaveCredentialPayload(ctx, account.ID, "trae-oauth-v1", []byte(`{"access_token":"secret-at","refresh_token":"secret-rt"}`)); err != nil {
		t.Fatal(err)
	}
	if err := source.SetSecret(ctx, "proxy_api_key", "console-secret"); err != nil {
		t.Fatal(err)
	}
	if err := source.SetUsagePolicy(ctx, account.ID, accounts.UsagePolicy{ReserveCredits: 12.5, DailyTokens: 100000, PreferExpiring: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.InsertAPIKey(ctx, accounts.StoredAPIKey{
		ID: "key-1", Name: "client", Prefix: "sk-abc", KeyHash: "hash-1", Enabled: true,
		Providers: []string{"trae:cn"},
		RoutingPolicy: accounts.KeyRoutingPolicy{
			Platforms:             []accounts.PlatformRoutePolicy{{Provider: "trae", Region: "cn", Priority: 90, MaxInFlight: 3}},
			CrossProviderFailover: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetModelContext(ctx, "trae/claude-sonnet-4", 200000); err != nil {
		t.Fatal(err)
	}
	if err := source.SetProviderModelSetting(ctx, "trae", "claude-sonnet-4", accounts.ProviderModelSetting{MaxMode: true, ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := source.InsertRequestLog(ctx, accounts.RequestLog{
		ID: "log-1", AccountID: account.ID, Provider: "trae", RequestedModel: "claude-sonnet-4",
		Status: "ok", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordCheckin(ctx, account.ID, "success", "claimed 200", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ClaimDailyTaskRun(ctx, account.ID, time.Now().UTC().Format("2006-01-02"), "manual", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}

	backup, err := source.Backup(ctx, filepath.Join(root, "backups"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.VerifyBackup(ctx, backup.Path); err != nil {
		t.Fatalf("the snapshot must verify before any restore: %v", err)
	}

	// Simulate a rollback restore: copy the verified snapshot over a fresh
	// database, exactly as the updater does inside the container.
	restoredPath := filepath.Join(root, "restored.db")
	data, readErr := os.ReadFile(backup.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := os.WriteFile(restoredPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	got, err := restored.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("account lost: %v", err)
	}
	if got.Name != "Trae CN" || got.Provider != "trae" || got.ProviderRegion != "cn" ||
		got.MaxInFlight != 6 || got.Priority != 80 || !got.Enabled || got.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("account settings lost: %+v", got)
	}
	format, payload, err := restored.LoadCredentialPayload(ctx, account.ID)
	if err != nil || format != "trae-oauth-v1" || string(payload) != `{"access_token":"secret-at","refresh_token":"secret-rt"}` {
		t.Fatalf("credential lost: format=%q payload=%q err=%v", format, payload, err)
	}
	if secret, ok, err := restored.GetSecret(ctx, "proxy_api_key"); err != nil || !ok || secret != "console-secret" {
		t.Fatalf("secret lost: %q ok=%v err=%v", secret, ok, err)
	}
	policy, err := restored.GetUsagePolicy(ctx, account.ID)
	if err != nil || policy.ReserveCredits != 12.5 || policy.DailyTokens != 100000 || !policy.PreferExpiring {
		t.Fatalf("usage guards lost: %+v err=%v", policy, err)
	}
	keys, err := restored.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != "key-1" || !keys[0].Enabled {
		t.Fatalf("api key lost: %+v err=%v", keys, err)
	}
	if len(keys[0].Providers) != 1 || keys[0].Providers[0] != "trae:cn" {
		t.Fatalf("provider grant lost: %+v", keys[0].Providers)
	}
	if len(keys[0].RoutingPolicy.Platforms) != 1 || keys[0].RoutingPolicy.Platforms[0].MaxInFlight != 3 || !keys[0].RoutingPolicy.CrossProviderFailover {
		t.Fatalf("routing policy lost: %+v", keys[0].RoutingPolicy)
	}
	if length, ok, err := restored.GetModelContext(ctx, "trae/claude-sonnet-4"); err != nil || !ok || length != 200000 {
		t.Fatalf("model context lost: %d ok=%v err=%v", length, ok, err)
	}
	setting, err := restored.GetProviderModelSetting(ctx, "trae", "claude-sonnet-4")
	if err != nil || !setting.MaxMode || setting.ReasoningEffort != "high" {
		t.Fatalf("model setting lost: %+v err=%v", setting, err)
	}
	logs, err := restored.ListRequestLogs(ctx, accounts.RequestLogFilter{AccountID: account.ID, Limit: 10})
	if err != nil || len(logs.Items) != 1 || logs.Items[0].ID != "log-1" {
		t.Fatalf("request log lost: %+v err=%v", logs, err)
	}
	checkins, err := restored.ListCheckinRecords(ctx, account.ID, 10)
	if err != nil || len(checkins) != 1 || checkins[0].Status != "success" {
		t.Fatalf("check-in history lost: %+v err=%v", checkins, err)
	}
	runs, err := restored.ListDailyTaskRuns(ctx, account.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("daily task history lost: %+v err=%v", runs, err)
	}
}

// A failed integrity check must stop the upgrade path, not publish a snapshot
// that a later rollback would trust.
func TestBackupIntegrityFailureIsNotPublished(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, accounts.CreateAccount{Name: "a", Provider: "workbuddy", Region: "cn"}); err != nil {
		t.Fatal(err)
	}
	backup, err := store.Backup(ctx, filepath.Join(root, "backups"), 5)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the manifest: verification must fail closed.
	if err := writeManifest(backup.Path+backupManifestSuffix, BackupManifest{
		Name: backup.Name, SHA256: "0000000000000000000000000000000000000000000000000000000000000000", IntegrityCheck: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyBackup(ctx, backup.Path); err == nil {
		t.Fatal("a snapshot whose recorded checksum does not match must not verify")
	}
}

func copyFileForTest(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
