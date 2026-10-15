package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

// Existing unlinked history remains visible. Only explicit parent links suppress
// check-in child rows; timestamps are never used to guess historical ownership.
const maintenanceLogUnion = `
SELECT c.id,c.account_id,a.name AS account_name,a.provider,a.provider_region AS region,
 'checkin' AS kind,c.trigger,c.status,c.message AS summary,c.created_at,
 c.created_at AS finished_at,0 AS next_retry_at,1 AS attempt,'' AS report_json
FROM checkin_records c JOIN accounts a ON a.id=c.account_id
WHERE c.daily_task_run_id=''
UNION ALL
SELECT d.id,d.account_id,a.name,a.provider,a.provider_region,
 'daily_tasks',CASE WHEN d.trigger='scheduled' AND d.attempt>1 THEN 'retry' ELSE d.trigger END,
 d.status,'',d.started_at,d.finished_at,d.next_retry_at,d.attempt,d.report_json
FROM daily_task_runs d JOIN accounts a ON a.id=d.account_id
`

func (s *Store) RecordCheckinAttempt(ctx context.Context, id, status, message string, at time.Time) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO checkin_records(id,account_id,status,message,created_at,trigger) VALUES(?,?,?,?,?,?)`, newCheckinRecordID(), id, status, message, formatTime(at), accounts.MaintenanceTrigger(ctx))
	return err
}

func (s *Store) ListMaintenanceLogs(ctx context.Context, filter accounts.MaintenanceLogFilter) (accounts.MaintenanceLogPage, error) {
	page := accounts.MaintenanceLogPage{Items: make([]accounts.MaintenanceLog, 0), Limit: filter.Limit, Offset: filter.Offset}
	if page.Limit <= 0 {
		page.Limit = 20
	}
	if page.Limit > 100 {
		page.Limit = 100
	}
	if page.Offset < 0 {
		page.Offset = 0
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 10)
	for _, pair := range []struct{ column, value string }{
		{"account_id", filter.AccountID}, {"provider", filter.Provider}, {"region", filter.Region},
		{"kind", filter.Kind}, {"trigger", filter.Trigger}, {"status", filter.Status},
	} {
		if pair.value != "" {
			clauses = append(clauses, pair.column+"=?")
			args = append(args, pair.value)
		}
	}
	if filter.From != nil {
		clauses = append(clauses, "julianday(created_at)>=julianday(?)")
		args = append(args, formatTime(*filter.From))
	}
	if filter.To != nil {
		clauses = append(clauses, "julianday(created_at)<=julianday(?)")
		args = append(args, formatTime(*filter.To))
	}
	query := " FROM (" + maintenanceLogUnion + ") WHERE " + strings.Join(clauses, " AND ")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*)"+query, args...).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count maintenance logs: %w", err)
	}
	queryArgs := append(append([]any{}, args...), page.Limit, page.Offset)
	rows, err := tx.QueryContext(ctx, "SELECT *"+query+" ORDER BY julianday(created_at) DESC,id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return page, fmt.Errorf("list maintenance logs: %w", err)
	}
	for rows.Next() {
		var item accounts.MaintenanceLog
		var created, finished, payload string
		var retry int64
		if err = rows.Scan(&item.ID, &item.AccountID, &item.AccountName, &item.Provider, &item.Region,
			&item.Kind, &item.Trigger, &item.Status, &item.Summary, &created, &finished, &retry, &item.Attempt, &payload); err != nil {
			rows.Close()
			return page, err
		}
		item.CreatedAt = parseTime(created)
		if finished != "" {
			at := parseTime(finished)
			item.FinishedAt = &at
		}
		if retry > 0 {
			at := time.Unix(0, retry).UTC()
			item.NextRetryAt = &at
		}
		if item.Kind == "daily_tasks" {
			var report providers.DailyTasksReport
			if err = json.Unmarshal([]byte(payload), &report); err != nil {
				rows.Close()
				return page, fmt.Errorf("decode maintenance report: %w", err)
			}
			item.Steps = report.Steps
			item.Summary = report.Summary()
		} else {
			item.Steps = []providers.DailyTaskStep{{Step: "checkin", Status: item.Status, Message: item.Summary, OK: item.Status == "success" || item.Status == "already"}}
		}
		if item.Steps == nil {
			item.Steps = make([]providers.DailyTaskStep, 0)
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	page.Count = len(page.Items)
	return page, tx.Commit()
}
