package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestMaintenanceKindsReuseLeaseAndKeepIndependentDueHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "maintenance.db")
	db, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run, err := db.ClaimMaintenanceTaskRun(ctx, account.ID, "credential", "scheduled", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.ClaimDailyTaskRun(ctx, account.ID, now.Format("2006-01-02"), "manual", now, time.Minute); !errors.Is(err, accounts.ErrDailyTasksBusy) {
		t.Fatalf("daily task raced credential lease: %v", err)
	}
	if _, err = other.ClaimMaintenanceTaskRun(ctx, account.ID, "quota", "manual", now, time.Minute); !errors.Is(err, accounts.ErrDailyTasksBusy) {
		t.Fatalf("quota raced credential lease: %v", err)
	}
	finish := now.Add(time.Second)
	retry := finish.Add(time.Minute)
	run.FinishedAt = &finish
	run.NextRetryAt = &retry
	run.Report = providers.DailyTasksReport{}
	run.Report.Add("credential", "error", "synthetic error")
	run.Status = run.Report.Status()
	if err = db.FinishDailyTaskRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err = other.ClaimMaintenanceTaskRun(ctx, account.ID, "credential", "scheduled", finish, time.Minute); !errors.Is(err, accounts.ErrDailyTasksNotDue) {
		t.Fatalf("persisted backoff lost: %v", err)
	}
	manual, err := other.ClaimMaintenanceTaskRun(ctx, account.ID, "credential", "manual", finish, time.Minute)
	if err != nil {
		t.Fatal("manual retry blocked", err)
	}
	finish = finish.Add(time.Second)
	manual.FinishedAt = &finish
	manual.Report = providers.DailyTasksReport{}
	manual.Report.Add("credential", "success", "checked")
	manual.Status = manual.Report.Status()
	if err = other.FinishDailyTaskRun(ctx, manual); err != nil {
		t.Fatal(err)
	}
	daily, err := db.ClaimDailyTaskRun(ctx, account.ID, now.Format("2006-01-02"), "scheduled", finish, time.Minute)
	if err != nil {
		t.Fatal("credential success incorrectly completed daily tasks", err)
	}
	if len(daily.Report.Steps) != 0 || daily.Attempt != 1 {
		t.Fatalf("reused foreign kind report: %+v", daily)
	}
	page, err := db.ListMaintenanceLogs(ctx, accounts.MaintenanceLogFilter{Kind: "credential"})
	if err != nil || page.Total != 2 {
		t.Fatalf("history missing %d %v", page.Total, err)
	}
	runs, err := db.ListDailyTaskRuns(ctx, account.ID, 20)
	if err != nil || len(runs) != 1 || runs[0].Kind != "daily_tasks" {
		t.Fatal("legacy daily history contaminated", err)
	}
}

func TestMaintenanceSuccessDueUsesCurrentConfiguredInterval(t *testing.T) {
	for _, kind := range []string{"credential", "quota"} {
		for _, scenario := range []string{"shorter", "longer", "failure_backoff"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				db, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				account, err := db.Create(ctx, accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				start := now.Add(-10 * time.Minute)
				run, err := db.ClaimMaintenanceTaskRun(ctx, account.ID, kind, "manual", start, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				finish := start.Add(time.Second)
				oldNext := now.Add(24 * time.Hour)
				interval := "5"
				status := "success"
				wantDue := true
				if scenario == "longer" {
					interval = "60"
					oldNext = finish.Add(5 * time.Minute)
					wantDue = false
				}
				if scenario == "failure_backoff" {
					status = "error"
					wantDue = false
				}
				run.FinishedAt = &finish
				run.NextRetryAt = &oldNext
				run.Report = providers.DailyTasksReport{}
				run.Report.Add(kind, status, "synthetic")
				run.Status = run.Report.Status()
				if err := db.FinishDailyTaskRun(ctx, run); err != nil {
					t.Fatal(err)
				}
				if err := db.SetSecret(ctx, accounts.QuotaRefreshIntervalSecret, interval); err != nil {
					t.Fatal(err)
				}
				if err := db.SetSecret(ctx, accounts.MaintenanceScheduleSecret, `{"credential_enabled":true,"credential_interval_minutes":`+interval+`,"quota_enabled":true,"daily_enabled":false,"daily_time":"22:00"}`); err != nil {
					t.Fatal(err)
				}
				_, err = db.ClaimMaintenanceTaskRun(ctx, account.ID, kind, "scheduled", now, time.Minute)
				if wantDue && err != nil {
					t.Fatalf("shortened successful interval still blocked by old schedule: %v", err)
				}
				if !wantDue && !errors.Is(err, accounts.ErrDailyTasksNotDue) {
					t.Fatalf("current interval/backoff not respected: %v", err)
				}
			})
		}
	}
}
