package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestMaintenanceLogsAllPlatformsFiltersAndExplicitParent(t *testing.T) {
	s, wbID := dailyTestStore(t)
	ctx := context.Background()
	other, err := s.Create(ctx, accounts.CreateAccount{Name: "qoder history", Provider: "qoder", Region: "global"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	if err = s.RecordCheckin(ctx, other.ID, "error", "old failure", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordCheckin(accounts.WithMaintenanceTrigger(ctx, "manual"), wbID, "already", "old standalone", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, err := s.ClaimDailyTaskRun(ctx, wbID, "2026-10-15", "scheduled", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	finishTestRun(t, s, run, now.Add(time.Second), true)
	page, err := s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Count != 3 || page.Items[0].ID != run.ID || page.Items[0].Status != "partial" || len(page.Items[0].Steps) != 2 {
		t.Fatalf("page=%+v", page)
	}
	if page.Items[1].Trigger != "manual" || page.Items[2].Trigger != "unknown" || page.Items[2].Provider != "qoder" {
		t.Fatalf("history=%+v", page.Items)
	}
	page, err = s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Provider: "qoder", Region: "global", Kind: "checkin", Trigger: "unknown", Status: "error", AccountID: other.ID})
	if err != nil || page.Total != 1 {
		t.Fatalf("filter=%+v err=%v", page, err)
	}
	page, err = s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Limit: 1, Offset: 1})
	if err != nil || page.Total != 3 || page.Count != 1 || page.Items[0].ID == run.ID {
		t.Fatalf("pagination=%+v err=%v", page, err)
	}
	from, to := now.Add(-2*time.Minute), now.Add(-time.Second)
	page, err = s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{From: &from, To: &to})
	if err != nil || page.Total != 1 || page.Items[0].Trigger != "manual" {
		t.Fatalf("time=%+v err=%v", page, err)
	}
	page, err = s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Limit: 999, Offset: -1, Provider: "missing"})
	if err != nil || page.Total != 0 || page.Items == nil || page.Limit != 100 || page.Offset != 0 {
		t.Fatalf("empty=%+v err=%v", page, err)
	}
	second, err := s.ClaimDailyTaskRun(ctx, wbID, "2026-10-15", "scheduled", now.Add(6*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Trigger: "retry"})
	if err != nil || page.Total != 1 || page.Items[0].ID != second.ID || page.Items[0].Attempt != 2 {
		t.Fatalf("retry=%+v err=%v", page, err)
	}
}

func TestMaintenanceMigrationPreservesLegacyHistoryAndCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	defer s.Close()
	ctx := context.Background()
	if _, err = db.Exec(schemaMigrationsDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range sqliteMigrations[:23] {
		if err = s.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Create(ctx, accounts.CreateAccount{Name: "preserved", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"access_token":"fixture-only"}`)
	if _, err = db.Exec(`INSERT INTO account_credential_payloads (account_id, format, payload, updated_at) VALUES (?, ?, ?, ?)`, a.ID, "workbuddy-oauth-v1", payload, formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
	if _, err = db.Exec(`INSERT INTO checkin_records(id,account_id,status,message,created_at) VALUES('legacy-checkin',?,'success','legacy',?)`, a.ID, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO daily_task_runs(id,account_id,run_date,trigger,status,attempt,started_at,finished_at,report_json) VALUES('legacy-daily',?,'2026-10-15','manual','success',1,?,?,?)`, a.ID, formatTime(now), formatTime(now), `{"steps":[{"step":"checkin","status":"success","ok":true}]}`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	page, err := upgraded.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{})
	if err != nil || page.Total != 2 {
		t.Fatalf("legacy records guessed/removed: total=%d err=%v", page.Total, err)
	}
	for _, item := range page.Items {
		if item.Kind == "checkin" && item.Trigger != "unknown" {
			t.Fatalf("legacy provenance guessed: %q", item.Trigger)
		}
	}
	_, got, err := upgraded.LoadCredentialPayload(ctx, a.ID)
	if err != nil || string(got) != string(payload) {
		t.Fatal("credential changed")
	}
	after, err := upgraded.Get(ctx, a.ID)
	if err != nil || after.Name != a.Name || after.Enabled != a.Enabled || after.AuthType != a.AuthType || after.UpdatedAt != a.UpdatedAt {
		t.Fatal("account changed")
	}
}

func TestCheckinAttemptDoesNotChangeAccountAndDatabasesStayIsolated(t *testing.T) {
	s, id := dailyTestStore(t)
	ctx := context.Background()
	before, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordCheckinAttempt(accounts.WithMaintenanceTrigger(ctx, "manual"), id, "already", "completed earlier today; no upstream call", time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.LastCheckinAt != after.LastCheckinAt || before.LastCheckinMsg != after.LastCheckinMsg || before.UpdatedAt != after.UpdatedAt || before.Enabled != after.Enabled {
		t.Fatal("append-only attempt changed account")
	}
	other, err := OpenStore(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	page, err := other.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{})
	if err != nil || page.Total != 0 {
		t.Fatalf("isolated page=%+v err=%v", page, err)
	}
}
