package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"strings"
	"time"
)

func (s *Store) InsertRequestLog(ctx context.Context, log accounts.RequestLog) error {
	if strings.TrimSpace(log.ID) == "" {
		return fmt.Errorf("request log id required")
	}
	if strings.TrimSpace(log.Status) == "" {
		log.Status = accounts.RequestStatusStarted
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}
	var finished any
	if log.FinishedAt != nil {
		finished = formatTime(*log.FinishedAt)
	}
	_, err := execSQLiteBusyRetry(ctx, s.db, `
	INSERT INTO request_logs (
		  id, created_at, finished_at, stream, status, requested_model, mapped_model, requested_reasoning, resolved_reasoning, account_id, provider, routing,
		  prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, usage_source, credits,
		  latency_ms, ttfb_ms, error_kind, error_code, error_message, attempt_count, message_count, empty_message_indexes, message_roles,
		  client_request_id, output_limit_tokens, output_limit_source, prompt_stripped, message_shape_known, source_fingerprint, finish_reason, finish_reason_source, request_path, output_budget_enforcement
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, formatTime(log.CreatedAt), finished, boolToInt(log.Stream), log.Status,
		log.RequestedModel, log.MappedModel, strings.TrimSpace(log.RequestedReasoning), strings.TrimSpace(log.ResolvedReasoning), nullIfEmpty(log.AccountID), strings.TrimSpace(log.Provider), strings.TrimSpace(log.Routing),
		nullableInt(log.PromptTokens), nullableInt(log.CompletionTokens),
		nullableInt(log.CacheReadTokens), nullableInt(log.CacheWriteTokens),
		log.UsageSource, nullableFloat(log.Credits), nullableInt(log.LatencyMs), nullableInt(log.TTFBMs),
		log.ErrorKind, log.ErrorCode, log.ErrorMessage, log.AttemptCount, log.MessageCount, encodeIntSlice(log.EmptyMessageIndexes), encodeStringSlice(log.MessageRoles),
		log.ClientRequestID, nullableInt(log.OutputLimit), log.OutputLimitSource, nullableBool(log.PromptStripped), boolToInt(log.MessageShapeKnown),
		log.SourceFingerprint, log.FinishReason, log.FinishReasonSource, log.RequestPath, log.OutputBudgetEnforcement,
	)
	if err != nil {
		return fmt.Errorf("insert request log: %w", err)
	}
	return nil
}

func (s *Store) UpdateRequestLog(ctx context.Context, log accounts.RequestLog) error {
	if strings.TrimSpace(log.ID) == "" {
		return fmt.Errorf("request log id required")
	}
	var finished any
	if log.FinishedAt != nil {
		finished = formatTime(*log.FinishedAt)
	}
	result, err := execSQLiteBusyRetry(ctx, s.db, `
		UPDATE request_logs SET
		  finished_at = ?, status = ?, requested_model = ?, mapped_model = ?, requested_reasoning = ?, resolved_reasoning = ?, account_id = ?, provider = ?, routing = ?,
		  prompt_tokens = ?, completion_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
		  usage_source = ?, credits = ?, latency_ms = ?, ttfb_ms = ?,
		  error_kind = ?, error_code = ?, error_message = ?, attempt_count = ?,
		  message_count = CASE WHEN ? THEN ? ELSE message_count END,
		  empty_message_indexes = CASE WHEN ? THEN ? ELSE empty_message_indexes END,
		  message_roles = CASE WHEN ? THEN ? ELSE message_roles END,
		  message_shape_known = CASE WHEN ? THEN 1 ELSE message_shape_known END,
		  client_request_id = CASE WHEN ? <> '' THEN ? ELSE client_request_id END,
		  output_limit_tokens = COALESCE(?, output_limit_tokens),
		  output_limit_source = CASE WHEN ? <> '' THEN ? ELSE output_limit_source END,
		  prompt_stripped = COALESCE(?, prompt_stripped),
		  finish_reason = CASE WHEN ? <> '' THEN ? ELSE finish_reason END,
		  finish_reason_source = CASE WHEN ? <> '' THEN ? ELSE finish_reason_source END,
		  request_path = CASE WHEN ? <> '' THEN ? ELSE request_path END
		  , output_budget_enforcement = CASE WHEN ? <> '' THEN ? ELSE output_budget_enforcement END
		WHERE id = ?`,
		finished, log.Status, log.RequestedModel, log.MappedModel, strings.TrimSpace(log.RequestedReasoning), strings.TrimSpace(log.ResolvedReasoning), nullIfEmpty(log.AccountID), strings.TrimSpace(log.Provider), strings.TrimSpace(log.Routing),
		nullableInt(log.PromptTokens), nullableInt(log.CompletionTokens),
		nullableInt(log.CacheReadTokens), nullableInt(log.CacheWriteTokens),
		log.UsageSource, nullableFloat(log.Credits), nullableInt(log.LatencyMs), nullableInt(log.TTFBMs),
		log.ErrorKind, log.ErrorCode, log.ErrorMessage, log.AttemptCount,
		boolToInt(log.MessageShapeKnown), log.MessageCount,
		boolToInt(log.MessageShapeKnown), encodeIntSlice(log.EmptyMessageIndexes),
		boolToInt(log.MessageShapeKnown), encodeStringSlice(log.MessageRoles),
		boolToInt(log.MessageShapeKnown),
		log.ClientRequestID, log.ClientRequestID,
		nullableInt(log.OutputLimit), log.OutputLimitSource, log.OutputLimitSource,
		nullableBool(log.PromptStripped), log.FinishReason, log.FinishReason, log.FinishReasonSource, log.FinishReasonSource,
		log.RequestPath, log.RequestPath, log.OutputBudgetEnforcement, log.OutputBudgetEnforcement, log.ID,
	)
	if err != nil {
		return fmt.Errorf("update request log: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return accounts.ErrRequestLogNotFound
	}
	return nil
}

func (s *Store) InsertRequestAttempt(ctx context.Context, attempt accounts.RequestAttempt) error {
	if strings.TrimSpace(attempt.ID) == "" {
		return fmt.Errorf("request attempt id required")
	}
	if strings.TrimSpace(attempt.RequestID) == "" {
		return fmt.Errorf("request id required")
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}
	var finished any
	if attempt.FinishedAt != nil {
		finished = formatTime(*attempt.FinishedAt)
	}
	_, err := execSQLiteBusyRetry(ctx, s.db, `
INSERT INTO request_attempts (
  id, request_id, attempt_index, account_id, started_at, finished_at, status, http_status,
  error_kind, error_message, latency_ms, prompt_tokens, completion_tokens, usage_source, provider, model, key_id, credits, usage_state, error_scope, route_reason,
  provider_region, error_code, upstream_reset_at, provider_plan, error_phase, error_source
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.RequestID, attempt.AttemptIndex, attempt.AccountID,
		formatTime(attempt.StartedAt), finished, attempt.Status, nullableInt(attempt.HTTPStatus),
		attempt.ErrorKind, attempt.ErrorMessage, nullableInt(attempt.LatencyMs),
		nullableInt(attempt.PromptTokens), nullableInt(attempt.CompletionTokens), attempt.UsageSource,
		attempt.Provider, attempt.Model, attempt.KeyID, nullableFloat(attempt.Credits), attempt.UsageState, attempt.ErrorScope, attempt.RouteReason,
		attempt.Region, attempt.ErrorCode, nullableTimePtr(attempt.UpstreamResetAt), attempt.ProviderPlan, attempt.ErrorPhase, attempt.ErrorSource,
	)
	if err != nil {
		return fmt.Errorf("insert request attempt: %w", err)
	}
	return nil
}

func (s *Store) ListRequestLogs(ctx context.Context, filter accounts.RequestLogFilter) (accounts.RequestLogList, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	where, args := buildRequestLogWhere(filter)
	countQuery := "SELECT COUNT(*) FROM request_logs" + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return accounts.RequestLogList{}, fmt.Errorf("count request logs: %w", err)
	}

	query := `
			SELECT id, created_at, finished_at, stream, status, requested_model, mapped_model, requested_reasoning, resolved_reasoning, account_id,
			       COALESCE(NULLIF(provider, ''), (SELECT provider FROM accounts WHERE accounts.id = request_logs.account_id), ''), routing,
			       prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, usage_source,
			       COALESCE(credits, (SELECT credit FROM request_usage_details WHERE request_usage_details.request_id = request_logs.id)),
			       latency_ms, ttfb_ms, error_kind, error_code, error_message, attempt_count, message_count, empty_message_indexes, message_roles,
			       client_request_id, output_limit_tokens, output_limit_source, prompt_stripped, message_shape_known, source_fingerprint, finish_reason, finish_reason_source, request_path, output_budget_enforcement
			FROM request_logs` + where + ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return accounts.RequestLogList{}, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()

	items := make([]accounts.RequestLog, 0, limit)
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return accounts.RequestLogList{}, fmt.Errorf("scan request log: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return accounts.RequestLogList{}, err
	}
	return accounts.RequestLogList{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *Store) SummarizeRequestLogs(ctx context.Context, from, to time.Time) (accounts.RequestStats, error) {
	if from.IsZero() {
		from = time.Now().UTC().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now().UTC()
	}
	from = from.UTC()
	to = to.UTC()
	if !to.After(from) {
		to = from.Add(time.Hour)
	}
	hours := int(to.Sub(from).Round(time.Hour) / time.Hour)
	if hours < 1 {
		hours = 1
	}

	filter := accounts.RequestLogFilter{From: &from, To: &to}
	where, args := buildRequestLogWhere(filter)
	stats := accounts.RequestStats{
		Window:    accounts.RequestStatsWindow{From: from, To: to, Hours: hours},
		Status:    make([]accounts.RequestStatsBucket, 0),
		Errors:    make([]accounts.RequestStatsBucket, 0),
		Models:    make([]accounts.RequestStatsNamed, 0),
		Accounts:  make([]accounts.RequestStatsNamed, 0),
		Providers: make([]accounts.RequestStatsNamed, 0),
		Series:    make([]accounts.RequestStatsPoint, 0, hours),
	}

	row := s.db.QueryRowContext(ctx, `
SELECT
	  COUNT(*),
	  COALESCE(SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN status = 'incomplete' THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'canceled' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN stream = 1 THEN 1 ELSE 0 END), 0),
  AVG(CASE WHEN latency_ms IS NOT NULL THEN latency_ms END),
  AVG(CASE WHEN ttfb_ms IS NOT NULL THEN ttfb_ms END),
  COALESCE(SUM(COALESCE(prompt_tokens, 0)), 0),
  COALESCE(SUM(COALESCE(completion_tokens, 0)), 0),
  COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0)
FROM request_logs`+where, args...)
	var avgLatency, avgTTFB sql.NullFloat64
	if err := row.Scan(
		&stats.Totals.Requests, &stats.Totals.OK, &stats.Totals.Incomplete, &stats.Totals.Error, &stats.Totals.Canceled, &stats.Totals.Streaming,
		&avgLatency, &avgTTFB, &stats.Tokens.Prompt, &stats.Tokens.Completion, &stats.Tokens.CacheRead,
	); err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request logs: %w", err)
	}
	stats.Tokens.Total = stats.Tokens.Prompt + stats.Tokens.Completion
	if stats.Totals.Requests > 0 {
		stats.Totals.SuccessRate = float64(stats.Totals.OK) / float64(stats.Totals.Requests)
	}
	stats.Latency.AvgMs = roundedNullInt(avgLatency)
	stats.Latency.TTFBAvgMs = roundedNullInt(avgTTFB)
	stats.Latency.P50Ms, stats.Latency.P95Ms = s.requestLatencyPercentiles(ctx, where, args)

	statusRows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*) FROM request_logs`+where+` GROUP BY status ORDER BY COUNT(*) DESC, status ASC`, args...)
	if err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request status: %w", err)
	}
	stats.Status, err = scanCountBuckets(statusRows)
	if err != nil {
		return accounts.RequestStats{}, err
	}

	errorRows, err := s.db.QueryContext(ctx, `
SELECT error_kind, COUNT(*) FROM request_logs`+whereAnd(where, "error_kind <> ''")+`
GROUP BY error_kind ORDER BY COUNT(*) DESC, error_kind ASC LIMIT 8`, args...)
	if err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request errors: %w", err)
	}
	stats.Errors, err = scanCountBuckets(errorRows)
	if err != nil {
		return accounts.RequestStats{}, err
	}

	modelRows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(NULLIF(requested_model, ''), '(unknown)'), COUNT(*),
       COALESCE(SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
       AVG(CASE WHEN latency_ms IS NOT NULL THEN latency_ms END)
FROM request_logs`+where+` GROUP BY 1 ORDER BY COUNT(*) DESC, 1 ASC LIMIT 6`, args...)
	if err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request models: %w", err)
	}
	stats.Models, err = scanNamedBuckets(modelRows)
	if err != nil {
		return accounts.RequestStats{}, err
	}

	accountRows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(NULLIF(account_id, ''), '(unassigned)'), COUNT(*),
       COALESCE(SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
       AVG(CASE WHEN latency_ms IS NOT NULL THEN latency_ms END)
FROM request_logs`+where+` GROUP BY 1 ORDER BY COUNT(*) DESC, 1 ASC LIMIT 6`, args...)
	if err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request accounts: %w", err)
	}
	stats.Accounts, err = scanNamedBuckets(accountRows)
	if err != nil {
		return accounts.RequestStats{}, err
	}

	providerRows, err := s.db.QueryContext(ctx, `
	SELECT COALESCE(NULLIF(provider, ''), (SELECT provider FROM accounts WHERE accounts.id = request_logs.account_id), '(unknown)'), COUNT(*),
	       COALESCE(SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END), 0),
	       COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
	       AVG(CASE WHEN latency_ms IS NOT NULL THEN latency_ms END)
	FROM request_logs`+where+` GROUP BY 1 ORDER BY COUNT(*) DESC, 1 ASC LIMIT 6`, args...)
	if err != nil {
		return accounts.RequestStats{}, fmt.Errorf("summarize request providers: %w", err)
	}
	stats.Providers, err = scanNamedBuckets(providerRows)
	if err != nil {
		return accounts.RequestStats{}, err
	}

	series, err := s.requestSeries(ctx, from, to, where, args)
	if err != nil {
		return accounts.RequestStats{}, err
	}
	stats.Series = series
	return stats, nil
}

func (s *Store) requestLatencyPercentiles(ctx context.Context, where string, args []any) (*int, *int) {
	rows, err := s.db.QueryContext(ctx, `SELECT latency_ms FROM request_logs`+whereAnd(where, "latency_ms IS NOT NULL")+` ORDER BY latency_ms ASC`, args...)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	values := make([]int, 0, 128)
	for rows.Next() {
		var value int
		if err := rows.Scan(&value); err != nil {
			return nil, nil
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return roundedInt(percentileNearestRank(values, 50)), roundedInt(percentileNearestRank(values, 95))
}

func (s *Store) requestSeries(ctx context.Context, from, to time.Time, where string, args []any) ([]accounts.RequestStatsPoint, error) {
	span := to.Sub(from)
	daily := span > 48*time.Hour
	quarter := !daily && span <= 2*time.Hour
	step := time.Hour
	trunc := from.Truncate(time.Hour)
	end := to.Truncate(time.Hour)
	if daily {
		step = 24 * time.Hour
		trunc = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
		end = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	} else if quarter {
		step = 15 * time.Minute
		trunc = from.Truncate(15 * time.Minute)
		end = to.Truncate(15 * time.Minute)
	}
	if to.After(end) {
		end = end.Add(step)
	}
	points := make([]accounts.RequestStatsPoint, 0)
	index := map[string]int{}
	for cursor := trunc; cursor.Before(end); cursor = cursor.Add(step) {
		index[seriesKey(cursor, daily, quarter)] = len(points)
		points = append(points, accounts.RequestStatsPoint{At: cursor.UTC()})
	}
	if len(points) == 0 {
		return points, nil
	}

	expr := "substr(created_at, 1, 13)"
	if daily {
		expr = "substr(created_at, 1, 10)"
	} else if quarter {
		expr = "substr(created_at, 1, 14) || printf('%02d', (CAST(substr(created_at, 15, 2) AS INTEGER) / 15) * 15)"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT %s, COUNT(*),
       COALESCE(SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0)
FROM request_logs%s GROUP BY 1 ORDER BY 1 ASC`, expr, where), args...)
	if err != nil {
		return nil, fmt.Errorf("summarize request series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucket string
		var requests, okCount, errorCount int
		if err := rows.Scan(&bucket, &requests, &okCount, &errorCount); err != nil {
			return nil, fmt.Errorf("scan request series: %w", err)
		}
		if i, exists := index[normalizeSeriesBucket(bucket, daily, quarter)]; exists {
			points[i].Requests = requests
			points[i].OK = okCount
			points[i].Error = errorCount
		}
	}
	return points, rows.Err()
}

func seriesKey(value time.Time, daily, quarter bool) string {
	utc := value.UTC()
	if daily {
		return utc.Format("2006-01-02")
	}
	if quarter {
		return utc.Truncate(15 * time.Minute).Format("2006-01-02T15:04")
	}
	return utc.Format("2006-01-02T15")
}

func normalizeSeriesBucket(bucket string, daily, quarter bool) string {
	text := strings.TrimSpace(bucket)
	if daily {
		if len(text) >= 10 {
			return text[:10]
		}
		return text
	}
	if quarter {
		if len(text) >= 16 {
			parsed, err := time.Parse("2006-01-02T15:04", text[:16])
			if err == nil {
				return parsed.UTC().Truncate(15 * time.Minute).Format("2006-01-02T15:04")
			}
			return text[:16]
		}
		return text
	}
	if len(text) >= 13 {
		return text[:13]
	}
	return text
}

func scanCountBuckets(rows *sql.Rows) ([]accounts.RequestStatsBucket, error) {
	defer rows.Close()
	items := make([]accounts.RequestStatsBucket, 0)
	for rows.Next() {
		var item accounts.RequestStatsBucket
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return nil, fmt.Errorf("scan request bucket: %w", err)
		}
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanNamedBuckets(rows *sql.Rows) ([]accounts.RequestStatsNamed, error) {
	defer rows.Close()
	items := make([]accounts.RequestStatsNamed, 0)
	for rows.Next() {
		var item accounts.RequestStatsNamed
		var avg sql.NullFloat64
		if err := rows.Scan(&item.Key, &item.Count, &item.OK, &item.Error, &avg); err != nil {
			return nil, fmt.Errorf("scan named request bucket: %w", err)
		}
		item.LatencyAvgMs = roundedNullInt(avg)
		items = append(items, item)
	}
	return items, rows.Err()
}

func whereAnd(where, extra string) string {
	if strings.TrimSpace(where) == "" {
		return " WHERE " + extra
	}
	return where + " AND " + extra
}

func roundedNullInt(value sql.NullFloat64) *int {
	if !value.Valid {
		return nil
	}
	rounded := int(value.Float64 + 0.5)
	return &rounded
}

func roundedInt(value int) *int {
	return &value
}

func percentileNearestRank(sorted []int, percentile int) int {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int((float64(percentile) / 100) * float64(len(sorted)))
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func (s *Store) GetRequestLog(ctx context.Context, id string) (accounts.RequestLog, error) {
	row := s.db.QueryRowContext(ctx, `
			SELECT id, created_at, finished_at, stream, status, requested_model, mapped_model, requested_reasoning, resolved_reasoning, account_id,
			       COALESCE(NULLIF(provider, ''), (SELECT provider FROM accounts WHERE accounts.id = request_logs.account_id), ''), routing,
			       prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, usage_source,
			       COALESCE(credits, (SELECT credit FROM request_usage_details WHERE request_usage_details.request_id = request_logs.id)),
			       latency_ms, ttfb_ms, error_kind, error_code, error_message, attempt_count, message_count, empty_message_indexes, message_roles,
			       client_request_id, output_limit_tokens, output_limit_source, prompt_stripped, message_shape_known, source_fingerprint, finish_reason, finish_reason_source, request_path, output_budget_enforcement
			FROM request_logs WHERE id = ?`, strings.TrimSpace(id))
	log, err := scanRequestLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.RequestLog{}, accounts.ErrRequestLogNotFound
	}
	if err != nil {
		return accounts.RequestLog{}, fmt.Errorf("get request log: %w", err)
	}
	attempts, err := s.listRequestAttempts(ctx, log.ID)
	if err != nil {
		return accounts.RequestLog{}, err
	}
	log.Attempts = attempts
	diagnostic, err := s.getRequestStreamDiagnostic(ctx, log.ID)
	if err != nil {
		return accounts.RequestLog{}, err
	}
	log.StreamDiagnostic = diagnostic
	log.RoutingDiagnostic, err = s.requestRoutingDiagnostic(ctx, log.ID)
	if err != nil {
		return accounts.RequestLog{}, err
	}
	usageDetail, err := s.getRequestUsageDetail(ctx, log.ID)
	if err != nil {
		return accounts.RequestLog{}, err
	}
	log.UsageDetail = usageDetail
	return log, nil
}

func (s *Store) InsertRequestUsageDetail(ctx context.Context, detail accounts.RequestUsageDetail) error {
	if strings.TrimSpace(detail.RequestID) == "" {
		return fmt.Errorf("request usage detail request id required")
	}
	if detail.CreatedAt.IsZero() {
		detail.CreatedAt = time.Now().UTC()
	}
	unit := strings.TrimSpace(detail.Unit)
	if unit == "" {
		unit = "credits"
	}
	_, err := execSQLiteBusyRetry(ctx, s.db, `
INSERT INTO request_usage_details (request_id, created_at, provider, credit, unit)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(request_id) DO UPDATE SET
  provider = excluded.provider, credit = excluded.credit, unit = excluded.unit`,
		detail.RequestID, formatTime(detail.CreatedAt), detail.Provider, nullableFloat(detail.Credit), unit,
	)
	if err != nil {
		return fmt.Errorf("insert request usage detail: %w", err)
	}
	return nil
}

func (s *Store) getRequestUsageDetail(ctx context.Context, requestID string) (*accounts.RequestUsageDetail, error) {
	var detail accounts.RequestUsageDetail
	var created string
	var credit sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
SELECT request_id, created_at, provider, credit, unit
FROM request_usage_details WHERE request_id = ?`, requestID).Scan(
		&detail.RequestID, &created, &detail.Provider, &credit, &detail.Unit,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request usage detail: %w", err)
	}
	detail.CreatedAt = parseTime(created)
	if credit.Valid {
		value := credit.Float64
		detail.Credit = &value
	}
	return &detail, nil
}

func (s *Store) InsertRequestStreamDiagnostic(ctx context.Context, diagnostic accounts.RequestStreamDiagnostic) error {
	if strings.TrimSpace(diagnostic.RequestID) == "" {
		return fmt.Errorf("request stream diagnostic request id required")
	}
	if diagnostic.CreatedAt.IsZero() {
		diagnostic.CreatedAt = time.Now().UTC()
	}
	var finished any
	if diagnostic.FinishedAt != nil {
		finished = formatTime(*diagnostic.FinishedAt)
	}
	contentLength := any(-1) // legacy schema requires a value; known flag keeps this unknown.
	if diagnostic.ContentLength != nil {
		contentLength = *diagnostic.ContentLength
	}
	_, err := execSQLiteBusyRetry(ctx, s.db, `
INSERT INTO request_stream_diagnostics (
  request_id, created_at, finished_at, upstream_status, upstream_request_id, context_err,
  cancellation_source, relay_error, sse_event_count, bytes_read, content_length,
  last_event, saw_done, content_length_known, upstream_finish_reason,
  upstream_finish_reason_source, external_finish_reason, external_finish_reason_source,
  content_chars, reasoning_chars, tool_event_count, char_count_unit, cancellation_stage,
  prepare_duration_ms, upstream_response_duration_ms, first_event_duration_ms, total_duration_ms,
  first_content_duration_ms, timeout_phase, output_budget_enforcement
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(request_id) DO UPDATE SET
  finished_at = excluded.finished_at, upstream_status = excluded.upstream_status,
  upstream_request_id = excluded.upstream_request_id, context_err = excluded.context_err,
  cancellation_source = excluded.cancellation_source, relay_error = excluded.relay_error,
  sse_event_count = excluded.sse_event_count, bytes_read = excluded.bytes_read,
	content_length = CASE WHEN excluded.content_length_known = 1 THEN excluded.content_length ELSE content_length END,
	content_length_known = MAX(content_length_known, excluded.content_length_known),
	last_event = excluded.last_event, saw_done = excluded.saw_done,
	upstream_finish_reason = CASE WHEN excluded.upstream_finish_reason <> '' THEN excluded.upstream_finish_reason ELSE upstream_finish_reason END,
	upstream_finish_reason_source = CASE WHEN excluded.upstream_finish_reason_source <> '' THEN excluded.upstream_finish_reason_source ELSE upstream_finish_reason_source END,
	external_finish_reason = CASE WHEN excluded.external_finish_reason <> '' THEN excluded.external_finish_reason ELSE external_finish_reason END,
	external_finish_reason_source = CASE WHEN excluded.external_finish_reason_source <> '' THEN excluded.external_finish_reason_source ELSE external_finish_reason_source END,
	content_chars = COALESCE(excluded.content_chars, content_chars),
	reasoning_chars = COALESCE(excluded.reasoning_chars, reasoning_chars),
	tool_event_count = COALESCE(excluded.tool_event_count, tool_event_count),
	char_count_unit = CASE WHEN excluded.char_count_unit <> '' THEN excluded.char_count_unit ELSE char_count_unit END,
	cancellation_stage = CASE WHEN excluded.cancellation_stage <> '' THEN excluded.cancellation_stage ELSE cancellation_stage END,
	prepare_duration_ms = COALESCE(excluded.prepare_duration_ms, prepare_duration_ms),
	upstream_response_duration_ms = COALESCE(excluded.upstream_response_duration_ms, upstream_response_duration_ms),
	first_event_duration_ms = COALESCE(excluded.first_event_duration_ms, first_event_duration_ms),
	total_duration_ms = COALESCE(excluded.total_duration_ms, total_duration_ms),
	first_content_duration_ms = COALESCE(excluded.first_content_duration_ms, first_content_duration_ms),
	timeout_phase = CASE WHEN excluded.timeout_phase <> '' THEN excluded.timeout_phase ELSE timeout_phase END,
	output_budget_enforcement = CASE WHEN excluded.output_budget_enforcement <> '' THEN excluded.output_budget_enforcement ELSE output_budget_enforcement END`,
		diagnostic.RequestID, formatTime(diagnostic.CreatedAt), finished, nullableInt(diagnostic.UpstreamStatus),
		diagnostic.UpstreamRequestID, diagnostic.ContextErr, diagnostic.CancellationSource, diagnostic.RelayError,
		diagnostic.SSEEventCount, diagnostic.BytesRead, contentLength, diagnostic.LastEvent,
		boolToInt(diagnostic.SawDone), boolToInt(diagnostic.ContentLength != nil),
		diagnostic.UpstreamFinishReason, diagnostic.UpstreamFinishReasonSource,
		diagnostic.ExternalFinishReason, diagnostic.ExternalFinishReasonSource,
		nullableInt(diagnostic.ContentChars), nullableInt(diagnostic.ReasoningChars), nullableInt(diagnostic.ToolEventCount),
		diagnostic.CharCountUnit, diagnostic.CancellationStage,
		nullableInt(diagnostic.PrepareDurationMs), nullableInt(diagnostic.UpstreamResponseDurationMs),
		nullableInt(diagnostic.FirstEventDurationMs), nullableInt(diagnostic.TotalDurationMs),
		nullableInt(diagnostic.FirstContentDurationMs), diagnostic.TimeoutPhase, diagnostic.OutputBudgetEnforcement,
	)
	if err != nil {
		return fmt.Errorf("insert request stream diagnostic: %w", err)
	}
	return nil
}

func (s *Store) getRequestStreamDiagnostic(ctx context.Context, requestID string) (*accounts.RequestStreamDiagnostic, error) {
	var diagnostic accounts.RequestStreamDiagnostic
	var created, upstreamRequestID, contextErr, cancellationSource, relayError string
	var finished sql.NullString
	var upstreamFinishReason, upstreamFinishReasonSource, externalFinishReason, externalFinishReasonSource string
	var charCountUnit, cancellationStage string
	var upstreamStatus, contentLength, contentChars, reasoningChars, toolEventCount sql.NullInt64
	var prepareDuration, upstreamResponseDuration, firstEventDuration, totalDuration sql.NullInt64
	var firstContentDuration sql.NullInt64
	var contentLengthKnown int
	var sawDone int
	err := s.db.QueryRowContext(ctx, `
SELECT request_id, created_at, finished_at, upstream_status, upstream_request_id, context_err,
       cancellation_source, relay_error, sse_event_count, bytes_read, content_length,
       last_event, saw_done, content_length_known, upstream_finish_reason,
       upstream_finish_reason_source, external_finish_reason, external_finish_reason_source,
       content_chars, reasoning_chars, tool_event_count, char_count_unit, cancellation_stage,
       prepare_duration_ms, upstream_response_duration_ms, first_event_duration_ms, total_duration_ms,
       first_content_duration_ms, timeout_phase, output_budget_enforcement
FROM request_stream_diagnostics WHERE request_id = ?`, requestID).Scan(
		&diagnostic.RequestID, &created, &finished, &upstreamStatus, &upstreamRequestID, &contextErr,
		&cancellationSource, &relayError, &diagnostic.SSEEventCount, &diagnostic.BytesRead,
		&contentLength, &diagnostic.LastEvent, &sawDone, &contentLengthKnown,
		&upstreamFinishReason, &upstreamFinishReasonSource, &externalFinishReason, &externalFinishReasonSource,
		&contentChars, &reasoningChars, &toolEventCount, &charCountUnit, &cancellationStage,
		&prepareDuration, &upstreamResponseDuration, &firstEventDuration, &totalDuration,
		&firstContentDuration, &diagnostic.TimeoutPhase, &diagnostic.OutputBudgetEnforcement,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request stream diagnostic: %w", err)
	}
	diagnostic.CreatedAt = parseTime(created)
	if finished.Valid && finished.String != "" {
		parsed := parseTime(finished.String)
		diagnostic.FinishedAt = &parsed
	}
	if upstreamStatus.Valid {
		diagnostic.UpstreamStatus = nullIntPtr(upstreamStatus)
	}
	diagnostic.UpstreamRequestID, diagnostic.ContextErr = upstreamRequestID, contextErr
	diagnostic.CancellationSource, diagnostic.RelayError = cancellationSource, relayError
	diagnostic.UpstreamFinishReason, diagnostic.UpstreamFinishReasonSource = upstreamFinishReason, upstreamFinishReasonSource
	diagnostic.ExternalFinishReason, diagnostic.ExternalFinishReasonSource = externalFinishReason, externalFinishReasonSource
	diagnostic.ContentLength = nil
	if contentLengthKnown != 0 {
		diagnostic.ContentLength = nullIntPtr(contentLength)
	}
	diagnostic.ContentChars = nullIntPtr(contentChars)
	diagnostic.ReasoningChars = nullIntPtr(reasoningChars)
	diagnostic.ToolEventCount = nullIntPtr(toolEventCount)
	diagnostic.CharCountUnit, diagnostic.CancellationStage = charCountUnit, cancellationStage
	diagnostic.PrepareDurationMs = nullIntPtr(prepareDuration)
	diagnostic.UpstreamResponseDurationMs = nullIntPtr(upstreamResponseDuration)
	diagnostic.FirstEventDurationMs = nullIntPtr(firstEventDuration)
	diagnostic.FirstContentDurationMs = nullIntPtr(firstContentDuration)
	diagnostic.TotalDurationMs = nullIntPtr(totalDuration)
	diagnostic.SawDone = sawDone != 0
	return &diagnostic, nil
}

func (s *Store) listRequestAttempts(ctx context.Context, requestID string) ([]accounts.RequestAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, request_id, attempt_index, account_id, started_at, finished_at, status, http_status,
       error_kind, error_message, latency_ms, prompt_tokens, completion_tokens, usage_source,provider,model,key_id,credits,usage_state,error_scope,route_reason,
       provider_region,error_code,upstream_reset_at,provider_plan,error_phase,error_source
FROM request_attempts WHERE request_id = ? ORDER BY attempt_index ASC, started_at ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list request attempts: %w", err)
	}
	defer rows.Close()
	items := make([]accounts.RequestAttempt, 0)
	for rows.Next() {
		item, err := scanRequestAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan request attempt: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClearRequestLogs(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM request_logs`)
	if err != nil {
		return 0, fmt.Errorf("clear request logs: %w", err)
	}
	changed, _ := result.RowsAffected()
	return changed, nil
}

func (s *Store) PurgeRequestLogs(ctx context.Context, olderThan time.Duration, maxRows int) (int64, error) {
	if olderThan <= 0 {
		olderThan = 7 * 24 * time.Hour
	}
	if maxRows <= 0 {
		maxRows = 20_000
	}
	cutoff := formatTime(time.Now().UTC().Add(-olderThan))
	result, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge old request logs: %w", err)
	}
	deleted, _ := result.RowsAffected()

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&total); err != nil {
		return deleted, fmt.Errorf("count request logs for cap: %w", err)
	}
	if total <= maxRows {
		return deleted, nil
	}
	overflow := total - maxRows
	result, err = s.db.ExecContext(ctx, `
DELETE FROM request_logs WHERE id IN (
  SELECT id FROM request_logs ORDER BY created_at ASC, id ASC LIMIT ?
)`, overflow)
	if err != nil {
		return deleted, fmt.Errorf("purge excess request logs: %w", err)
	}
	extra, _ := result.RowsAffected()
	return deleted + extra, nil
}

func buildRequestLogWhere(filter accounts.RequestLogFilter) (string, []any) {
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if account := strings.TrimSpace(filter.AccountID); account != "" {
		clauses = append(clauses, "account_id = ?")
		args = append(args, account)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if filter.Stream != nil {
		clauses = append(clauses, "stream = ?")
		args = append(args, boolToInt(*filter.Stream))
	}
	if kind := strings.TrimSpace(filter.ErrorKind); kind != "" {
		clauses = append(clauses, "error_kind = ?")
		args = append(args, kind)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, "(requested_model = ? OR mapped_model = ?)")
		args = append(args, model, model)
	}
	if id := strings.TrimSpace(filter.ID); id != "" {
		clauses = append(clauses, "id = ?")
		args = append(args, id)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		clauses = append(clauses, "id LIKE ?")
		args = append(args, "%"+query+"%")
	}
	if filter.From != nil && !filter.From.IsZero() {
		clauses = append(clauses, "substr(created_at, 1, 19) >= ?")
		args = append(args, filter.From.UTC().Format("2006-01-02T15:04:05"))
	}
	if filter.To != nil && !filter.To.IsZero() {
		clauses = append(clauses, "substr(created_at, 1, 19) <= ?")
		args = append(args, filter.To.UTC().Format("2006-01-02T15:04:05"))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func encodeIntSlice(values []int) string {
	if len(values) == 0 {
		return ""
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func encodeStringSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func decodeIntSlice(raw string) []int {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []int
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	return values
}

func decodeStringSlice(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	return values
}

func scanRequestLog(row rowScanner) (accounts.RequestLog, error) {
	var (
		log                         accounts.RequestLog
		finished, accountID         sql.NullString
		stream                      int
		prompt, completion          sql.NullInt64
		cacheRead, cacheWrite       sql.NullInt64
		outputLimit, promptStripped sql.NullInt64
		credits                     sql.NullFloat64
		latency, ttfb               sql.NullInt64
		emptyIndexes, messageRoles  sql.NullString
		created                     string
		shapeKnown                  int
	)
	err := row.Scan(
		&log.ID, &created, &finished, &stream, &log.Status, &log.RequestedModel, &log.MappedModel, &log.RequestedReasoning, &log.ResolvedReasoning, &accountID, &log.Provider, &log.Routing,
		&prompt, &completion, &cacheRead, &cacheWrite, &log.UsageSource, &credits,
		&latency, &ttfb, &log.ErrorKind, &log.ErrorCode, &log.ErrorMessage, &log.AttemptCount, &log.MessageCount, &emptyIndexes, &messageRoles,
		&log.ClientRequestID, &outputLimit, &log.OutputLimitSource, &promptStripped, &shapeKnown,
		&log.SourceFingerprint, &log.FinishReason, &log.FinishReasonSource, &log.RequestPath, &log.OutputBudgetEnforcement,
	)
	if err != nil {
		return accounts.RequestLog{}, err
	}
	log.CreatedAt = parseTime(created)
	log.Stream = stream != 0
	if finished.Valid && finished.String != "" {
		parsed := parseTime(finished.String)
		log.FinishedAt = &parsed
	}
	if accountID.Valid {
		log.AccountID = accountID.String
	}
	log.EmptyMessageIndexes = decodeIntSlice(emptyIndexes.String)
	log.MessageRoles = decodeStringSlice(messageRoles.String)
	log.PromptTokens = nullIntPtr(prompt)
	log.CompletionTokens = nullIntPtr(completion)
	log.CacheReadTokens = nullIntPtr(cacheRead)
	log.CacheWriteTokens = nullIntPtr(cacheWrite)
	log.OutputLimit = nullIntPtr(outputLimit)
	if promptStripped.Valid {
		value := promptStripped.Int64 != 0
		log.PromptStripped = &value
	}
	log.MessageShapeKnown = shapeKnown != 0
	log.LatencyMs = nullIntPtr(latency)
	log.TTFBMs = nullIntPtr(ttfb)
	if credits.Valid {
		value := credits.Float64
		log.Credits = &value
	}
	return log, nil
}

func scanRequestAttempt(row rowScanner) (accounts.RequestAttempt, error) {
	var (
		attempt                     accounts.RequestAttempt
		credits                     sql.NullFloat64
		finished                    sql.NullString
		upstreamResetAt             sql.NullString
		httpStatus                  sql.NullInt64
		latency, prompt, completion sql.NullInt64
		started                     string
	)
	err := row.Scan(
		&attempt.ID, &attempt.RequestID, &attempt.AttemptIndex, &attempt.AccountID, &started, &finished,
		&attempt.Status, &httpStatus, &attempt.ErrorKind, &attempt.ErrorMessage, &latency, &prompt, &completion, &attempt.UsageSource,
		&attempt.Provider, &attempt.Model, &attempt.KeyID, &credits, &attempt.UsageState, &attempt.ErrorScope, &attempt.RouteReason,
		&attempt.Region, &attempt.ErrorCode, &upstreamResetAt, &attempt.ProviderPlan, &attempt.ErrorPhase, &attempt.ErrorSource,
	)
	if err != nil {
		return accounts.RequestAttempt{}, err
	}
	if credits.Valid {
		attempt.Credits = &credits.Float64
	}
	attempt.StartedAt = parseTime(started)
	if finished.Valid && finished.String != "" {
		parsed := parseTime(finished.String)
		attempt.FinishedAt = &parsed
	}
	if upstreamResetAt.Valid && upstreamResetAt.String != "" {
		parsed := parseTime(upstreamResetAt.String)
		attempt.UpstreamResetAt = &parsed
	}
	attempt.HTTPStatus = nullIntPtr(httpStatus)
	attempt.LatencyMs = nullIntPtr(latency)
	attempt.PromptTokens = nullIntPtr(prompt)
	attempt.CompletionTokens = nullIntPtr(completion)
	return attempt, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return boolToInt(*value)
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimePtr(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatTime(value.UTC())
}

func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}
