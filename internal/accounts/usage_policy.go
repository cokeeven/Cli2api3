package accounts

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

type PlatformRoutePolicy struct {
	Provider    string `json:"provider"`
	Region      string `json:"region"`
	Priority    int    `json:"priority"`
	MaxInFlight int    `json:"max_inflight"`
}

type KeyRoutingPolicy struct {
	Platforms             []PlatformRoutePolicy `json:"platforms"`
	CrossProviderFailover bool                  `json:"cross_provider_failover"`
}

func NormalizeKeyRoutingPolicy(p KeyRoutingPolicy) (KeyRoutingPolicy, error) {
	seen := map[string]bool{}
	for i := range p.Platforms {
		r := &p.Platforms[i]
		r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
		r.Region = strings.ToLower(strings.TrimSpace(r.Region))
		if r.Region == "" {
			return p, fmt.Errorf("platform region required")
		}
		if _, err := ParseProviderGrant(r.Provider + ":" + r.Region); err != nil {
			return p, err
		}
		if r.MaxInFlight < 0 || r.MaxInFlight > 10000 || r.Priority < 0 || r.Priority > 100000 {
			return p, fmt.Errorf("invalid platform priority or max_inflight")
		}
		key := r.Provider + ":" + r.Region
		if seen[key] {
			return p, fmt.Errorf("duplicate platform %s", key)
		}
		seen[key] = true
	}
	if p.Platforms == nil {
		p.Platforms = []PlatformRoutePolicy{}
	}
	return p, nil
}

// Zero limits disable the corresponding guard. Limits are shared by all keys.
type UsagePolicy struct {
	ReserveCredits   float64          `json:"reserve_credits"`
	DailyTokens      int64            `json:"daily_tokens"`
	DailyCredits     float64          `json:"daily_credits"`
	ModelDailyTokens map[string]int64 `json:"model_daily_tokens"`
	PreferExpiring   bool             `json:"prefer_expiring"`
}

func NormalizeUsagePolicy(p UsagePolicy) (UsagePolicy, error) {
	if math.IsNaN(p.ReserveCredits) || math.IsInf(p.ReserveCredits, 0) || math.IsNaN(p.DailyCredits) || math.IsInf(p.DailyCredits, 0) || p.ReserveCredits < 0 || p.DailyCredits < 0 || p.DailyTokens < 0 {
		return p, fmt.Errorf("usage limits must be finite and non-negative")
	}
	m := map[string]int64{}
	for model, limit := range p.ModelDailyTokens {
		model = strings.TrimSpace(model)
		if model == "" || limit < 0 {
			return p, fmt.Errorf("model token limit requires a model and non-negative limit")
		}
		canonical, err := NormalizeUsageModelID(model)
		if err != nil {
			return p, err
		}
		if _, exists := m[canonical]; exists {
			return p, fmt.Errorf("duplicate canonical model %s", canonical)
		}
		m[canonical] = limit
	}
	p.ModelDailyTokens = m
	return p, nil
}

// NormalizeUsageModelID accepts a public provider/model ID but stores only
// the canonical model, because account policies already identify the platform.
func NormalizeUsageModelID(model string) (string, error) {
	model = strings.TrimSpace(model)
	if prefix, tail, ok := strings.Cut(model, "/"); ok {
		if _, err := ParseProviderGrant(strings.ToLower(prefix)); err != nil {
			return "", fmt.Errorf("invalid model provider prefix: %w", err)
		}
		if strings.TrimSpace(tail) == "" || strings.Contains(tail, "/") {
			return "", fmt.Errorf("invalid model id")
		}
		model = tail
	}
	model = NormalizeModelName(model)
	if model == "" || model == "auto" {
		return "", fmt.Errorf("a concrete canonical model id is required")
	}
	return model, nil
}

type UsageReservation struct {
	ID, RequestID, KeyID, AccountID, Model string
	EstimatedTokens                        int64
	CreditUpperBound                       *float64
	FreeModel                              bool
	At                                     time.Time
}
type UsageSettlement struct {
	ID            string
	ActualTokens  *int64
	ActualCredits *float64
	NonBillable   bool
	UsageSource   string
	At            time.Time
}
type UsageBudgetStore interface {
	ReserveUsage(context.Context, UsageReservation) error
	MarkUsageDispatched(context.Context, string) error
	SettleUsage(context.Context, UsageSettlement) error
	RecoverUsageReservations(context.Context) error
}
type UsagePolicyStore interface {
	GetUsagePolicy(context.Context, string) (UsagePolicy, error)
	SetUsagePolicy(context.Context, string, UsagePolicy) error
	GetUsageAnalysis(context.Context, string, int) (UsageAnalysis, error)
	ReconcileUsageBalance(context.Context, string, string) (int64, error)
}
type UsageGuardError struct {
	Reason  string
	ResetAt time.Time
}

func (e *UsageGuardError) Error() string { return "usage guard: " + e.Reason }

type UsageDay struct {
	Day               string  `json:"day"`
	TokensActual      int64   `json:"tokens_actual"`
	TokensReserved    int64   `json:"tokens_reserved"`
	CreditsActual     float64 `json:"credits_actual"`
	CreditsReserved   float64 `json:"credits_reserved"`
	UnknownAttempts   int     `json:"unknown_attempts"`
	EstimatedAttempts int     `json:"estimated_attempts"`
	Attempts          int     `json:"attempts"`
}
type QuotaAnalysis struct {
	Unit          string         `json:"unit"`
	Known         bool           `json:"known"`
	Available     float64        `json:"available"`
	Expiring7d    float64        `json:"expiring_7d"`
	Expiring30d   float64        `json:"expiring_30d"`
	Expired       float64        `json:"expired"`
	UnknownExpiry float64        `json:"unknown_expiry"`
	FetchedAt     string         `json:"fetched_at"`
	Packages      []QuotaPackage `json:"packages"`
}
type UsageAnalysis struct {
	QuotaHistory           []QuotaHistoryPoint `json:"quota_history"`
	GuardStates            []UsageGuardState   `json:"guard_states"`
	CreditBalancePending   float64             `json:"credit_balance_pending"`
	CreditBalanceUnbounded int                 `json:"credit_balance_unbounded"`
	AccountID              string              `json:"account_id"`
	Policy                 UsagePolicy         `json:"policy"`
	Day                    string              `json:"day"`
	Today                  UsageDay            `json:"today"`
	Quota                  QuotaAnalysis       `json:"quota"`
	Series                 []UsageDay          `json:"series"`
	RecordingSince         string              `json:"recording_since"`
}

type QuotaHistoryPoint struct {
	FetchedAt   string  `json:"fetched_at"`
	Available   float64 `json:"available"`
	Expiring7d  float64 `json:"expiring_7d"`
	Expiring30d float64 `json:"expiring_30d"`
	Unit        string  `json:"unit"`
	Known       bool    `json:"known"`
}
type UsageGuardState struct {
	Reason  string `json:"reason"`
	Scope   string `json:"scope"`
	Model   string `json:"model,omitempty"`
	ResetAt string `json:"reset_at,omitempty"`
}

var ChinaTime = time.FixedZone("CST", 8*60*60)

func UsageDayKey(at time.Time) string { return at.In(ChinaTime).Format("2006-01-02") }
