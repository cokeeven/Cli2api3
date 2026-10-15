package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func (s *Store) GetUsagePolicy(ctx context.Context, id string) (accounts.UsagePolicy, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return accounts.UsagePolicy{}, err
	}
	return readUsagePolicy(ctx, s.db, id)
}

type usageQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readUsagePolicy(ctx context.Context, q usageQuerier, id string) (accounts.UsagePolicy, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT policy_json FROM account_usage_policies WHERE account_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.NormalizeUsagePolicy(accounts.UsagePolicy{})
	}
	if err != nil {
		return accounts.UsagePolicy{}, err
	}
	var p accounts.UsagePolicy
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	return accounts.NormalizeUsagePolicy(p)
}
func (s *Store) SetUsagePolicy(ctx context.Context, id string, p accounts.UsagePolicy) error {
	p, err := accounts.NormalizeUsagePolicy(p)
	if err != nil {
		return err
	}
	if _, err = s.Get(ctx, id); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	_, err = s.db.ExecContext(ctx, `INSERT INTO account_usage_policies(account_id,policy_json,updated_at) VALUES(?,?,?) ON CONFLICT(account_id) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, id, string(b), formatTime(time.Now()))
	return err
}

func (s *Store) ReserveUsage(ctx context.Context, r accounts.UsageReservation) error {
	if strings.TrimSpace(r.ID) == "" || r.AccountID == "" {
		return fmt.Errorf("reservation id and account required")
	}
	if r.EstimatedTokens < 0 {
		r.EstimatedTokens = 0
	}
	if r.CreditUpperBound != nil && (math.IsNaN(*r.CreditUpperBound) || math.IsInf(*r.CreditUpperBound, 0) || *r.CreditUpperBound < 0) {
		return fmt.Errorf("invalid credit upper bound")
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	r.Model = accounts.CanonicalModelID(r.Model)
	day := accounts.UsageDayKey(r.At)
	if r.FreeModel {
		v := 0.0
		r.CreditUpperBound = &v
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingAccount, existingModel string
	err = tx.QueryRowContext(ctx, `SELECT account_id,model FROM usage_reservations WHERE id=?`, r.ID).Scan(&existingAccount, &existingModel)
	if err == nil {
		if existingAccount != r.AccountID || existingModel != r.Model {
			return fmt.Errorf("reservation id reused")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var rawQuota string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(quota_json,'') FROM accounts WHERE id=?`, r.AccountID).Scan(&rawQuota); errors.Is(err, sql.ErrNoRows) {
		return accounts.ErrAccountNotFound
	} else if err != nil {
		return err
	}
	p, err := readUsagePolicy(ctx, tx, r.AccountID)
	if err != nil {
		return err
	}
	reset := r.At.In(accounts.ChinaTime)
	reset = time.Date(reset.Year(), reset.Month(), reset.Day()+1, 0, 0, 0, 0, accounts.ChinaTime)
	deny := func(reason string) error {
		until := time.Time{}
		if reason == "daily_tokens" || reason == "model_daily_tokens" || reason == "daily_credits" {
			until = reset
		}
		return &accounts.UsageGuardError{Reason: reason, ResetAt: until}
	}
	var tokens, modelTokens int64
	var credits float64
	var unknownTokens, unknownCredits int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(actual_tokens,estimated_tokens)),0),COALESCE(SUM(COALESCE(actual_credits,credit_upper_bound,0)),0),COALESCE(SUM(CASE WHEN model=? THEN COALESCE(actual_tokens,estimated_tokens) ELSE 0 END),0),COALESCE(SUM(CASE WHEN actual_tokens IS NULL AND estimated_tokens=0 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN actual_credits IS NULL AND credit_upper_bound IS NULL THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND day=? AND state!='abandoned'`, r.Model, r.AccountID, day).Scan(&tokens, &credits, &modelTokens, &unknownTokens, &unknownCredits)
	if err != nil {
		return err
	}
	if p.DailyTokens > 0 && (r.EstimatedTokens == 0 || unknownTokens > 0 || tokens > p.DailyTokens || r.EstimatedTokens > p.DailyTokens-tokens) {
		return deny("daily_tokens")
	}
	if limit := p.ModelDailyTokens[r.Model]; limit > 0 {
		var unknownModel int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_reservations WHERE account_id=? AND day=? AND model=? AND actual_tokens IS NULL AND estimated_tokens=0 AND state!='abandoned'`, r.AccountID, day, r.Model).Scan(&unknownModel); err != nil {
			return err
		}
		if r.EstimatedTokens == 0 || unknownModel > 0 || modelTokens > limit || r.EstimatedTokens > limit-modelTokens {
			return deny("model_daily_tokens")
		}
	}
	if !r.FreeModel && (p.DailyCredits > 0 || p.ReserveCredits > 0) && r.CreditUpperBound == nil {
		return deny("credit_upper_bound_unknown")
	}
	if !r.FreeModel && p.DailyCredits > 0 && (unknownCredits > 0 || credits+*r.CreditUpperBound > p.DailyCredits) {
		return deny("daily_credits")
	}
	if !r.FreeModel && p.ReserveCredits > 0 {
		var q accounts.QuotaSnapshot
		if json.Unmarshal([]byte(rawQuota), &q) != nil || q.FetchedAt == "" || !strings.EqualFold(q.Unit, "credits") {
			return deny("credit_balance_unknown")
		}
		fetched, err := time.Parse(time.RFC3339Nano, q.FetchedAt)
		if err != nil || r.At.Sub(fetched) > 15*time.Minute || fetched.After(r.At.Add(time.Minute)) {
			return deny("credit_balance_stale")
		}
		var liability float64
		var unbounded int
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN actual_credits IS NULL THEN COALESCE(credit_upper_bound,0) ELSE actual_credits END),0),COALESCE(SUM(CASE WHEN actual_credits IS NULL AND credit_upper_bound IS NULL THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND state!='abandoned' AND covered_by_snapshot='' AND (actual_credits IS NULL OR julianday(finished_at)>julianday(?))`, r.AccountID, formatTime(fetched)).Scan(&liability, &unbounded)
		if err != nil {
			return err
		}
		available := analyzeQuota(&q, r.At).Available
		if unbounded > 0 || available-liability-*r.CreditUpperBound < p.ReserveCredits {
			return deny("reserve_credits")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_reservations(id,request_id,key_id,account_id,model,day,state,estimated_tokens,credit_upper_bound,created_at) VALUES(?,?,?,?,?,?,'reserved',?,?,?)`, r.ID, r.RequestID, r.KeyID, r.AccountID, r.Model, day, r.EstimatedTokens, r.CreditUpperBound, formatTime(r.At))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkUsageDispatched(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE usage_reservations SET state='dispatched' WHERE id=? AND state='reserved'`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("usage reservation is not dispatchable")
	}
	return nil
}
func (s *Store) SettleUsage(ctx context.Context, u accounts.UsageSettlement) error {
	if u.At.IsZero() {
		u.At = time.Now()
	}
	if u.ActualTokens != nil && *u.ActualTokens < 0 {
		return fmt.Errorf("negative actual tokens")
	}
	if u.ActualCredits != nil && (math.IsNaN(*u.ActualCredits) || math.IsInf(*u.ActualCredits, 0) || *u.ActualCredits < 0) {
		return fmt.Errorf("invalid actual credits")
	}
	if !u.NonBillable && u.ActualCredits == nil {
		var bound sql.NullFloat64
		if err := s.db.QueryRowContext(ctx, `SELECT credit_upper_bound FROM usage_reservations WHERE id=?`, u.ID).Scan(&bound); err != nil {
			return err
		}
		// A trusted zero upper bound proves no credits can be charged (for
		// example an explicitly free catalog model). A paid estimate does not.
		if bound.Valid && bound.Float64 == 0 {
			zero := 0.0
			u.ActualCredits = &zero
		}
	}
	state := "settled"
	if u.NonBillable {
		tokens := int64(0)
		credits := 0.0
		u.ActualTokens = &tokens
		u.ActualCredits = &credits
		state = "abandoned"
	} else if u.ActualTokens == nil || u.ActualCredits == nil {
		state = "unknown"
	}
	// Estimates remain liabilities; they never become reported actual usage.
	if !u.NonBillable && strings.Contains(strings.ToLower(u.UsageSource), "estimat") {
		u.ActualTokens = nil
		state = "unknown"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE usage_reservations SET state=?,actual_tokens=?,actual_credits=?,usage_source=?,finished_at=? WHERE id=? AND state IN ('reserved','dispatched')`, state, u.ActualTokens, u.ActualCredits, u.UsageSource, formatTime(u.At), u.ID)
	return err
}
func (s *Store) RecoverUsageReservations(ctx context.Context) error {
	// Reserved attempts never reached dispatch. Dispatched attempts may have
	// consumed upstream resources and keep their conservative liabilities.
	_, err := s.db.ExecContext(ctx, `UPDATE usage_reservations SET state=CASE WHEN state='reserved' THEN 'abandoned' ELSE 'unknown' END, actual_tokens=CASE WHEN state='reserved' THEN 0 ELSE actual_tokens END, actual_credits=CASE WHEN state='reserved' THEN 0 ELSE actual_credits END,finished_at=? WHERE state IN ('reserved','dispatched')`, formatTime(time.Now()))
	return err
}

// ReconcileUsageBalance records an operator acknowledgement that a fresh
// balance snapshot already includes ended, uncertain attempts. It neither
// fabricates request costs nor releases the conservative daily budget.
func (s *Store) ReconcileUsageBalance(ctx context.Context, id, snapshot string) (int64, error) {
	when, err := time.Parse(time.RFC3339Nano, snapshot)
	if err != nil || time.Since(when) > 15*time.Minute || when.After(time.Now().Add(time.Minute)) {
		return 0, fmt.Errorf("a fresh quota snapshot is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(quota_json,'') FROM accounts WHERE id=?`, id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return 0, accounts.ErrAccountNotFound
	} else if err != nil {
		return 0, err
	}
	var q accounts.QuotaSnapshot
	if json.Unmarshal([]byte(raw), &q) != nil || q.FetchedAt != snapshot || !strings.EqualFold(q.Unit, "credits") {
		return 0, fmt.Errorf("quota snapshot changed or credit balance is unknown; refresh and review again")
	}
	result, err := tx.ExecContext(ctx, `UPDATE usage_reservations SET covered_by_snapshot=? WHERE account_id=? AND state='unknown' AND actual_credits IS NULL AND finished_at!='' AND julianday(finished_at)<=julianday(?) AND covered_by_snapshot=''`, snapshot, id, snapshot)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func analyzeQuota(q *accounts.QuotaSnapshot, now time.Time) accounts.QuotaAnalysis {
	out := accounts.QuotaAnalysis{Packages: []accounts.QuotaPackage{}}
	if q == nil {
		return out
	}
	out.Unit = q.Unit
	out.FetchedAt = q.FetchedAt
	out.Known = q.FetchedAt != ""
	if len(q.Packages) == 0 {
		out.Available = math.Max(0, q.Remaining)
		out.UnknownExpiry = out.Available
		return out
	}
	for _, p := range q.Packages {
		p.Status = strings.ToLower(strings.TrimSpace(p.Status))
		if p.Status == "inactive" || p.Status == "disabled" {
			out.Packages = append(out.Packages, p)
			continue
		}
		if p.Remain <= 0 || p.Status == "exhausted" {
			p.Status = "exhausted"
			out.Packages = append(out.Packages, p)
			continue
		}
		expires := p.EndsAt
		if p.ExpirySource == "cycle" {
			expires = 0
		}
		if expires == 0 && p.ExpirySource != "cycle" && p.EndTime != "" {
			if t, e := time.Parse(time.RFC3339Nano, p.EndTime); e == nil {
				expires = t.Unix()
			}
		}
		if p.Status == "expired" || p.Status == "exhausted" || (expires > 0 && expires <= now.Unix()) {
			p.Status = "expired"
			out.Expired += p.Remain
		} else {
			out.Available += p.Remain
			p.Status = "active"
			if expires == 0 {
				out.UnknownExpiry += p.Remain
			} else {
				if expires <= now.Add(7*24*time.Hour).Unix() {
					out.Expiring7d += p.Remain
				}
				if expires <= now.Add(30*24*time.Hour).Unix() {
					out.Expiring30d += p.Remain
				}
			}
		}
		out.Packages = append(out.Packages, p)
	}
	return out
}
func (s *Store) GetUsageAnalysis(ctx context.Context, id string, days int) (accounts.UsageAnalysis, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return accounts.UsageAnalysis{}, err
	}
	p, err := s.GetUsagePolicy(ctx, id)
	if err != nil {
		return accounts.UsageAnalysis{}, err
	}
	now := time.Now()
	out := accounts.UsageAnalysis{AccountID: id, Policy: p, Day: accounts.UsageDayKey(now), Quota: analyzeQuota(a.Quota, now), Series: []accounts.UsageDay{}, QuotaHistory: []accounts.QuotaHistoryPoint{}, GuardStates: []accounts.UsageGuardState{}}
	out.Today.Day = out.Day
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(credit_upper_bound),0),COALESCE(SUM(CASE WHEN credit_upper_bound IS NULL THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND actual_credits IS NULL AND covered_by_snapshot='' AND state!='abandoned'`, id).Scan(&out.CreditBalancePending, &out.CreditBalanceUnbounded); err != nil {
		return out, err
	}

	var since sql.NullString
	if err = s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM usage_reservations WHERE account_id=?`, id).Scan(&since); err != nil {
		return out, err
	}
	out.RecordingSince = since.String
	rows, err := s.db.QueryContext(ctx, `SELECT day,COALESCE(SUM(actual_tokens),0),COALESCE(SUM(CASE WHEN actual_tokens IS NULL THEN estimated_tokens ELSE 0 END),0),COALESCE(SUM(actual_credits),0),COALESCE(SUM(CASE WHEN actual_credits IS NULL THEN credit_upper_bound ELSE 0 END),0),SUM(CASE WHEN state='unknown' THEN 1 ELSE 0 END),SUM(CASE WHEN usage_source LIKE '%estimat%' THEN 1 ELSE 0 END),COUNT(*) FROM usage_reservations WHERE account_id=? AND day>=? GROUP BY day ORDER BY day`, id, accounts.UsageDayKey(now.AddDate(0, 0, -days+1)))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d accounts.UsageDay
		if err = rows.Scan(&d.Day, &d.TokensActual, &d.TokensReserved, &d.CreditsActual, &d.CreditsReserved, &d.UnknownAttempts, &d.EstimatedAttempts, &d.Attempts); err != nil {
			return out, err
		}
		out.Series = append(out.Series, d)
		if d.Day == out.Day {
			out.Today = d
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = s.fillUsageHistory(ctx, id, now, &out); err != nil {
		return out, err
	}
	if err = s.fillGuardStates(ctx, a, now, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) fillUsageHistory(ctx context.Context, id string, now time.Time, out *accounts.UsageAnalysis) error {
	rows, err := s.db.QueryContext(ctx, `SELECT fetched_at,snapshot_json FROM quota_history WHERE account_id=? AND julianday(fetched_at)>=julianday(?) ORDER BY id DESC LIMIT 200`, id, formatTime(now.AddDate(0, 0, -90)))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at, raw string
		if err = rows.Scan(&at, &raw); err != nil {
			return err
		}
		var q accounts.QuotaSnapshot
		if err = json.Unmarshal([]byte(raw), &q); err != nil {
			return err
		}
		captured := parseTime(at)
		if sourceAt, e := time.Parse(time.RFC3339Nano, q.FetchedAt); e == nil {
			captured = sourceAt
		}
		v := analyzeQuota(&q, captured)
		fetched := q.FetchedAt
		if fetched == "" {
			fetched = at
		}
		out.QuotaHistory = append(out.QuotaHistory, accounts.QuotaHistoryPoint{FetchedAt: fetched, Available: v.Available, Expiring7d: v.Expiring7d, Expiring30d: v.Expiring30d, Unit: v.Unit, Known: v.Known})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i, j := 0, len(out.QuotaHistory)-1; i < j; i, j = i+1, j-1 {
		out.QuotaHistory[i], out.QuotaHistory[j] = out.QuotaHistory[j], out.QuotaHistory[i]
	}
	return nil
}

func (s *Store) fillGuardStates(ctx context.Context, a accounts.Account, now time.Time, out *accounts.UsageAnalysis) error {
	p := out.Policy
	day := now.In(accounts.ChinaTime)
	reset := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, accounts.ChinaTime).Format(time.RFC3339)
	add := func(reason, scope, model, until string) {
		out.GuardStates = append(out.GuardStates, accounts.UsageGuardState{Reason: reason, Scope: scope, Model: model, ResetAt: until})
	}
	var tokenUnknown, creditUnknown int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN actual_tokens IS NULL AND estimated_tokens=0 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN actual_credits IS NULL AND credit_upper_bound IS NULL THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND day=? AND state!='abandoned'`, a.ID, out.Day).Scan(&tokenUnknown, &creditUnknown); err != nil {
		return err
	}
	if p.DailyTokens > 0 {
		if tokenUnknown > 0 {
			add("daily_tokens_unknown", "all_models", "", reset)
		} else if out.Today.TokensActual+out.Today.TokensReserved >= p.DailyTokens {
			add("daily_tokens", "all_models", "", reset)
		}
	}
	if p.DailyCredits > 0 {
		if creditUnknown > 0 {
			add("daily_credits_unknown", "paid_models", "", reset)
		} else if out.Today.CreditsActual+out.Today.CreditsReserved >= p.DailyCredits {
			add("daily_credits", "paid_models", "", reset)
		}
	}
	models := make([]string, 0, len(p.ModelDailyTokens))
	for model, limit := range p.ModelDailyTokens {
		if limit > 0 {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	for _, model := range models {
		var held int64
		var unknown int
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(actual_tokens,estimated_tokens)),0),COALESCE(SUM(CASE WHEN actual_tokens IS NULL AND estimated_tokens=0 THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND day=? AND model=? AND state!='abandoned'`, a.ID, out.Day, model).Scan(&held, &unknown); err != nil {
			return err
		}
		if unknown > 0 {
			add("model_daily_tokens_unknown", "model", model, reset)
		} else if held >= p.ModelDailyTokens[model] {
			add("model_daily_tokens", "model", model, reset)
		}
	}
	if a.Quota != nil && a.Quota.Exceeded {
		add("account_quota_exhausted", "paid_models", "", "")
	}
	if p.ReserveCredits <= 0 {
		return nil
	}
	if a.Quota == nil || !out.Quota.Known || !strings.EqualFold(out.Quota.Unit, "credits") {
		add("credit_balance_unknown", "paid_models", "", "")
		return nil
	}
	fetched, err := time.Parse(time.RFC3339Nano, a.Quota.FetchedAt)
	if err != nil || now.Sub(fetched) > 15*time.Minute || fetched.After(now.Add(time.Minute)) {
		add("credit_balance_stale", "paid_models", "", "")
		return nil
	}
	var liability float64
	var unbounded int
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(actual_credits,credit_upper_bound,0)),0),COALESCE(SUM(CASE WHEN actual_credits IS NULL AND credit_upper_bound IS NULL THEN 1 ELSE 0 END),0) FROM usage_reservations WHERE account_id=? AND state!='abandoned' AND covered_by_snapshot='' AND (actual_credits IS NULL OR julianday(finished_at)>julianday(?))`, a.ID, formatTime(fetched)).Scan(&liability, &unbounded); err != nil {
		return err
	}
	if unbounded > 0 {
		add("reserve_credits_unknown", "paid_models", "", "")
	} else if out.Quota.Available-liability <= p.ReserveCredits {
		add("reserve_credits", "paid_models", "", "")
	}
	return nil
}
