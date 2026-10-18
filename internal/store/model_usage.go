package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

const (
	maxModelUsageGroups      = 2000
	maxBudgetObservationRows = 20000
)

func (s *Store) ReadModelUsage(ctx context.Context, filter accounts.ModelUsageQuery) (accounts.ModelUsageData, error) {
	if filter.From.IsZero() || filter.To.IsZero() || filter.From.After(filter.To) {
		return accounts.ModelUsageData{}, fmt.Errorf("valid model usage window required")
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxModelUsageGroups {
		limit = 500
	}
	where, args := modelUsageWhere(filter)
	data := accounts.ModelUsageData{Rows: []accounts.ModelUsageRow{}, Recent: []accounts.ModelUsageError{}, BudgetObservations: []accounts.ModelBudgetObservation{}, From: filter.From.UTC(), To: filter.To.UTC()}
	query := `
SELECT
  COALESCE(NULLIF(ra.provider,''), rl.provider, ''),
  COALESCE(NULLIF(ra.provider_region,''), acct.provider_region, 'unknown'),
  ra.account_id,
  COALESCE(NULLIF(ra.model,''), NULLIF(rl.mapped_model,''), rl.requested_model),
  COUNT(DISTINCT ra.request_id), COUNT(*),
  SUM(CASE WHEN ra.status='ok' THEN 1 ELSE 0 END),
  SUM(CASE WHEN ra.status='error' THEN 1 ELSE 0 END),
  SUM(CASE WHEN ra.status='canceled' THEN 1 ELSE 0 END),
  SUM(CASE WHEN ra.status='failover' THEN 1 ELSE 0 END),
  COUNT(DISTINCT CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN ra.request_id END),
  SUM(CASE WHEN ra.usage_state='actual' AND ra.prompt_tokens IS NOT NULL THEN ra.prompt_tokens ELSE 0 END),
  SUM(CASE WHEN ra.usage_state='actual' AND ra.completion_tokens IS NOT NULL THEN ra.completion_tokens ELSE 0 END),
  SUM(CASE WHEN ra.usage_state='actual' AND ra.prompt_tokens IS NOT NULL AND ra.completion_tokens IS NOT NULL THEN 1 ELSE 0 END),
  SUM(CASE WHEN ra.usage_state='estimated' THEN 1 ELSE 0 END),
  SUM(CASE WHEN ra.usage_state!='estimated' AND (ra.usage_state!='actual' OR ra.prompt_tokens IS NULL OR ra.completion_tokens IS NULL) THEN 1 ELSE 0 END),
  MAX(CASE WHEN ra.status='ok' THEN ra.finished_at END)
FROM request_attempts ra
JOIN request_logs rl ON rl.id=ra.request_id
LEFT JOIN accounts acct ON acct.id=ra.account_id
WHERE ` + where + `
GROUP BY 1,2,3,4
ORDER BY MAX(CASE WHEN ra.status='ok' THEN ra.finished_at END) DESC, COUNT(*) DESC
LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return accounts.ModelUsageData{}, fmt.Errorf("query model usage groups: %w", err)
	}
	for rows.Next() {
		var row accounts.ModelUsageRow
		var prompt, completion sql.NullInt64
		var lastSuccess sql.NullString
		err = rows.Scan(&row.Provider, &row.Region, &row.AccountID, &row.Model, &row.Requests, &row.Attempts,
			&row.SucceededAttempts, &row.FailedAttempts, &row.CanceledAttempts, &row.FailoverAttempts, &row.LengthRequests,
			&prompt, &completion, &row.KnownUsageAttempts, &row.EstimatedUsageAttempts, &row.MissingUsageAttempts, &lastSuccess)
		if err != nil {
			rows.Close()
			return accounts.ModelUsageData{}, fmt.Errorf("scan model usage group: %w", err)
		}
		if prompt.Valid {
			value := prompt.Int64
			row.PromptTokens = &value
		}
		if completion.Valid {
			value := completion.Int64
			row.CompletionTokens = &value
		}
		if lastSuccess.Valid && lastSuccess.String != "" {
			value := parseTime(lastSuccess.String)
			row.LastSuccessAt = &value
		}
		data.Rows = append(data.Rows, row)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return accounts.ModelUsageData{}, fmt.Errorf("iterate model usage groups: %w", err)
	}
	rows.Close()
	if len(data.Rows) > limit {
		data.Rows = data.Rows[:limit]
		data.Truncated = true
	}
	if err := s.attachRequestCacheUsage(ctx, filter, data.Rows); err != nil {
		return accounts.ModelUsageData{}, err
	}
	if err := s.listModelUsageErrors(ctx, filter, &data); err != nil {
		return accounts.ModelUsageData{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(filter.Provider), "trae") {
		budgetFilter := filter
		budgetFilter.From = filter.To.UTC().Add(-7 * 24 * time.Hour)
		// Reference budgets need comparable samples from other accounts even
		// while the operator filters the visible account list to one account.
		budgetFilter.Account = ""
		data.BudgetObservationFrom = budgetFilter.From
		if err := s.listWorkBuddyBudgetObservations(ctx, budgetFilter, &data); err != nil {
			return accounts.ModelUsageData{}, err
		}
	}
	return data, nil
}

func (s *Store) listWorkBuddyBudgetObservations(ctx context.Context, filter accounts.ModelUsageQuery, data *accounts.ModelUsageData) error {
	conditions := []string{
		"COALESCE(ra.finished_at,ra.started_at) >= ?",
		"COALESCE(ra.finished_at,ra.started_at) < ?",
		"COALESCE(NULLIF(ra.provider,''),rl.provider,'')='workbuddy'",
	}
	args := []any{formatTime(filter.From.UTC()), formatTime(filter.To.UTC())}
	if region := strings.TrimSpace(strings.ToLower(filter.Region)); region != "" {
		conditions = append(conditions, "LOWER(COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'))=?")
		args = append(args, region)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		conditions = append(conditions, "COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model)=?")
		args = append(args, model)
	}
	where := strings.Join(conditions, " AND ")
	query := `
SELECT COALESCE(ra.finished_at,ra.started_at),ra.account_id,
       COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'),
       COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model),
       ra.provider_plan,rl.request_path,ra.status,ra.error_kind,ra.error_code,ra.error_scope,ra.upstream_reset_at,
       ra.usage_state,ra.prompt_tokens,ra.completion_tokens
FROM request_attempts ra JOIN request_logs rl ON rl.id=ra.request_id
LEFT JOIN accounts acct ON acct.id=ra.account_id
WHERE ` + where + `
ORDER BY COALESCE(ra.finished_at,ra.started_at) DESC,ra.id DESC LIMIT ?`
	args = append(args, maxBudgetObservationRows+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query WorkBuddy budget observations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item accounts.ModelBudgetObservation
		var at string
		var reset sql.NullString
		var prompt, completion sql.NullInt64
		if err := rows.Scan(&at, &item.AccountID, &item.Region, &item.Model, &item.Plan, &item.RequestPath, &item.Status, &item.Kind,
			&item.Code, &item.Scope, &reset, &item.UsageState, &prompt, &completion); err != nil {
			return fmt.Errorf("scan WorkBuddy budget observation: %w", err)
		}
		item.At = parseTime(at)
		if reset.Valid && reset.String != "" {
			value := parseTime(reset.String)
			item.UpstreamResetAt = &value
		}
		if prompt.Valid {
			value := prompt.Int64
			item.PromptTokens = &value
		}
		if completion.Valid {
			value := completion.Int64
			item.CompletionTokens = &value
		}
		data.BudgetObservations = append(data.BudgetObservations, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate WorkBuddy budget observations: %w", err)
	}
	if len(data.BudgetObservations) > maxBudgetObservationRows {
		data.BudgetObservations = data.BudgetObservations[:maxBudgetObservationRows]
		data.BudgetObservationsTruncated = true
	}
	return nil
}

func modelUsageWhere(filter accounts.ModelUsageQuery) (string, []any) {
	conditions := []string{"rl.created_at >= ?", "rl.created_at < ?", "COALESCE(NULLIF(ra.provider,''),rl.provider,'') IN ('workbuddy','trae')"}
	args := []any{formatTime(filter.From.UTC()), formatTime(filter.To.UTC())}
	if provider := strings.TrimSpace(strings.ToLower(filter.Provider)); provider != "" {
		conditions = append(conditions, "COALESCE(NULLIF(ra.provider,''),rl.provider,'')=?")
		args = append(args, provider)
	}
	if region := strings.TrimSpace(strings.ToLower(filter.Region)); region != "" {
		conditions = append(conditions, "LOWER(COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'))=?")
		args = append(args, region)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		conditions = append(conditions, "COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model)=?")
		args = append(args, model)
	}
	if account := strings.TrimSpace(filter.Account); account != "" {
		conditions = append(conditions, "ra.account_id=?")
		args = append(args, account)
	}
	return strings.Join(conditions, " AND "), args
}

func (s *Store) attachRequestCacheUsage(ctx context.Context, filter accounts.ModelUsageQuery, rows []accounts.ModelUsageRow) error {
	if len(rows) == 0 {
		return nil
	}
	query := `
SELECT rl.provider, COALESCE(acct.provider_region,'unknown'), COALESCE(NULLIF(rl.account_id,''),''),
       COALESCE(NULLIF(rl.mapped_model,''),rl.requested_model),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') THEN rl.cache_read_tokens ELSE NULL END),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') THEN rl.cache_write_tokens ELSE NULL END),
       COUNT(*),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') AND rl.cache_read_tokens IS NOT NULL THEN 1 ELSE 0 END),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') AND rl.cache_read_tokens IS NULL THEN 1 ELSE 0 END)
FROM request_logs rl
LEFT JOIN accounts acct ON acct.id=rl.account_id
WHERE rl.created_at >= ? AND rl.created_at < ? AND rl.provider IN ('workbuddy','trae')`
	args := []any{formatTime(filter.From.UTC()), formatTime(filter.To.UTC())}
	if provider := strings.TrimSpace(strings.ToLower(filter.Provider)); provider != "" {
		query += " AND rl.provider=?"
		args = append(args, provider)
	}
	if region := strings.TrimSpace(strings.ToLower(filter.Region)); region != "" {
		query += " AND LOWER(COALESCE(acct.provider_region,'unknown'))=?"
		args = append(args, region)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		query += " AND COALESCE(NULLIF(rl.mapped_model,''),rl.requested_model)=?"
		args = append(args, model)
	}
	if account := strings.TrimSpace(filter.Account); account != "" {
		query += " AND rl.account_id=?"
		args = append(args, account)
	}
	query += " GROUP BY 1,2,3,4"
	cacheRows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query request cache usage: %w", err)
	}
	defer cacheRows.Close()
	byKey := make(map[string]*accounts.ModelUsageRow, len(rows))
	for i := range rows {
		byKey[modelUsageKey(rows[i].Provider, rows[i].Region, rows[i].AccountID, rows[i].Model)] = &rows[i]
	}
	for cacheRows.Next() {
		var provider, region, account, model string
		var read, write sql.NullInt64
		var total, known, missing int
		if err := cacheRows.Scan(&provider, &region, &account, &model, &read, &write, &total, &known, &missing); err != nil {
			return fmt.Errorf("scan request cache usage: %w", err)
		}
		row := byKey[modelUsageKey(provider, region, account, model)]
		if row == nil {
			continue
		}
		if read.Valid {
			value := read.Int64
			row.CacheReadTokens = &value
		}
		if write.Valid {
			value := write.Int64
			row.CacheWriteTokens = &value
		}
		row.CacheKnownRequests, row.CacheMissingRequests = known, total-known
	}
	return cacheRows.Err()
}

func (s *Store) listModelUsageErrors(ctx context.Context, filter accounts.ModelUsageQuery, data *accounts.ModelUsageData) error {
	where, args := modelUsageWhere(filter)
	query := `
SELECT COALESCE(ra.finished_at,ra.started_at),ra.request_id,ra.id,
       COALESCE(NULLIF(ra.provider,''),rl.provider,''),
       COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'),
       ra.account_id,COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model),
       ra.status,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'output_limit' ELSE ra.error_kind END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'length' ELSE ra.error_code END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'output' ELSE ra.error_scope END,
       ra.upstream_reset_at,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN rl.finish_reason ELSE '' END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN rl.finish_reason_source ELSE '' END
FROM request_attempts ra JOIN request_logs rl ON rl.id=ra.request_id
LEFT JOIN accounts acct ON acct.id=ra.account_id
WHERE ` + where + ` AND ((ra.status!='ok' AND ra.error_kind!='') OR (rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id)))
ORDER BY COALESCE(ra.finished_at,ra.started_at) DESC LIMIT 100`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query model usage errors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item accounts.ModelUsageError
		var at string
		var reset sql.NullString
		if err := rows.Scan(&at, &item.RequestID, &item.AttemptID, &item.Provider, &item.Region, &item.AccountID,
			&item.Model, &item.Status, &item.Kind, &item.Code, &item.Scope, &reset, &item.FinishReason, &item.FinishReasonSource); err != nil {
			return fmt.Errorf("scan model usage error: %w", err)
		}
		item.At = parseTime(at)
		if reset.Valid && reset.String != "" {
			value := parseTime(reset.String)
			item.UpstreamResetAt = &value
		}
		data.Recent = append(data.Recent, item)
	}
	return rows.Err()
}

func modelUsageKey(provider, region, account, model string) string {
	return provider + "\x00" + region + "\x00" + account + "\x00" + model
}

var _ accounts.ModelUsageStore = (*Store)(nil)
