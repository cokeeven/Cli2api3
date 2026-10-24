package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

const maxCompactCatalogEntries = 5000

func (s *Store) ReadModelUsageOverview(ctx context.Context, filter accounts.ModelUsageOverviewQuery) (accounts.ModelUsageOverviewData, error) {
	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("valid model usage window required")
	}
	page, pageSize := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	catalog := filter.CatalogModels
	catalogLimited := filter.CatalogLimited || len(catalog) > maxCompactCatalogEntries
	if len(catalog) > maxCompactCatalogEntries {
		catalog = catalog[:maxCompactCatalogEntries]
	}
	cte, catalogArgs := modelUsageCatalogCTE(catalog)
	baseWhere, baseArgs := compactModelUsageWhere(filter.Provider, filter.Region, filter.From, filter.To)
	filterSQL, filterArgs := compactModelUsageFilter(filter.Search, filter.Activity)
	args := append(append(append([]any{}, catalogArgs...), baseArgs...), filterArgs...)
	queryCTE := cte + `,
base AS (
  SELECT COALESCE(NULLIF(ra.provider,''),rl.provider,'') AS provider,
         LOWER(COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown')) AS region,
         COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model) AS model,
         ra.request_id, ra.account_id, ra.status, ra.error_kind, ra.error_code, ra.error_scope,
         ra.usage_state, ra.prompt_tokens, ra.completion_tokens,
         ra.finished_at, ra.started_at, rl.finish_reason, ra.attempt_index,
         (SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) AS last_attempt_index
  FROM request_attempts ra
  JOIN request_logs rl ON rl.id=ra.request_id
  LEFT JOIN accounts acct ON acct.id=ra.account_id
  WHERE ` + baseWhere + `
),
aggregated AS (
  SELECT provider, region, model,
         COUNT(DISTINCT request_id) AS requests,
         COUNT(*) AS attempts,
         SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END) AS succeeded_attempts,
         SUM(CASE WHEN status='error' THEN 1 ELSE 0 END) AS failed_attempts,
         SUM(CASE WHEN status='canceled' THEN 1 ELSE 0 END) AS canceled_attempts,
         SUM(CASE WHEN status='failover' THEN 1 ELSE 0 END) AS failover_attempts,
         SUM(CASE WHEN status='failover' AND (
           TRIM(COALESCE(error_kind,''))<>'' OR TRIM(COALESCE(error_code,''))<>'' OR TRIM(COALESCE(error_scope,''))<>'')
           THEN 1 ELSE 0 END) AS error_evidence_failover_attempts,
         COUNT(DISTINCT CASE WHEN finish_reason='length' AND attempt_index=last_attempt_index THEN request_id END) AS length_requests,
         SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL THEN prompt_tokens ELSE 0 END) AS prompt_tokens_sum,
         SUM(CASE WHEN usage_state='actual' AND completion_tokens IS NOT NULL THEN completion_tokens ELSE 0 END) AS completion_tokens_sum,
         SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL AND completion_tokens IS NOT NULL THEN 1 ELSE 0 END) AS known_usage_attempts,
         SUM(CASE WHEN usage_state='estimated' THEN 1 ELSE 0 END) AS estimated_usage_attempts,
         SUM(CASE WHEN usage_state!='estimated' AND (usage_state!='actual' OR prompt_tokens IS NULL OR completion_tokens IS NULL) THEN 1 ELSE 0 END) AS missing_usage_attempts,
	         COUNT(DISTINCT NULLIF(account_id,'')) AS account_count,
         MAX(COALESCE(finished_at,started_at)) AS last_activity
  FROM base
  GROUP BY provider,region,model
),
model_keys AS (
  SELECT provider,region,model FROM aggregated
  UNION
  SELECT provider,region,model FROM catalog
),
joined AS (
  SELECT k.provider,k.region,k.model,
         COALESCE(a.requests,0) AS requests, COALESCE(a.attempts,0) AS attempts,
         COALESCE(a.succeeded_attempts,0) AS succeeded_attempts, COALESCE(a.failed_attempts,0) AS failed_attempts,
         COALESCE(a.canceled_attempts,0) AS canceled_attempts, COALESCE(a.failover_attempts,0) AS failover_attempts,
         COALESCE(a.error_evidence_failover_attempts,0) AS error_evidence_failover_attempts,
         COALESCE(a.length_requests,0) AS length_requests,
         CASE WHEN COALESCE(a.known_usage_attempts,0)>0 THEN a.prompt_tokens_sum ELSE NULL END AS prompt_tokens,
         CASE WHEN COALESCE(a.known_usage_attempts,0)>0 THEN a.completion_tokens_sum ELSE NULL END AS completion_tokens,
         COALESCE(a.known_usage_attempts,0) AS known_usage_attempts,
         COALESCE(a.estimated_usage_attempts,0) AS estimated_usage_attempts,
         COALESCE(a.missing_usage_attempts,0) AS missing_usage_attempts,
         COALESCE(a.account_count,0) AS account_count,
         CASE WHEN COALESCE(a.requests,0)>0 THEN 1 ELSE 0 END AS recent_usage,
         a.last_activity
  FROM model_keys k
  LEFT JOIN aggregated a ON a.provider=k.provider AND a.region=k.region AND a.model=k.model
),
filtered AS (SELECT * FROM joined WHERE ` + filterSQL + `)`

	var result accounts.ModelUsageOverviewData
	result.Rows = []accounts.ModelUsageOverviewRow{}
	result.Page, result.PageSize = page, pageSize
	result.From, result.To, result.CatalogLimited = filter.From.UTC(), filter.To.UTC(), catalogLimited
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("begin model usage overview snapshot: %w", err)
	}
	defer tx.Rollback()
	summaryQuery := queryCTE + `
SELECT COUNT(*),
       COALESCE(SUM(attempts),0),
       COALESCE(SUM(prompt_tokens),0),
       COALESCE(SUM(completion_tokens),0),
       COALESCE(SUM(known_usage_attempts),0),
       COALESCE(SUM(estimated_usage_attempts),0),
       COALESCE(SUM(missing_usage_attempts),0),
       (SELECT COUNT(DISTINCT b.request_id) FROM base b JOIN filtered f ON f.provider=b.provider AND f.region=b.region AND f.model=b.model)
FROM filtered`
	var summary accounts.ModelUsageOverviewSummary
	if err := tx.QueryRowContext(ctx, summaryQuery, args...).Scan(&result.Total, &summary.Attempts, &summary.PromptTokens,
		&summary.CompletionTokens, &summary.KnownUsageAttempts, &summary.EstimatedUsageAttempts, &summary.MissingUsageAttempts, &summary.Requests); err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("summarize model usage overview: %w", err)
	}
	summary.Models = result.Total
	result.Summary = summary
	pageQuery := queryCTE + `
SELECT provider,region,model,requests,attempts,succeeded_attempts,failed_attempts,canceled_attempts,failover_attempts,length_requests,
       prompt_tokens,completion_tokens,known_usage_attempts,estimated_usage_attempts,missing_usage_attempts,account_count,recent_usage,last_activity
FROM filtered
ORDER BY provider,region,model
LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := tx.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("query model usage overview page: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item accounts.ModelUsageOverviewRow
		var prompt, completion sql.NullInt64
		var lastActivity sql.NullString
		var recent int
		if err := rows.Scan(&item.Provider, &item.Region, &item.Model, &item.Requests, &item.Attempts, &item.SucceededAttempts,
			&item.FailedAttempts, &item.CanceledAttempts, &item.FailoverAttempts, &item.LengthRequests, &prompt, &completion,
			&item.KnownUsageAttempts, &item.EstimatedUsageAttempts, &item.MissingUsageAttempts, &item.AccountCount, &recent, &lastActivity); err != nil {
			return accounts.ModelUsageOverviewData{}, fmt.Errorf("scan model usage overview row: %w", err)
		}
		if prompt.Valid {
			value := prompt.Int64
			item.PromptTokens = &value
		}
		if completion.Valid {
			value := completion.Int64
			item.CompletionTokens = &value
		}
		if recent != 0 {
			item.RecentUsage = true
		}
		if lastActivity.Valid && lastActivity.String != "" {
			value := parseTime(lastActivity.String)
			item.LastActivityAt = &value
		}
		result.Rows = append(result.Rows, item)
	}
	if err := rows.Err(); err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("iterate model usage overview page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("close model usage overview page: %w", err)
	}
	result.HasMore = page*pageSize < result.Total
	if err := tx.Commit(); err != nil {
		return accounts.ModelUsageOverviewData{}, fmt.Errorf("finish model usage overview snapshot: %w", err)
	}
	return result, nil
}

func (s *Store) ReadModelUsageDetail(ctx context.Context, filter accounts.ModelUsageDetailQuery) (accounts.ModelUsageDetailData, error) {
	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || strings.TrimSpace(filter.Model) == "" {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("valid model usage detail selection required")
	}
	page, pageSize := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	catalog := filter.CatalogAccounts
	catalogLimited := filter.CatalogLimited || len(catalog) > maxCompactCatalogEntries
	if len(catalog) > maxCompactCatalogEntries {
		catalog = catalog[:maxCompactCatalogEntries]
	}
	cte, catalogArgs := modelUsageAccountCatalogCTE(catalog)
	where, whereArgs := compactModelUsageWhere(filter.Provider, filter.Region, filter.From, filter.To)
	where += " AND COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model)=?"
	whereArgs = append(whereArgs, filter.Model)
	queryCTE := cte + `,
base AS (
  SELECT COALESCE(NULLIF(ra.provider,''),rl.provider,'') AS provider,
         LOWER(COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown')) AS region,
         COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model) AS model,
         ra.request_id, ra.account_id, ra.status, ra.usage_state, ra.prompt_tokens, ra.completion_tokens,
         ra.finished_at, ra.started_at, rl.finish_reason, ra.attempt_index,
         (SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) AS last_attempt_index
  FROM request_attempts ra JOIN request_logs rl ON rl.id=ra.request_id
  LEFT JOIN accounts acct ON acct.id=ra.account_id
  WHERE ` + where + `
),
aggregated AS (
  SELECT account_id,
         COUNT(DISTINCT request_id) AS requests, COUNT(*) AS attempts,
         SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END) AS succeeded_attempts,
         SUM(CASE WHEN status='error' THEN 1 ELSE 0 END) AS failed_attempts,
         SUM(CASE WHEN status='canceled' THEN 1 ELSE 0 END) AS canceled_attempts,
         SUM(CASE WHEN status='failover' THEN 1 ELSE 0 END) AS failover_attempts,
         COUNT(DISTINCT CASE WHEN finish_reason='length' AND attempt_index=last_attempt_index THEN request_id END) AS length_requests,
         CASE WHEN SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL AND completion_tokens IS NOT NULL THEN 1 ELSE 0 END)>0
              THEN SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL THEN prompt_tokens ELSE 0 END) ELSE NULL END AS prompt_tokens,
         CASE WHEN SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL AND completion_tokens IS NOT NULL THEN 1 ELSE 0 END)>0
              THEN SUM(CASE WHEN usage_state='actual' AND completion_tokens IS NOT NULL THEN completion_tokens ELSE 0 END) ELSE NULL END AS completion_tokens,
         SUM(CASE WHEN usage_state='actual' AND prompt_tokens IS NOT NULL AND completion_tokens IS NOT NULL THEN 1 ELSE 0 END) AS known_usage_attempts,
         SUM(CASE WHEN usage_state='estimated' THEN 1 ELSE 0 END) AS estimated_usage_attempts,
         SUM(CASE WHEN usage_state!='estimated' AND (usage_state!='actual' OR prompt_tokens IS NULL OR completion_tokens IS NULL) THEN 1 ELSE 0 END) AS missing_usage_attempts,
         MAX(CASE WHEN status='ok' THEN finished_at END) AS last_success_at
  FROM base GROUP BY account_id
),
eligible AS (SELECT account_id FROM aggregated UNION SELECT account_id FROM catalog),
joined AS (
  SELECT e.account_id,COALESCE(a.requests,0) AS requests,COALESCE(a.attempts,0) AS attempts,
         COALESCE(a.succeeded_attempts,0) AS succeeded_attempts,COALESCE(a.failed_attempts,0) AS failed_attempts,
         COALESCE(a.canceled_attempts,0) AS canceled_attempts,COALESCE(a.failover_attempts,0) AS failover_attempts,
         COALESCE(a.length_requests,0) AS length_requests,a.prompt_tokens,a.completion_tokens,
         COALESCE(a.known_usage_attempts,0) AS known_usage_attempts,
         COALESCE(a.estimated_usage_attempts,0) AS estimated_usage_attempts,
         COALESCE(a.missing_usage_attempts,0) AS missing_usage_attempts,a.last_success_at
  FROM eligible e LEFT JOIN aggregated a ON a.account_id=e.account_id
)`
	args := append(append([]any{}, catalogArgs...), whereArgs...)
	var result accounts.ModelUsageDetailData
	result.Rows = []accounts.ModelUsageAccountRow{}
	result.Page, result.PageSize = page, pageSize
	result.From, result.To = filter.From.UTC(), filter.To.UTC()
	result.CatalogLimited = catalogLimited
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("begin model usage detail snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, queryCTE+` SELECT COUNT(*) FROM joined`, args...).Scan(&result.Total); err != nil {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("count model usage account rows: %w", err)
	}
	order := "account_id"
	if filter.Sort == "errors" {
		order = "failed_attempts DESC, account_id"
	}
	pageQuery := queryCTE + `
SELECT account_id,requests,attempts,succeeded_attempts,failed_attempts,canceled_attempts,failover_attempts,length_requests,
       prompt_tokens,completion_tokens,known_usage_attempts,estimated_usage_attempts,missing_usage_attempts,last_success_at
FROM joined ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := tx.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("query model usage account page: %w", err)
	}
	for rows.Next() {
		var item accounts.ModelUsageAccountRow
		var prompt, completion sql.NullInt64
		var lastSuccess sql.NullString
		if err := rows.Scan(&item.AccountID, &item.Requests, &item.Attempts, &item.SucceededAttempts, &item.FailedAttempts,
			&item.CanceledAttempts, &item.FailoverAttempts, &item.LengthRequests, &prompt, &completion, &item.KnownUsageAttempts,
			&item.EstimatedUsageAttempts, &item.MissingUsageAttempts, &lastSuccess); err != nil {
			rows.Close()
			return accounts.ModelUsageDetailData{}, fmt.Errorf("scan model usage account row: %w", err)
		}
		if prompt.Valid {
			value := prompt.Int64
			item.PromptTokens = &value
		}
		if completion.Valid {
			value := completion.Int64
			item.CompletionTokens = &value
		}
		if lastSuccess.Valid && lastSuccess.String != "" {
			value := parseTime(lastSuccess.String)
			item.LastSuccessAt = &value
		}
		result.Rows = append(result.Rows, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return accounts.ModelUsageDetailData{}, fmt.Errorf("iterate model usage account page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("close model usage account page: %w", err)
	}
	result.HasMore = page*pageSize < result.Total
	if err := attachCompactRequestCacheUsage(ctx, tx, filter, result.Rows); err != nil {
		return accounts.ModelUsageDetailData{}, err
	}
	if err := tx.Commit(); err != nil {
		return accounts.ModelUsageDetailData{}, fmt.Errorf("finish model usage detail snapshot: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(filter.Provider), "workbuddy") {
		observationFilter := accounts.ModelUsageQuery{Provider: "workbuddy", Region: filter.Region, Model: filter.Model,
			From: filter.To.UTC().Add(-7 * 24 * time.Hour), To: filter.To.UTC(), Limit: 1}
		result.BudgetObservationFrom = observationFilter.From
		result.BudgetObservations = []accounts.ModelBudgetObservation{}
		budgetData := accounts.ModelUsageData{BudgetObservations: result.BudgetObservations}
		if err := s.listWorkBuddyBudgetObservations(ctx, observationFilter, &budgetData); err != nil {
			return accounts.ModelUsageDetailData{}, err
		}
		result.BudgetObservations = budgetData.BudgetObservations
		result.BudgetObservationsTruncated = budgetData.BudgetObservationsTruncated
	}
	return result, nil
}

func (s *Store) ReadModelUsageDiagnostics(ctx context.Context, filter accounts.ModelUsageDiagnosticsQuery) (accounts.ModelUsageDiagnosticsData, error) {
	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("valid model usage diagnostic window required")
	}
	page, pageSize := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	where, args := compactModelUsageWhere(filter.Provider, filter.Region, filter.From, filter.To)
	if strings.TrimSpace(filter.Model) != "" {
		where += " AND COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model)=?"
		args = append(args, filter.Model)
	}
	if strings.TrimSpace(filter.Account) != "" {
		where += " AND ra.account_id=?"
		args = append(args, filter.Account)
	}
	where += ` AND ((ra.status!='ok' AND ra.error_kind!='') OR (rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id)))`
	from := ` FROM request_attempts ra JOIN request_logs rl ON rl.id=ra.request_id LEFT JOIN accounts acct ON acct.id=ra.account_id WHERE ` + where
	var result accounts.ModelUsageDiagnosticsData
	result.Errors = []accounts.ModelUsageError{}
	result.Page, result.PageSize, result.From, result.To = page, pageSize, filter.From.UTC(), filter.To.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("begin model usage diagnostics snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*)"+from, args...).Scan(&result.Total); err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("count model usage diagnostics: %w", err)
	}
	query := `SELECT COALESCE(ra.finished_at,ra.started_at),ra.request_id,ra.id,
       COALESCE(NULLIF(ra.provider,''),rl.provider,''),
       COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'),
       ra.account_id,COALESCE(NULLIF(ra.model,''),NULLIF(rl.mapped_model,''),rl.requested_model),ra.status,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'output_limit' ELSE ra.error_kind END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'length' ELSE ra.error_code END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN 'output' ELSE ra.error_scope END,
       ra.upstream_reset_at,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN rl.finish_reason ELSE '' END,
       CASE WHEN rl.finish_reason='length' AND ra.attempt_index=(SELECT MAX(last_attempt.attempt_index) FROM request_attempts last_attempt WHERE last_attempt.request_id=ra.request_id) THEN rl.finish_reason_source ELSE '' END` + from + `
ORDER BY COALESCE(ra.finished_at,ra.started_at) DESC,ra.id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := tx.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("query model usage diagnostics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item accounts.ModelUsageError
		var at string
		var reset sql.NullString
		if err := rows.Scan(&at, &item.RequestID, &item.AttemptID, &item.Provider, &item.Region, &item.AccountID,
			&item.Model, &item.Status, &item.Kind, &item.Code, &item.Scope, &reset, &item.FinishReason, &item.FinishReasonSource); err != nil {
			return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("scan model usage diagnostic: %w", err)
		}
		item.At = parseTime(at)
		if reset.Valid && reset.String != "" {
			value := parseTime(reset.String)
			item.UpstreamResetAt = &value
		}
		result.Errors = append(result.Errors, item)
	}
	if err := rows.Err(); err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("iterate model usage diagnostics: %w", err)
	}
	if err := rows.Close(); err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("close model usage diagnostics: %w", err)
	}
	result.HasMore = page*pageSize < result.Total
	if err := tx.Commit(); err != nil {
		return accounts.ModelUsageDiagnosticsData{}, fmt.Errorf("finish model usage diagnostics snapshot: %w", err)
	}
	return result, nil
}

// attachCompactRequestCacheUsage preserves the legacy request-log cache
// semantics while limiting the aggregation to this model and account page.
// request_logs has one row per logical request, so retries are not counted
// again through request_attempts.
func attachCompactRequestCacheUsage(ctx context.Context, tx *sql.Tx, filter accounts.ModelUsageDetailQuery, rows []accounts.ModelUsageAccountRow) error {
	if len(rows) == 0 {
		return nil
	}
	accountRows := make([]*accounts.ModelUsageAccountRow, 0, len(rows))
	placeholders := make([]string, 0, len(rows))
	args := []any{formatTime(filter.From.UTC()), formatTime(filter.To.UTC()), strings.ToLower(strings.TrimSpace(filter.Provider)), strings.ToLower(strings.TrimSpace(filter.Region)), strings.TrimSpace(filter.Model)}
	for index := range rows {
		if rows[index].AccountID == "" {
			continue
		}
		accountRows = append(accountRows, &rows[index])
		placeholders = append(placeholders, "?")
		args = append(args, rows[index].AccountID)
	}
	if len(accountRows) == 0 {
		return nil
	}
	query := `
SELECT COALESCE(NULLIF(rl.account_id,''),''),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') THEN rl.cache_read_tokens ELSE NULL END),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') THEN rl.cache_write_tokens ELSE NULL END),
       COUNT(*),
       SUM(CASE WHEN rl.usage_source IN ('upstream','provider') AND rl.cache_read_tokens IS NOT NULL THEN 1 ELSE 0 END)
FROM request_logs rl
LEFT JOIN accounts acct ON acct.id=rl.account_id
WHERE rl.created_at >= ? AND rl.created_at < ? AND rl.provider=?
  AND LOWER(COALESCE(acct.provider_region,'unknown'))=?
  AND COALESCE(NULLIF(rl.mapped_model,''),rl.requested_model)=?
  AND rl.account_id IN (` + strings.Join(placeholders, ",") + `)
GROUP BY 1`
	cacheRows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query compact request cache usage: %w", err)
	}
	defer cacheRows.Close()
	byAccount := make(map[string]*accounts.ModelUsageAccountRow, len(accountRows))
	for _, row := range accountRows {
		byAccount[row.AccountID] = row
	}
	for cacheRows.Next() {
		var accountID string
		var read, write sql.NullInt64
		var total, known int
		if err := cacheRows.Scan(&accountID, &read, &write, &total, &known); err != nil {
			return fmt.Errorf("scan compact request cache usage: %w", err)
		}
		row := byAccount[accountID]
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
		row.CacheKnownRequests = known
		row.CacheMissingRequests = total - known
	}
	if err := cacheRows.Err(); err != nil {
		return fmt.Errorf("iterate compact request cache usage: %w", err)
	}
	if err := cacheRows.Close(); err != nil {
		return fmt.Errorf("close compact request cache usage: %w", err)
	}
	return nil
}

func modelUsageCatalogCTE(models []accounts.ModelUsageCatalogModel) (string, []any) {
	if len(models) == 0 {
		return "WITH catalog(provider,region,model) AS (SELECT '', '', '' WHERE 0)", nil
	}
	values := make([]string, len(models))
	args := make([]any, 0, len(models)*3)
	for index, item := range models {
		values[index] = "(?,?,?)"
		args = append(args, item.Provider, strings.ToLower(strings.TrimSpace(item.Region)), strings.TrimSpace(item.Model))
	}
	return "WITH catalog(provider,region,model) AS (VALUES " + strings.Join(values, ",") + ")", args
}

func modelUsageAccountCatalogCTE(accountsList []string) (string, []any) {
	if len(accountsList) == 0 {
		return "WITH catalog(account_id) AS (SELECT '' WHERE 0)", nil
	}
	values := make([]string, len(accountsList))
	args := make([]any, 0, len(accountsList))
	for index, account := range accountsList {
		values[index] = "(?)"
		args = append(args, account)
	}
	return "WITH catalog(account_id) AS (VALUES " + strings.Join(values, ",") + ")", args
}

func compactModelUsageWhere(provider, region string, from, to time.Time) (string, []any) {
	conditions := []string{"rl.created_at >= ?", "rl.created_at < ?", "COALESCE(NULLIF(ra.provider,''),rl.provider,'') IN ('workbuddy','trae')"}
	args := []any{formatTime(from.UTC()), formatTime(to.UTC())}
	if provider = strings.TrimSpace(strings.ToLower(provider)); provider != "" {
		conditions = append(conditions, "COALESCE(NULLIF(ra.provider,''),rl.provider,'')=?")
		args = append(args, provider)
	}
	if region = strings.TrimSpace(strings.ToLower(region)); region != "" {
		conditions = append(conditions, "LOWER(COALESCE(NULLIF(ra.provider_region,''),acct.provider_region,'unknown'))=?")
		args = append(args, region)
	}
	return strings.Join(conditions, " AND "), args
}

func compactModelUsageFilter(search, activity string) (string, []any) {
	conditions := []string{"1=1"}
	args := []any{}
	if search = strings.TrimSpace(search); search != "" {
		conditions = append(conditions, "instr(lower(model),lower(?))>0")
		args = append(args, search)
	}
	switch activity {
	case "used":
		conditions = append(conditions, "requests>0")
	case "errors":
		// Include recovered failovers only when their attempt recorded error
		// evidence. A user cancellation by itself remains outside this filter.
		conditions = append(conditions, "(failed_attempts>0 OR error_evidence_failover_attempts>0 OR length_requests>0)")
	case "unused":
		conditions = append(conditions, "requests=0")
	}
	return strings.Join(conditions, " AND "), args
}

var _ accounts.CompactModelUsageStore = (*Store)(nil)
