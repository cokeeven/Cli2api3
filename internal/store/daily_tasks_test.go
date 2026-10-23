package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func dailyTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daily.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(context.Background(), accounts.CreateAccount{Name: "daily", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	return s, a.ID
}

func finishTestRun(t *testing.T, s *Store, run accounts.DailyTaskRun, now time.Time, fail bool) accounts.DailyTaskRun {
	t.Helper()
	run.Report = providers.DailyTasksReport{AccountID: run.AccountID, RanAt: formatTime(run.StartedAt)}
	run.Report.Add("checkin", "success", "checked in")
	if fail {
		run.Report.Add("growth", "error", "failed")
		retry := now.Add(5 * time.Minute)
		run.NextRetryAt = &retry
	}
	run.Status = run.Report.Status()
	run.FinishedAt = &now
	if err := s.FinishDailyTaskRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestDailyTasksClaimRetryAndRestartDeduplication(t *testing.T) {
	s, id := dailyTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	run, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDailyTaskRun(ctx, id, date, "manual", now, time.Minute); !errors.Is(err, accounts.ErrDailyTasksBusy) {
		t.Fatalf("busy=%v", err)
	}
	run = finishTestRun(t, s, run, now, true)
	if _, err = s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(time.Minute), time.Minute); !errors.Is(err, accounts.ErrDailyTasksNotDue) {
		t.Fatalf("retry too early=%v", err)
	}
	next, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(6*time.Minute), time.Minute)
	if err != nil || next.Attempt != 2 || len(next.Report.Steps) != 2 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	next.Report = providers.DailyTasksReport{AccountID: id}
	next.Report.Reuse(providers.DailyTaskStep{Step: "checkin", Status: "success"})
	next.Report.Add("growth", "success", "done")
	finished := now.Add(6 * time.Minute)
	next.FinishedAt = &finished
	next.Status = "success"
	if err = s.FinishDailyTaskRun(ctx, next); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListCheckinRecords(ctx, id, 50)
	if err != nil || len(records) != 1 {
		t.Fatalf("checkins=%+v err=%v", records, err)
	}
	var path string
	if err = s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(7*time.Minute), time.Minute); !errors.Is(err, accounts.ErrDailyTasksNotDue) {
		t.Fatalf("restart duplicated success=%v", err)
	}
	runs, err := reopened.ListDailyTaskRuns(ctx, id, 20)
	if err != nil || len(runs) != 2 || runs[0].Status != "success" {
		t.Fatalf("history=%+v err=%v", runs, err)
	}
}

func TestDailyTasksExpiredLeaseAndRetryLimit(t *testing.T) {
	s, id := dailyTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	old, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	old.Report.Add("checkin", "success", "checked in")
	old.Status = "success"
	finish := now.Add(2 * time.Minute)
	old.FinishedAt = &finish
	if err = s.FinishDailyTaskRun(ctx, old); !errors.Is(err, accounts.ErrDailyTasksLeaseLost) {
		t.Fatalf("old owner overwrote run: %v", err)
	}
	finishTestRun(t, s, next, now.Add(2*time.Minute), true)
	third, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(10*time.Minute), time.Minute)
	if err != nil || third.Attempt != 3 {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	finishTestRun(t, s, third, now.Add(10*time.Minute), true)
	if _, err = s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(time.Hour), time.Minute); !errors.Is(err, accounts.ErrDailyTasksNotDue) {
		t.Fatalf("retry cap=%v", err)
	}
	runs, _ := s.ListDailyTaskRuns(ctx, id, 20)
	if runs[2].Status != "interrupted" {
		t.Fatalf("expired run=%+v", runs[2])
	}
}

func TestDailyTasksHistoryAndCheckinCommitAtomically(t *testing.T) {
	s, id := dailyTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	run, err := s.ClaimDailyTaskRun(ctx, id, now.Format("2006-01-02"), "manual", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	run.Report.Add("checkin", "success", "checked in")
	run.Status = "success"
	run.FinishedAt = &now
	_, err = s.db.Exec(`CREATE TEMP TRIGGER reject_daily_checkin BEFORE INSERT ON checkin_records BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishDailyTaskRun(ctx, run); err == nil {
		t.Fatal("expected persistence failure")
	}
	runs, _ := s.ListDailyTaskRuns(ctx, id, 20)
	account, _ := s.Get(ctx, id)
	if runs[0].Status != "running" || account.LastCheckinStatus != "" {
		t.Fatalf("partial commit: %+v %+v", runs, account)
	}
	_, _ = s.db.Exec(`DROP TRIGGER reject_daily_checkin`)
	if err = s.FinishDailyTaskRun(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestDailyTasksSeparateDatabasesNeverShareLeases(t *testing.T) {
	first, id := dailyTestStore(t)
	second, other := dailyTestStore(t)
	if _, err := second.db.Exec(`UPDATE accounts SET id=? WHERE id=?`, id, other); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, s := range []*Store{first, second} {
		if _, err := s.ClaimDailyTaskRun(context.Background(), id, now.Format("2006-01-02"), "manual", now, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDailyTasksMigrationPreservesExistingAccountsAndCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
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
	for _, migration := range sqliteMigrations[:22] {
		if err = s.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Create(ctx, accounts.CreateAccount{Name: "existing", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"access_token":"fixture-only","uid":"existing"}`)
	if _, err = db.Exec(`INSERT INTO account_credential_payloads (account_id, format, payload, updated_at) VALUES (?, ?, ?, ?)`, a.ID, "workbuddy-oauth-v1", payload, formatTime(time.Now().UTC())); err != nil {
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
	got, err := upgraded.Get(ctx, a.ID)
	if err != nil || got.Name != a.Name || got.ProviderRegion != a.ProviderRegion {
		t.Fatalf("account changed: %+v %v", got, err)
	}
	if got.CredentialGeneration != 0 {
		t.Fatalf("legacy account credential generation = %d, want zero baseline", got.CredentialGeneration)
	}
	_, saved, err := upgraded.LoadCredentialPayload(ctx, a.ID)
	if err != nil || string(saved) != string(payload) {
		t.Fatal("credential changed")
	}
}

func TestDailyTasksConcurrentDatabaseConnectionsClaimOnce(t *testing.T) {
	s, id := dailyTestStore(t)
	var path string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var done sync.WaitGroup
	for _, candidate := range []*Store{s, other} {
		done.Add(1)
		go func(db *Store) {
			defer done.Done()
			<-start
			now := time.Now().UTC()
			_, err := db.ClaimDailyTaskRun(context.Background(), id, now.Format("2006-01-02"), "manual", now, time.Minute)
			results <- err
		}(candidate)
	}
	close(start)
	done.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, accounts.ErrDailyTasksBusy) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d", winners)
	}
}

func TestDailyTasksConfirmedStepsSurviveInterruptedRetry(t *testing.T) {
	s, id := dailyTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	first, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	finishTestRun(t, s, first, now, true)
	if _, err = s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(6*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimDailyTaskRun(ctx, id, date, "scheduled", now.Add(8*time.Minute), time.Minute)
	if err != nil || len(recovered.Report.Steps) != 2 || recovered.Report.Steps[0].Status != "success" {
		t.Fatalf("confirmed steps lost: %+v %v", recovered, err)
	}
}
