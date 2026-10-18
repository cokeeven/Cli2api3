package accounts

import (
	"context"
	"time"
)

// ModelUsageQuery bounds a read-only aggregation over gateway request attempts.
type ModelUsageQuery struct {
	Provider string
	Region   string
	Model    string
	Account  string
	From     time.Time
	To       time.Time
	Limit    int
}

type ModelUsageRow struct {
	Provider               string     `json:"provider"`
	Region                 string     `json:"region"`
	AccountID              string     `json:"account_id"`
	Model                  string     `json:"model"`
	Requests               int        `json:"requests"`
	Attempts               int        `json:"attempts"`
	SucceededAttempts      int        `json:"succeeded_attempts"`
	FailedAttempts         int        `json:"failed_attempts"`
	CanceledAttempts       int        `json:"canceled_attempts"`
	FailoverAttempts       int        `json:"failover_attempts"`
	LengthRequests         int        `json:"length_requests"`
	PromptTokens           *int64     `json:"prompt_tokens"`
	CompletionTokens       *int64     `json:"completion_tokens"`
	KnownUsageAttempts     int        `json:"known_usage_attempts"`
	EstimatedUsageAttempts int        `json:"estimated_usage_attempts"`
	MissingUsageAttempts   int        `json:"missing_usage_attempts"`
	LastSuccessAt          *time.Time `json:"last_success_at,omitempty"`
	CacheReadTokens        *int64     `json:"cache_read_tokens"`
	CacheWriteTokens       *int64     `json:"cache_write_tokens"`
	CacheKnownRequests     int        `json:"cache_known_requests"`
	CacheMissingRequests   int        `json:"cache_missing_requests"`
}

type ModelUsageError struct {
	At                 time.Time  `json:"at"`
	RequestID          string     `json:"request_id"`
	AttemptID          string     `json:"attempt_id"`
	Provider           string     `json:"provider"`
	Region             string     `json:"region"`
	AccountID          string     `json:"account_id"`
	Model              string     `json:"model"`
	Status             string     `json:"status"`
	Kind               string     `json:"kind,omitempty"`
	Code               string     `json:"code,omitempty"`
	Scope              string     `json:"scope,omitempty"`
	FinishReason       string     `json:"finish_reason,omitempty"`
	FinishReasonSource string     `json:"finish_reason_source,omitempty"`
	UpstreamResetAt    *time.Time `json:"upstream_reset_at,omitempty"`
}

type ModelUsageData struct {
	Rows                        []ModelUsageRow          `json:"rows"`
	Recent                      []ModelUsageError        `json:"recent"`
	BudgetObservations          []ModelBudgetObservation `json:"budget_observations"`
	BudgetObservationsTruncated bool                     `json:"budget_observations_truncated"`
	From                        time.Time                `json:"from"`
	To                          time.Time                `json:"to"`
	BudgetObservationFrom       time.Time                `json:"budget_observation_from"`
	Truncated                   bool                     `json:"truncated"`
}

type WorkBuddyBudgetEstimate struct {
	AccountID             string                    `json:"account_id"`
	Region                string                    `json:"region"`
	Model                 string                    `json:"model"`
	Plan                  string                    `json:"plan,omitempty"`
	RequestPath           string                    `json:"request_path,omitempty"`
	SampleCount           int                       `json:"sample_count"`
	CompleteWindows       int                       `json:"complete_windows"`
	SampleMinTokens       *int64                    `json:"sample_min_tokens"`
	SampleMaxTokens       *int64                    `json:"sample_max_tokens"`
	SampleFrom            *time.Time                `json:"sample_from,omitempty"`
	SampleTo              *time.Time                `json:"sample_to,omitempty"`
	StatisticalDays       int                       `json:"statistical_days"`
	StatisticalTimezone   string                    `json:"statistical_timezone"`
	WeightRule            string                    `json:"weight_rule"`
	SampleFreshness       string                    `json:"sample_freshness"`
	EstimatedTokens       *int64                    `json:"estimated_tokens"`
	ObservedSinceReset    *int64                    `json:"observed_since_reset"`
	EstimatedRemaining    *int64                    `json:"estimated_remaining"`
	ResetAt               *time.Time                `json:"reset_at,omitempty"`
	RestrictionObservedAt *time.Time                `json:"restriction_observed_at,omitempty"`
	Restricted            bool                      `json:"restricted"`
	RestrictionSource     string                    `json:"restriction_source,omitempty"`
	Cooling               bool                      `json:"cooling"`
	CoverageComplete      bool                      `json:"coverage_complete"`
	CoverageState         string                    `json:"coverage_state"`
	MissingAttempts       int                       `json:"missing_attempts"`
	ObservationTruncated  bool                      `json:"observation_truncated"`
	Basis                 string                    `json:"basis"`
	Reference             *WorkBuddyBudgetReference `json:"reference,omitempty"`
}

// WorkBuddyBudgetReference is a historical comparison across other accounts
// with an explicit matching plan and the same known public API path.
type WorkBuddyBudgetReference struct {
	EstimatedTokens *int64     `json:"estimated_tokens"`
	SampleCount     int        `json:"sample_count"`
	AccountCount    int        `json:"account_count"`
	SampleMinTokens *int64     `json:"sample_min_tokens"`
	SampleMaxTokens *int64     `json:"sample_max_tokens"`
	SampleFrom      *time.Time `json:"sample_from,omitempty"`
	SampleTo        *time.Time `json:"sample_to,omitempty"`
	Basis           string     `json:"basis"`
}

// ModelBudgetObservation is a compact, sanitized attempt row used to learn
// WorkBuddy windows from explicit upstream limit events and known usage.
type ModelBudgetObservation struct {
	At               time.Time  `json:"at"`
	AccountID        string     `json:"account_id"`
	Region           string     `json:"region"`
	Model            string     `json:"model"`
	Plan             string     `json:"plan,omitempty"`
	RequestPath      string     `json:"request_path,omitempty"`
	Status           string     `json:"status"`
	Kind             string     `json:"kind,omitempty"`
	Code             string     `json:"code,omitempty"`
	Scope            string     `json:"scope,omitempty"`
	UpstreamResetAt  *time.Time `json:"upstream_reset_at,omitempty"`
	UsageState       string     `json:"usage_state"`
	PromptTokens     *int64     `json:"prompt_tokens"`
	CompletionTokens *int64     `json:"completion_tokens"`
}

type ModelUsageStore interface {
	ReadModelUsage(context.Context, ModelUsageQuery) (ModelUsageData, error)
}
