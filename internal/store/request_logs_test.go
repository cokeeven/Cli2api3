package store

import (
	"context"
	"database/sql"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestDiagnosticsMigrationPreservesLegacyData(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-diagnostics.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: database}
	if _, err := database.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range sqliteMigrations[:27] {
		if err := legacy.applyMigration(ctx, migration); err != nil {
			t.Fatalf("apply %s: %v", migration.filename, err)
		}
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO accounts (id, name, provider, provider_region, created_at, updated_at) VALUES ('synthetic-account', 'Synthetic', 'workbuddy', 'cn', '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z');
INSERT INTO account_credential_payloads (account_id, format, payload, updated_at) VALUES ('synthetic-account', 'test', 'synthetic-credential-payload', '2026-10-17T00:00:00Z');
INSERT INTO api_keys (id, name, key_hash, prefix, providers_json, created_at, updated_at) VALUES ('synthetic-key-id', 'Synthetic key', 'synthetic-hash', 'sk-test', '["workbuddy:cn"]', '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z');
INSERT INTO provider_model_settings (provider, model_id, max_mode, reasoning_effort, updated_at) VALUES ('workbuddy', 'test-model', 1, 'high', '2026-10-17T00:00:00Z');
INSERT INTO app_secrets (name, value, created_at, updated_at) VALUES ('synthetic-config', 'test-config-value', '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z');
INSERT INTO request_logs (id, created_at, status, requested_model, account_id, message_count, empty_message_indexes, message_roles) VALUES ('synthetic-request', '2026-10-17T00:00:00Z', 'error', 'test-model', 'synthetic-account', 3, '[1]', '["system","user","assistant"]');
INSERT INTO request_attempts (id, request_id, attempt_index, account_id, started_at, status) VALUES ('synthetic-attempt', 'synthetic-request', 0, 'synthetic-account', '2026-10-17T00:00:00Z', 'error');
INSERT INTO request_stream_diagnostics (request_id, created_at, finished_at, content_length, saw_done) VALUES ('synthetic-request', '2026-10-17T00:00:00Z', '2026-10-17T00:00:01Z', 0, 0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var payload string
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT CAST(payload AS TEXT) FROM account_credential_payloads WHERE account_id = 'synthetic-account'`).Scan(&payload); err != nil || payload != "synthetic-credential-payload" {
		t.Fatalf("credential payload=%q err=%v", payload, err)
	}
	var keyCount, settingMaxMode int
	var settingReasoning, configValue string
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE id = 'synthetic-key-id' AND key_hash = 'synthetic-hash'`).Scan(&keyCount); err != nil || keyCount != 1 {
		t.Fatalf("synthetic key count=%d err=%v", keyCount, err)
	}
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT max_mode, reasoning_effort FROM provider_model_settings WHERE provider='workbuddy' AND model_id='test-model'`).Scan(&settingMaxMode, &settingReasoning); err != nil || settingMaxMode != 1 || settingReasoning != "high" {
		t.Fatalf("provider settings=%d/%q err=%v", settingMaxMode, settingReasoning, err)
	}
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT value FROM app_secrets WHERE name='synthetic-config'`).Scan(&configValue); err != nil || configValue != "test-config-value" {
		t.Fatalf("synthetic config=%q err=%v", configValue, err)
	}
	log, err := upgraded.GetRequestLog(ctx, "synthetic-request")
	if err != nil {
		t.Fatal(err)
	}
	if log.MessageCount != 3 || len(log.MessageRoles) != 3 || len(log.EmptyMessageIndexes) != 1 || log.MessageShapeKnown {
		t.Fatalf("legacy request history changed or guessed as known: %+v", log)
	}
	if len(log.Attempts) != 1 || log.Attempts[0].ProviderPlan != "" || log.Attempts[0].ErrorPhase != "" || log.Attempts[0].ErrorSource != "" || log.FinishReason != "" || log.FinishReasonSource != "" || log.OutputBudgetEnforcement != "" || log.StreamDiagnostic == nil || log.StreamDiagnostic.ContentLength != nil || log.StreamDiagnostic.OutputBudgetEnforcement != "" {
		t.Fatalf("legacy request children or unknown content length changed: %+v", log)
	}
	var migrationCount int
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = '028_request_diagnostics.sql'`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("028 migration count=%d err=%v", migrationCount, err)
	}
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = '036_output_budget_evidence.sql'`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("036 migration count=%d err=%v", migrationCount, err)
	}
}

func TestRequestAttemptErrorEvidencePersistsAndLegacyValuesStayUnknown(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "attempt-error-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: "request-synthetic-auth", CreatedAt: now, Status: accounts.RequestStatusError,
		RequestedModel: "glm-5.3", Provider: "workbuddy",
	}); err != nil {
		t.Fatal(err)
	}
	status := 200
	if err := store.InsertRequestAttempt(ctx, accounts.RequestAttempt{
		ID: "attempt-synthetic-auth", RequestID: "request-synthetic-auth", AttemptIndex: 0,
		StartedAt: now, FinishedAt: &now, Status: accounts.AttemptStatusError, HTTPStatus: &status,
		ErrorKind: accounts.KindAuth, ErrorCode: "12153", ErrorPhase: "chat", ErrorSource: "workbuddy_chat_response",
		ErrorMessage: "WorkBuddy 会话已被上游拒绝，请检查登录状态。",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRequestLog(ctx, "request-synthetic-auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("attempt count=%d", len(got.Attempts))
	}
	attempt := got.Attempts[0]
	if attempt.HTTPStatus == nil || *attempt.HTTPStatus != 200 || attempt.ErrorCode != "12153" || attempt.ErrorPhase != "chat" || attempt.ErrorSource != "workbuddy_chat_response" {
		t.Fatalf("persisted evidence=%+v", attempt)
	}
}

func TestRequestLogFinishReasonSurvivesCompletionAndList(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "finish-reason.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: "req-finish-length", CreatedAt: now, Status: accounts.RequestStatusStarted,
		RequestedModel: "glm-5.3", Provider: "workbuddy",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: "req-finish-length", FinishedAt: &now, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", Provider: "workbuddy", FinishReason: "length", FinishReasonSource: "upstream",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRequestLog(ctx, "req-finish-length")
	if err != nil || got.FinishReason != "length" || got.FinishReasonSource != "upstream" {
		t.Fatalf("get finish reason=%q/%q err=%v", got.FinishReason, got.FinishReasonSource, err)
	}
	listed, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].FinishReason != "length" || listed.Items[0].FinishReasonSource != "upstream" {
		t.Fatalf("listed finish reasons=%+v err=%v", listed.Items, err)
	}
}

func TestRequestLogOutputBudgetEnforcementPersistsAndEmptyUpdatesPreserve(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "output-budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	limit := 16000
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: "req-budget-evidence", CreatedAt: now, Status: accounts.RequestStatusStarted,
		RequestedModel: "glm-5.3", Provider: "trae", OutputLimit: &limit, OutputLimitSource: "caller",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: "req-budget-evidence", FinishedAt: &now, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", Provider: "trae", OutputLimit: &limit, OutputLimitSource: "caller",
		OutputBudgetEnforcement: "observed_ignored",
	}); err != nil {
		t.Fatal(err)
	}
	// Later lifecycle writes may omit optional diagnostic fields; they must not
	// erase a classification already recorded for this request.
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: "req-budget-evidence", FinishedAt: &now, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", Provider: "trae",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRequestStreamDiagnostic(ctx, accounts.RequestStreamDiagnostic{
		RequestID: "req-budget-evidence", CreatedAt: now, OutputBudgetEnforcement: "observed_ignored",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRequestStreamDiagnostic(ctx, accounts.RequestStreamDiagnostic{
		RequestID: "req-budget-evidence", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRequestLog(ctx, "req-budget-evidence")
	if err != nil || got.OutputBudgetEnforcement != "observed_ignored" || got.StreamDiagnostic == nil || got.StreamDiagnostic.OutputBudgetEnforcement != "observed_ignored" {
		t.Fatalf("stored request budget evidence=%q stream=%+v err=%v", got.OutputBudgetEnforcement, got.StreamDiagnostic, err)
	}
	listed, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].OutputBudgetEnforcement != "observed_ignored" {
		t.Fatalf("listed request budget evidence=%+v err=%v", listed.Items, err)
	}
}

func TestRequestLogsInsertListGetAndPurge(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if _, err := store.Create(ctx, accounts.CreateAccount{Name: "A", Provider: "qoder", Region: "global"}); err != nil {
		t.Fatal(err)
	}
	wb, err := store.Create(ctx, accounts.CreateAccount{Name: "WB", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	parentID := accounts.NewRequestID()
	prompt, completion := 12, 34
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: parentID, CreatedAt: now, Stream: false, Status: accounts.RequestStatusStarted,
		RequestedModel: "glm-5.3", RequestedReasoning: "high", AccountID: wb.ID, AttemptCount: 0,
	}); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(120 * time.Millisecond)
	latency := 120
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: parentID, CreatedAt: now, FinishedAt: &finished, Stream: false, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", RequestedReasoning: "high", ResolvedReasoning: "medium", AccountID: wb.ID, PromptTokens: &prompt, CompletionTokens: &completion,
		UsageSource: "upstream", LatencyMs: &latency, AttemptCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRequestAttempt(ctx, accounts.RequestAttempt{
		ID: accounts.NewAttemptID(), RequestID: parentID, AttemptIndex: 0, AccountID: "acc_a",
		StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusFailover,
		ErrorKind: accounts.KindRateLimit, ErrorMessage: "too many requests",
	}); err != nil {
		t.Fatal(err)
	}
	upstreamStatus := 200
	if err := store.InsertRequestStreamDiagnostic(ctx, accounts.RequestStreamDiagnostic{
		RequestID: parentID, CreatedAt: now, FinishedAt: &finished, UpstreamStatus: &upstreamStatus,
		ContextErr: "context canceled", CancellationSource: "request_context_canceled",
		RelayError: "stream read error: context canceled", SSEEventCount: 4, BytesRead: 512,
		ContentLength: nil, LastEvent: "message", SawDone: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRequestAttempt(ctx, accounts.RequestAttempt{
		ID: accounts.NewAttemptID(), RequestID: parentID, AttemptIndex: 1, AccountID: "acc_b",
		StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusOK,
		PromptTokens: &prompt, CompletionTokens: &completion, UsageSource: "upstream",
	}); err != nil {
		t.Fatal(err)
	}
	consumed := 0.75
	if err := store.InsertRequestUsageDetail(ctx, accounts.RequestUsageDetail{
		RequestID: parentID, CreatedAt: now, Provider: "workbuddy", Credit: &consumed, Unit: "credits",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Status != accounts.RequestStatusOK || list.Items[0].Provider != "workbuddy" {
		t.Fatalf("list = %+v", list)
	}
	if list.Items[0].RequestedReasoning != "high" || list.Items[0].ResolvedReasoning != "medium" {
		t.Fatalf("list reasoning = %+v", list.Items[0])
	}
	if list.Items[0].Credits == nil || *list.Items[0].Credits != 0.75 {
		t.Fatalf("list credits fallback = %+v", list.Items[0].Credits)
	}
	if list.Items[0].UsageDetail != nil {
		t.Fatalf("list should not carry usage detail: %+v", list.Items[0].UsageDetail)
	}

	got, err := store.GetRequestLog(ctx, parentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != wb.ID || got.Provider != "workbuddy" || len(got.Attempts) != 2 || got.Attempts[0].Status != accounts.AttemptStatusFailover {
		t.Fatalf("detail = %+v", got)
	}
	if got.RequestedReasoning != "high" || got.ResolvedReasoning != "medium" {
		t.Fatalf("detail reasoning = %+v", got)
	}
	if got.StreamDiagnostic == nil || got.StreamDiagnostic.UpstreamStatus == nil || *got.StreamDiagnostic.UpstreamStatus != 200 ||
		got.StreamDiagnostic.CancellationSource != "request_context_canceled" || got.StreamDiagnostic.SSEEventCount != 4 || got.StreamDiagnostic.SawDone {
		t.Fatalf("stream diagnostic = %+v", got.StreamDiagnostic)
	}
	if got.UsageDetail == nil || got.UsageDetail.Credit == nil || *got.UsageDetail.Credit != 0.75 ||
		got.UsageDetail.Unit != "credits" || got.UsageDetail.Provider != "workbuddy" {
		t.Fatalf("usage detail = %+v", got.UsageDetail)
	}

	filtered, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{AccountID: wb.ID, Status: accounts.RequestStatusOK})
	if err != nil || filtered.Total != 1 {
		t.Fatalf("filtered = %+v err=%v", filtered, err)
	}
	none, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{ErrorKind: accounts.KindQuota})
	if err != nil || none.Total != 0 {
		t.Fatalf("error filter = %+v err=%v", none, err)
	}

	from := now.Add(-time.Second)
	to := now.Add(time.Second)
	timed, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{From: &from, To: &to, Model: "glm-5.3"})
	if err != nil || timed.Total != 1 {
		t.Fatalf("time/model filter = %+v err=%v", timed, err)
	}
	tooNew := now.Add(time.Hour)
	emptyTime, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{From: &tooNew})
	if err != nil || emptyTime.Total != 0 {
		t.Fatalf("future from filter = %+v err=%v", emptyTime, err)
	}

	page, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 1, Offset: 0})
	if err != nil || page.Total != 1 || page.Limit != 1 || page.Offset != 0 || len(page.Items) != 1 {
		t.Fatalf("page = %+v err=%v", page, err)
	}

	oldID := accounts.NewRequestID()
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: oldID, CreatedAt: now.Add(-10 * 24 * time.Hour), Status: accounts.RequestStatusError, RequestedModel: "old",
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.PurgeRequestLogs(ctx, 7*24*time.Hour, 20_000)
	if err != nil || deleted < 1 {
		t.Fatalf("purge deleted=%d err=%v", deleted, err)
	}
	if _, err := store.GetRequestLog(ctx, oldID); err != accounts.ErrRequestLogNotFound {
		t.Fatalf("expected old log gone, got %v", err)
	}

	cleared, err := store.ClearRequestLogs(ctx)
	if err != nil || cleared < 1 {
		t.Fatalf("clear = %d err=%v", cleared, err)
	}
	var usageRows int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM request_usage_details`).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 0 {
		t.Fatalf("usage detail rows survived clear: %d", usageRows)
	}
}

func TestRequestLogFinishWithoutMessageShapePreservesStartShape(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "request-shape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created := time.Now().UTC()
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: "req_shape_preserve", CreatedAt: created, Status: accounts.RequestStatusStarted,
		RequestedModel: "trae/glm-5.3", MessageCount: 2, MessageShapeKnown: true,
		EmptyMessageIndexes: []int{1}, MessageRoles: []string{"system", "user"},
	}); err != nil {
		t.Fatal(err)
	}
	finished := created.Add(time.Second)
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: "req_shape_preserve", CreatedAt: created, FinishedAt: &finished,
		Status: accounts.RequestStatusOK, RequestedModel: "trae/glm-5.3",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRequestLog(ctx, "req_shape_preserve")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MessageShapeKnown || got.MessageCount != 2 || len(got.EmptyMessageIndexes) != 1 || got.EmptyMessageIndexes[0] != 1 ||
		len(got.MessageRoles) != 2 || got.MessageRoles[0] != "system" || got.MessageRoles[1] != "user" {
		t.Fatalf("final request log lost the message shape recorded at start: %+v", got)
	}
	if err := store.UpdateRequestLog(ctx, accounts.RequestLog{
		ID: "req_shape_preserve", Status: accounts.RequestStatusError, MessageShapeKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetRequestLog(ctx, "req_shape_preserve")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MessageShapeKnown || got.MessageCount != 0 || len(got.EmptyMessageIndexes) != 0 || len(got.MessageRoles) != 0 {
		t.Fatalf("explicitly supplied empty shape did not replace the earlier value: %+v", got)
	}
}

func TestUsageDetailBackfillUsesProviderThenAccountFallback(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.Create(ctx, accounts.CreateAccount{Name: "WB", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	explicit, fallback, missing := 1.5, 2.5, 3.5
	rows := []accounts.RequestLog{
		{ID: accounts.NewRequestID(), CreatedAt: now, Status: accounts.RequestStatusOK, RequestedModel: "m1", Provider: "trae", AccountID: account.ID, Credits: &explicit},
		{ID: accounts.NewRequestID(), CreatedAt: now, Status: accounts.RequestStatusOK, RequestedModel: "m2", AccountID: account.ID, Credits: &fallback},
		{ID: accounts.NewRequestID(), CreatedAt: now, Status: accounts.RequestStatusOK, RequestedModel: "m3", AccountID: "ghost", Credits: &missing},
	}
	for _, row := range rows {
		if err := store.InsertRequestLog(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.DB().ExecContext(ctx, `DROP TABLE IF EXISTS request_usage_details`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM schema_migrations WHERE filename = '020_request_usage_details.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := store.runMigrations(ctx); err != nil {
		t.Fatal(err)
	}

	explicitDetail, err := store.getRequestUsageDetail(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if explicitDetail == nil || explicitDetail.Provider != "trae" {
		t.Fatalf("explicit provider row = %+v, want trae", explicitDetail)
	}

	fallbackDetail, err := store.getRequestUsageDetail(ctx, rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackDetail == nil || fallbackDetail.Provider != "workbuddy" {
		t.Fatalf("account fallback row = %+v, want workbuddy", fallbackDetail)
	}
	if fallbackDetail.Credit == nil || *fallbackDetail.Credit != fallback || fallbackDetail.Unit != "credits" {
		t.Fatalf("account fallback credit/unit = %+v", fallbackDetail)
	}

	missingDetail, err := store.getRequestUsageDetail(ctx, rows[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if missingDetail == nil || missingDetail.Provider != "" {
		t.Fatalf("unreachable account row = %+v, want empty provider", missingDetail)
	}
}

func TestRequestLogsPaginationAndTimeFilter(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		account := "acc_a"
		model := "glm-5.3"
		if i%2 == 1 {
			account = "acc_b"
			model = "qwen3.7-plus"
		}
		if err := store.InsertRequestLog(ctx, accounts.RequestLog{
			ID: accounts.NewRequestID(), CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Status: accounts.RequestStatusOK, RequestedModel: model, AccountID: account,
		}); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 2, Offset: 0})
	if err != nil || page1.Total != 5 || page1.Limit != 2 || page1.Offset != 0 || len(page1.Items) != 2 {
		t.Fatalf("page1 = %+v err=%v", page1, err)
	}
	page3, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 2, Offset: 4})
	if err != nil || page3.Total != 5 || len(page3.Items) != 1 {
		t.Fatalf("page3 = %+v err=%v", page3, err)
	}

	from := base.Add(2 * time.Minute)
	to := base.Add(3 * time.Minute)
	window, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{From: &from, To: &to, Limit: 10})
	if err != nil || window.Total != 2 {
		t.Fatalf("window = %+v err=%v", window, err)
	}
	accountB, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{AccountID: "acc_b", Limit: 10})
	if err != nil || accountB.Total != 2 {
		t.Fatalf("account = %+v err=%v", accountB, err)
	}
	model, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Model: "qwen3.7-plus", Limit: 10})
	if err != nil || model.Total != 2 {
		t.Fatalf("model = %+v err=%v", model, err)
	}
	exactID := page1.Items[0].ID
	byID, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{ID: exactID, Limit: 10})
	if err != nil || byID.Total != 1 || byID.Items[0].ID != exactID {
		t.Fatalf("id = %+v err=%v", byID, err)
	}
	prefix, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Query: exactID[:8], Limit: 10})
	if err != nil || prefix.Total != 1 || prefix.Items[0].ID != exactID {
		t.Fatalf("id prefix = %+v err=%v", prefix, err)
	}
	noneQuery, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Query: "glm-5.3", Limit: 10})
	if err != nil || noneQuery.Total != 0 {
		t.Fatalf("query should only match ids, got %+v err=%v", noneQuery, err)
	}
}

func TestRequestLogsCapPurgeKeepsNewest(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := store.InsertRequestLog(ctx, accounts.RequestLog{
			ID: accounts.NewRequestID(), CreatedAt: base.Add(time.Duration(i) * time.Second),
			Status: accounts.RequestStatusOK, RequestedModel: "m",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PurgeRequestLogs(ctx, 30*24*time.Hour, 3); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 {
		t.Fatalf("expected 3 rows after cap purge, got %d", list.Total)
	}
}

func TestSummarizeRequestLogs(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Date(2026, 8, 28, 10, 15, 0, 0, time.UTC)
	insert := func(at time.Time, status, model, account, provider, kind string, latency, prompt, completion int, stream bool) {
		t.Helper()
		log := accounts.RequestLog{
			ID: accounts.NewRequestID(), CreatedAt: at, Status: status, RequestedModel: model, AccountID: account,
			Provider: provider, ErrorKind: kind, Stream: stream, AttemptCount: 1,
		}
		if latency > 0 {
			log.LatencyMs = &latency
		}
		if prompt > 0 {
			log.PromptTokens = &prompt
		}
		if completion > 0 {
			log.CompletionTokens = &completion
		}
		if err := store.InsertRequestLog(ctx, log); err != nil {
			t.Fatal(err)
		}
	}
	insert(base, accounts.RequestStatusOK, "glm-5.3", "acc_a", "qoder", "", 100, 12, 34, false)
	insert(base.Add(20*time.Minute), accounts.RequestStatusOK, "glm-5.3", "acc_a", "qoder", "", 200, 10, 20, true)
	insert(base.Add(90*time.Minute), accounts.RequestStatusError, "qwen3.7-plus", "acc_b", "workbuddy", accounts.KindRateLimit, 400, 8, 0, false)
	insert(base.Add(2*time.Hour), accounts.RequestStatusCanceled, "glm-5.3", "acc_a", "qoder", "", 0, 0, 0, false)
	insert(base.Add(2*time.Hour+15*time.Minute), accounts.RequestStatusIncomplete, "glm-5.3", "acc_a", "qoder", "", 0, 0, 0, false)
	insert(base.Add(-30*time.Hour), accounts.RequestStatusOK, "old", "acc_a", "qoder", "", 50, 1, 1, false)

	from := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)
	stats, err := store.SummarizeRequestLogs(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Totals.Requests != 5 || stats.Totals.OK != 2 || stats.Totals.Incomplete != 1 || stats.Totals.Error != 1 || stats.Totals.Canceled != 1 || stats.Totals.Streaming != 1 {
		t.Fatalf("totals = %+v", stats.Totals)
	}
	if stats.Totals.SuccessRate != 0.4 {
		t.Fatalf("success rate = %v", stats.Totals.SuccessRate)
	}
	if stats.Tokens.Prompt != 30 || stats.Tokens.Completion != 54 || stats.Tokens.Total != 84 {
		t.Fatalf("tokens = %+v", stats.Tokens)
	}
	if stats.Latency.AvgMs == nil || *stats.Latency.AvgMs != 233 {
		t.Fatalf("avg latency = %+v", stats.Latency.AvgMs)
	}
	if stats.Latency.P50Ms == nil || *stats.Latency.P50Ms != 200 {
		t.Fatalf("p50 = %+v", stats.Latency.P50Ms)
	}
	if stats.Latency.P95Ms == nil || *stats.Latency.P95Ms != 400 {
		t.Fatalf("p95 = %+v", stats.Latency.P95Ms)
	}
	if len(stats.Errors) != 1 || stats.Errors[0].Key != accounts.KindRateLimit || stats.Errors[0].Count != 1 {
		t.Fatalf("errors = %+v", stats.Errors)
	}
	if len(stats.Models) == 0 || stats.Models[0].Key != "glm-5.3" || stats.Models[0].Count != 4 {
		t.Fatalf("models = %+v", stats.Models)
	}
	if len(stats.Accounts) == 0 || stats.Accounts[0].Key != "acc_a" || stats.Accounts[0].Count != 4 {
		t.Fatalf("accounts = %+v", stats.Accounts)
	}
	if len(stats.Providers) == 0 || stats.Providers[0].Key != "qoder" || stats.Providers[0].Count != 4 {
		t.Fatalf("providers = %+v", stats.Providers)
	}
	if len(stats.Series) != 3 {
		t.Fatalf("series len = %d %+v", len(stats.Series), stats.Series)
	}
	if stats.Series[0].Requests != 2 || stats.Series[1].Requests != 1 || stats.Series[2].Requests != 2 {
		t.Fatalf("series = %+v", stats.Series)
	}

	empty, err := store.SummarizeRequestLogs(ctx, to.Add(time.Hour), to.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Totals.Requests != 0 || empty.Latency.AvgMs != nil || len(empty.Models) != 0 {
		t.Fatalf("empty stats = %+v", empty)
	}

	hourFrom := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	hourTo := hourFrom.Add(time.Hour)
	hourStats, err := store.SummarizeRequestLogs(ctx, hourFrom, hourTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourStats.Series) != 4 {
		t.Fatalf("1h series len = %d %+v", len(hourStats.Series), hourStats.Series)
	}
	if hourStats.Series[1].Requests != 1 || hourStats.Series[2].Requests != 1 {
		t.Fatalf("1h series = %+v", hourStats.Series)
	}
}

func TestRequestLogPersistsRouting(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "qoder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id := accounts.NewRequestID()
	if err := store.InsertRequestLog(ctx, accounts.RequestLog{
		ID: id, CreatedAt: time.Now().UTC(), Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.2", AccountID: "account-b", Routing: "sticky_escape",
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListRequestLogs(ctx, accounts.RequestLogFilter{Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Routing != "sticky_escape" {
		t.Fatalf("listed = %+v, err=%v", listed, err)
	}
	detail, err := store.GetRequestLog(ctx, id)
	if err != nil || detail.Routing != "sticky_escape" {
		t.Fatalf("detail = %+v, err=%v", detail, err)
	}
}
