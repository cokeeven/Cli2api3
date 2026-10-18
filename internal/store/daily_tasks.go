package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func (s *Store) ClaimDailyTaskRun(ctx context.Context, accountID, date, trigger string, now time.Time, lease time.Duration) (accounts.DailyTaskRun, error) {
	return s.claimTaskRun(ctx, accountID, date, "daily_tasks", trigger, now, lease, 0)
}

func (s *Store) ClaimMaintenanceTaskRun(ctx context.Context, accountID, kind, trigger string, now time.Time, lease time.Duration) (accounts.DailyTaskRun, error) {
	if kind != "credential" && kind != "quota" && kind != "checkin" {
		return accounts.DailyTaskRun{}, fmt.Errorf("unsupported maintenance kind")
	}
	var successInterval time.Duration
	if trigger == "scheduled" {
		switch kind {
		case "credential":
			schedule, err := accounts.LoadMaintenanceSchedule(ctx, s)
			if err != nil {
				return accounts.DailyTaskRun{}, err
			}
			successInterval = time.Duration(schedule.CredentialIntervalMinutes) * time.Minute
		case "quota":
			minutes, err := accounts.QuotaRefreshIntervalMinutes(ctx, s)
			if err != nil {
				return accounts.DailyTaskRun{}, err
			}
			successInterval = time.Duration(minutes) * time.Minute
		}
	}
	return s.claimTaskRun(ctx, accountID, now.Format("2006-01-02"), kind, trigger, now, lease, successInterval)
}

func (s *Store) claimTaskRun(ctx context.Context, accountID, date, kind, trigger string, now time.Time, lease, successInterval time.Duration) (accounts.DailyTaskRun, error) {
	var run accounts.DailyTaskRun
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return run, fmt.Errorf("invalid daily task date")
	}
	if (trigger != "manual" && trigger != "scheduled") || lease <= 0 {
		return run, fmt.Errorf("invalid daily task claim")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer tx.Rollback()
	notDue := func() (accounts.DailyTaskRun, error) {
		if err := tx.Commit(); err != nil {
			return run, err
		}
		return run, accounts.ErrDailyTasksNotDue
	}
	if _, err = tx.ExecContext(ctx, `UPDATE daily_task_runs SET status='interrupted',finished_at=?,lease_until=0 WHERE account_id=? AND status='running' AND lease_until<=?`, formatTime(now), accountID, now.UnixNano()); err != nil {
		return run, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_task_runs WHERE account_id=? AND status='running'`, accountID).Scan(&active); err != nil {
		return run, err
	}
	if active > 0 {
		return run, accounts.ErrDailyTasksBusy
	}
	attempt := 1
	if trigger == "scheduled" && kind != "daily_tasks" {
		var retry int64
		var previousStatus string
		var previousAttempt int
		var finished sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT next_retry_at,status,attempt,finished_at FROM daily_task_runs WHERE account_id=? AND kind=? ORDER BY rowid DESC LIMIT 1`, accountID, kind).Scan(&retry, &previousStatus, &previousAttempt, &finished)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return run, err
		}
		// Success follows the current configured period, including changes made
		// since it finished. Failure retries retain their persisted backoff.
		if previousStatus == "success" && successInterval > 0 && finished.Valid && !parseTime(finished.String).IsZero() {
			retry = parseTime(finished.String).Add(successInterval).UnixNano()
		}
		if retry > now.UnixNano() {
			return notDue()
		}
		if previousStatus == "error" {
			attempt = previousAttempt + 1
		}
	}
	if trigger == "scheduled" && kind == "daily_tasks" {
		var done, attempts int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_task_runs WHERE account_id=? AND run_date=? AND kind='daily_tasks' AND status IN ('success','skipped')`, accountID, date).Scan(&done); err != nil {
			return run, err
		}
		if done > 0 {
			return notDue()
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_task_runs WHERE account_id=? AND run_date=? AND kind='daily_tasks' AND trigger='scheduled'`, accountID, date).Scan(&attempts); err != nil {
			return run, err
		}
		if attempts >= 3 {
			return notDue()
		}
		attempt = attempts + 1
		var retry int64
		err = tx.QueryRowContext(ctx, `SELECT next_retry_at FROM daily_task_runs WHERE account_id=? AND run_date=? AND kind='daily_tasks' AND trigger='scheduled' ORDER BY rowid DESC LIMIT 1`, accountID, date).Scan(&retry)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return run, err
		}
		if retry > now.UnixNano() {
			return notDue()
		}
	}
	// Carry only today's confirmed results into a later attempt.
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT report_json FROM daily_task_runs WHERE account_id=? AND run_date=? AND kind=? ORDER BY rowid DESC LIMIT 1`, accountID, date, kind).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	if previous != "" {
		if err = json.Unmarshal([]byte(previous), &run.Report); err != nil {
			return run, fmt.Errorf("decode previous daily task report: %w", err)
		}
	}
	run.ID = "daily_" + strings.TrimPrefix(newAccountID(), "acc_")
	run.AccountID = accountID
	run.Kind = kind
	run.RunDate = date
	run.Trigger = trigger
	run.Status = "running"
	run.Attempt = attempt
	run.StartedAt = now.UTC()
	initialReport, err := json.Marshal(run.Report)
	if err != nil {
		return run, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daily_task_runs(id,account_id,run_date,kind,trigger,status,attempt,started_at,lease_until,report_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, run.ID, accountID, date, kind, trigger, "running", attempt, formatTime(now), now.Add(lease).UnixNano(), string(initialReport))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return run, accounts.ErrDailyTasksBusy
		}
		return run, err
	}
	if err = tx.Commit(); err != nil {
		return run, err
	}
	return run, nil
}

func (s *Store) FinishDailyTaskRun(ctx context.Context, run accounts.DailyTaskRun) error {
	if run.FinishedAt == nil {
		return fmt.Errorf("daily task finish time required")
	}
	if run.Status != run.Report.Status() {
		return fmt.Errorf("daily task status does not match report")
	}
	payload, err := json.Marshal(run.Report)
	if err != nil {
		return err
	}
	var retry int64
	if run.NextRetryAt != nil {
		retry = run.NextRetryAt.UnixNano()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE daily_task_runs SET status=?,finished_at=?,lease_until=0,next_retry_at=?,report_json=? WHERE id=? AND account_id=? AND status='running' AND lease_until>?`, run.Status, formatTime(*run.FinishedAt), retry, string(payload), run.ID, run.AccountID, run.FinishedAt.UnixNano())
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return accounts.ErrDailyTasksLeaseLost
	}
	for _, step := range run.Report.Steps {
		if step.Step != "checkin" || (run.Kind != "" && run.Kind != "daily_tasks") {
			continue
		}
		// Resume markers are not another check-in operation/history entry.
		if step.Reused {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET last_checkin_at=?,last_checkin_msg=?,last_checkin_status=?,updated_at=? WHERE id=?`, formatTime(*run.FinishedAt), step.Message, step.Status, formatTime(*run.FinishedAt), run.AccountID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO checkin_records(id,account_id,status,message,created_at,trigger,daily_task_run_id) VALUES(?,?,?,?,?,?,?)`, newCheckinRecordID(), run.AccountID, step.Status, step.Message, formatTime(*run.FinishedAt), run.Trigger, run.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListDailyTaskRuns(ctx context.Context, accountID string, limit int) ([]accounts.DailyTaskRun, error) {
	if _, err := s.Get(ctx, accountID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,account_id,run_date,trigger,status,attempt,started_at,finished_at,next_retry_at,report_json,kind FROM daily_task_runs WHERE account_id=? AND kind='daily_tasks' ORDER BY rowid DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]accounts.DailyTaskRun, 0)
	for rows.Next() {
		var run accounts.DailyTaskRun
		var start, finish, payload string
		var retry int64
		if err = rows.Scan(&run.ID, &run.AccountID, &run.RunDate, &run.Trigger, &run.Status, &run.Attempt, &start, &finish, &retry, &payload, &run.Kind); err != nil {
			return nil, err
		}
		run.StartedAt = parseTime(start)
		if finish != "" {
			at := parseTime(finish)
			run.FinishedAt = &at
		}
		if retry > 0 {
			at := time.Unix(0, retry).UTC()
			run.NextRetryAt = &at
		}
		if err = json.Unmarshal([]byte(payload), &run.Report); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
