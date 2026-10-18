package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestReadModelUsageSeparatesRequestsAttemptsUnknownAndZero(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStore(filepath.Join(t.TempDir(), "model-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "Synthetic WB", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	cacheZero := 0
	cacheWrite := 0
	logs := []accounts.RequestLog{
		{ID: "req-retried", CreatedAt: now, Status: accounts.RequestStatusOK, RequestedModel: "glm-5.3", MappedModel: "glm-5.3", AccountID: account.ID, Provider: "workbuddy", UsageSource: "upstream", CacheReadTokens: &cacheZero, CacheWriteTokens: &cacheWrite},
		{ID: "req-zero", CreatedAt: now.Add(time.Second), Status: accounts.RequestStatusOK, RequestedModel: "glm-5.3", MappedModel: "glm-5.3", AccountID: account.ID, Provider: "workbuddy", UsageSource: "unknown", CacheReadTokens: intPointer(999), FinishReason: "length", FinishReasonSource: "upstream"},
	}
	for _, log := range logs {
		if err := db.InsertRequestLog(ctx, log); err != nil {
			t.Fatal(err)
		}
	}
	zero := 0
	prompt, completion := 100, 20
	reset := now.Add(time.Hour)
	attempts := []accounts.RequestAttempt{
		{ID: "attempt-failed", RequestID: "req-retried", AttemptIndex: 0, AccountID: account.ID, Provider: "workbuddy", Region: "cn", ProviderPlan: "pro", Model: "glm-5.3", StartedAt: now, FinishedAt: timePointer(now), Status: accounts.AttemptStatusFailover, ErrorKind: accounts.KindRateLimit, ErrorCode: "6004", ErrorScope: "model", UpstreamResetAt: &reset, UsageState: "unknown"},
		{ID: "attempt-ok", RequestID: "req-retried", AttemptIndex: 1, AccountID: account.ID, Provider: "workbuddy", Region: "cn", ProviderPlan: "pro", Model: "glm-5.3", StartedAt: now.Add(time.Millisecond), FinishedAt: timePointer(now.Add(2 * time.Millisecond)), Status: accounts.AttemptStatusOK, PromptTokens: &prompt, CompletionTokens: &completion, UsageState: "actual", UsageSource: "upstream"},
		{ID: "attempt-zero", RequestID: "req-zero", AttemptIndex: 0, AccountID: account.ID, Provider: "workbuddy", Region: "cn", ProviderPlan: "pro", Model: "glm-5.3", StartedAt: now.Add(time.Second), FinishedAt: timePointer(now.Add(time.Second + time.Millisecond)), Status: accounts.AttemptStatusOK, PromptTokens: &zero, CompletionTokens: &zero, UsageState: "actual", UsageSource: "upstream"},
	}
	for _, attempt := range attempts {
		if err := db.InsertRequestAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	data, err := db.ReadModelUsage(ctx, accounts.ModelUsageQuery{From: from, To: to, Provider: "workbuddy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("rows = %+v", data.Rows)
	}
	row := data.Rows[0]
	if row.Requests != 2 || row.Attempts != 3 || row.SucceededAttempts != 2 || row.FailoverAttempts != 1 || row.LengthRequests != 1 {
		t.Fatalf("request/attempt counts = %+v", row)
	}
	if row.KnownUsageAttempts != 2 || row.MissingUsageAttempts != 1 || row.EstimatedUsageAttempts != 0 {
		t.Fatalf("usage coverage = %+v", row)
	}
	if row.PromptTokens == nil || *row.PromptTokens != 100 || row.CompletionTokens == nil || *row.CompletionTokens != 20 {
		t.Fatalf("actual token sums = %+v", row)
	}
	if row.CacheReadTokens == nil || *row.CacheReadTokens != 0 || row.CacheWriteTokens == nil || *row.CacheWriteTokens != 0 || row.CacheKnownRequests != 1 || row.CacheMissingRequests != 1 {
		t.Fatalf("cache null/zero distinction = %+v", row)
	}
	if len(data.Recent) != 2 || data.Recent[1].Code != "6004" || data.Recent[1].UpstreamResetAt == nil || data.Recent[0].Code != "length" || data.Recent[0].FinishReasonSource != "upstream" {
		t.Fatalf("recent error metadata = %+v", data.Recent)
	}
	if len(data.BudgetObservations) != 3 || data.BudgetObservations[0].Plan != "pro" {
		t.Fatalf("budget observations = %+v", data.BudgetObservations)
	}
}

func TestModelUsageBudgetObservationsRetainPathAndIncludePeerAccountsWhenFiltered(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStore(filepath.Join(t.TempDir(), "budget-path.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	accountsByName := make(map[string]accounts.Account)
	for _, name := range []string{"wb-a", "wb-b"} {
		account, err := db.Create(ctx, accounts.CreateAccount{Name: name, Provider: "workbuddy", Region: "cn", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		accountsByName[name] = account
	}
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Hour)
	for name, account := range accountsByName {
		requestID := "req-" + name
		if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: requestID, CreatedAt: now, Status: accounts.RequestStatusError,
			RequestedModel: "glm-5.3", MappedModel: "glm-5.3", Provider: "workbuddy", AccountID: account.ID, RequestPath: "openai_chat_completions"}); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-" + name, RequestID: requestID, AccountID: account.ID,
			Provider: "workbuddy", Region: "cn", ProviderPlan: "pro", Model: "glm-5.3", StartedAt: now,
			FinishedAt: timePointer(now), Status: accounts.AttemptStatusFailover, ErrorKind: accounts.KindRateLimit, ErrorCode: "6004",
			ErrorScope: "model", UpstreamResetAt: &reset, UsageState: "unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	// A later log update that has no path value must not erase the start-time route.
	if err := db.UpdateRequestLog(ctx, accounts.RequestLog{ID: "req-wb-a", Status: accounts.RequestStatusError, RequestedModel: "glm-5.3", MappedModel: "glm-5.3"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetRequestLog(ctx, "req-wb-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RequestPath != "openai_chat_completions" {
		t.Fatalf("request path was lost across request update: %+v", loaded)
	}
	data, err := db.ReadModelUsage(ctx, accounts.ModelUsageQuery{From: now.Add(-time.Minute), To: now.Add(time.Minute), Provider: "workbuddy", Model: "glm-5.3", Account: accountsByName["wb-a"].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 1 || data.Rows[0].AccountID != accountsByName["wb-a"].ID {
		t.Fatalf("visible rows should still respect the account filter: %+v", data.Rows)
	}
	if len(data.BudgetObservations) != 2 {
		t.Fatalf("budget reference query must include other comparable accounts: %+v", data.BudgetObservations)
	}
	for _, observation := range data.BudgetObservations {
		if observation.RequestPath != "openai_chat_completions" || observation.Plan != "pro" || observation.Code != "6004" {
			t.Fatalf("budget observation lost path/plan/limit metadata: %+v", observation)
		}
	}
}

func TestMigration035PreservesLegacyRequestRowsAndLeavesPathUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-request-log.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
	for _, migration := range sqliteMigrations[:len(sqliteMigrations)-1] {
		if err := legacy.applyMigration(context.Background(), migration); err != nil {
			t.Fatalf("apply historical migration %s: %v", migration.filename, err)
		}
	}
	created := formatTime(time.Now().UTC())
	if _, err := db.Exec(`INSERT INTO request_logs (id, created_at, status, requested_model) VALUES (?, ?, ?, ?)`, "req-legacy", created, "ok", "glm-5.3"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var status, model, requestPath string
	if err := upgraded.db.QueryRow(`SELECT status, requested_model, request_path FROM request_logs WHERE id=?`, "req-legacy").Scan(&status, &model, &requestPath); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || model != "glm-5.3" || requestPath != "" {
		t.Fatalf("migration must preserve the row and keep its unknown route unknown: status=%q model=%q path=%q", status, model, requestPath)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func intPointer(value int) *int { return &value }
