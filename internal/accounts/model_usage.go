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

// ModelUsageCatalogModel is a process-local catalog identity. It contains no
// account credentials and lets the read API show a model before it has usage.
type ModelUsageCatalogModel struct {
	Provider string
	Region   string
	Model    string
}

type ModelUsageOverviewQuery struct {
	Provider       string
	Region         string
	Search         string
	Activity       string
	From           time.Time
	To             time.Time
	Page           int
	PageSize       int
	CatalogModels  []ModelUsageCatalogModel
	CatalogLimited bool
}

type ModelUsageOverviewRow struct {
	Provider               string     `json:"provider"`
	Region                 string     `json:"region"`
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
	AccountCount           int        `json:"account_count"`
	RecentUsage            bool       `json:"recent_usage"`
	LastActivityAt         *time.Time `json:"last_activity_at,omitempty"`
	SchedulableAccounts    int        `json:"schedulable_accounts"`
	RestrictedAccounts     int        `json:"restricted_accounts"`
}

type ModelUsageOverviewSummary struct {
	Models                 int   `json:"models"`
	Requests               int   `json:"requests"`
	Attempts               int   `json:"attempts"`
	PromptTokens           int64 `json:"prompt_tokens"`
	CompletionTokens       int64 `json:"completion_tokens"`
	KnownUsageAttempts     int   `json:"known_usage_attempts"`
	EstimatedUsageAttempts int   `json:"estimated_usage_attempts"`
	MissingUsageAttempts   int   `json:"missing_usage_attempts"`
}

type ModelUsageOverviewData struct {
	Rows           []ModelUsageOverviewRow   `json:"rows"`
	Summary        ModelUsageOverviewSummary `json:"summary"`
	Total          int                       `json:"total"`
	Page           int                       `json:"page"`
	PageSize       int                       `json:"page_size"`
	HasMore        bool                      `json:"has_more"`
	From           time.Time                 `json:"from"`
	To             time.Time                 `json:"to"`
	CatalogLimited bool                      `json:"catalog_limited"`
}

type ModelUsageDetailQuery struct {
	Provider        string
	Region          string
	Model           string
	From            time.Time
	To              time.Time
	Page            int
	PageSize        int
	Sort            string
	CatalogAccounts []string
	CatalogLimited  bool
}

type ModelUsageAccountRow struct {
	AccountID              string     `json:"account_id"`
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

type ModelUsageDetailData struct {
	Rows                        []ModelUsageAccountRow   `json:"rows"`
	Total                       int                      `json:"total"`
	Page                        int                      `json:"page"`
	PageSize                    int                      `json:"page_size"`
	HasMore                     bool                     `json:"has_more"`
	From                        time.Time                `json:"from"`
	To                          time.Time                `json:"to"`
	BudgetObservationFrom       time.Time                `json:"budget_observation_from,omitempty"`
	BudgetObservations          []ModelBudgetObservation `json:"-"`
	BudgetObservationsTruncated bool                     `json:"budget_observations_truncated"`
	CatalogLimited              bool                     `json:"catalog_limited"`
}

type ModelUsageDiagnosticsQuery struct {
	Provider string
	Region   string
	Model    string
	Account  string
	From     time.Time
	To       time.Time
	Page     int
	PageSize int
}

type ModelUsageDiagnosticsData struct {
	Errors   []ModelUsageError `json:"errors"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	HasMore  bool              `json:"has_more"`
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
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
	AccountID                        string                           `json:"account_id"`
	Region                           string                           `json:"region"`
	Model                            string                           `json:"model"`
	Plan                             string                           `json:"plan,omitempty"`
	RequestPath                      string                           `json:"request_path,omitempty"`
	SampleCount                      int                              `json:"sample_count"`
	CompleteWindows                  int                              `json:"complete_windows"`
	SampleMinTokens                  *int64                           `json:"sample_min_tokens"`
	SampleMaxTokens                  *int64                           `json:"sample_max_tokens"`
	SampleFrom                       *time.Time                       `json:"sample_from,omitempty"`
	SampleTo                         *time.Time                       `json:"sample_to,omitempty"`
	StatisticalDays                  int                              `json:"statistical_days"`
	StatisticalTimezone              string                           `json:"statistical_timezone"`
	WeightRule                       string                           `json:"weight_rule"`
	SampleFreshness                  string                           `json:"sample_freshness"`
	EstimatedAt                      *time.Time                       `json:"estimated_at,omitempty"`
	EstimatedTokens                  *int64                           `json:"estimated_tokens"`
	ObservedSinceReset               *int64                           `json:"observed_since_reset"`
	EstimatedRemaining               *int64                           `json:"estimated_remaining"`
	Observed24hTokens                *int64                           `json:"observed_24h_tokens"`
	Estimated24hBudgetTokens         *int64                           `json:"estimated_24h_budget_tokens"`
	Observed24hCoverage              string                           `json:"observed_24h_coverage"`
	Observed24hMissing               int                              `json:"observed_24h_missing_attempts"`
	Observed24hFrom                  *time.Time                       `json:"observed_24h_from,omitempty"`
	Observed24hTo                    *time.Time                       `json:"observed_24h_to,omitempty"`
	Estimated24hRemaining            *int64                           `json:"estimated_24h_remaining"`
	Estimated24hBasis                string                           `json:"estimated_24h_basis,omitempty"`
	FirstLimit24hSample              *WorkBuddyBudgetSample           `json:"first_limit_24h_sample,omitempty"`
	ResetAt                          *time.Time                       `json:"reset_at,omitempty"`
	RestrictionObservedAt            *time.Time                       `json:"restriction_observed_at,omitempty"`
	Restricted                       bool                             `json:"restricted"`
	RestrictionSource                string                           `json:"restriction_source,omitempty"`
	Cooling                          bool                             `json:"cooling"`
	CoverageComplete                 bool                             `json:"coverage_complete"`
	CoverageState                    string                           `json:"coverage_state"`
	MissingAttempts                  int                              `json:"missing_attempts"`
	ObservationTruncated             bool                             `json:"observation_truncated"`
	Basis                            string                           `json:"basis"`
	Reference                        *WorkBuddyBudgetReference        `json:"reference,omitempty"`
	WeakReferences                   []WorkBuddyBudgetReference       `json:"weak_references,omitempty"`
	WeakReferencesTruncated          bool                             `json:"weak_references_truncated"`
	LowConfidenceReferences          []WorkBuddyBudgetSampleReference `json:"low_confidence_references,omitempty"`
	LowConfidenceReferencesTruncated bool                             `json:"low_confidence_references_truncated"`
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
	Plan            string     `json:"plan,omitempty"`
	RequestPath     string     `json:"request_path,omitempty"`
	Confidence      string     `json:"confidence"`
	Scope           string     `json:"scope"`
	Basis           string     `json:"basis"`
}

// WorkBuddyBudgetSample is an explicitly low-confidence observation, not a
// verified upstream window. It is kept separate from complete reset windows.
type WorkBuddyBudgetSample struct {
	Tokens          *int64     `json:"tokens"`
	From            *time.Time `json:"from,omitempty"`
	To              *time.Time `json:"to,omitempty"`
	MissingAttempts int        `json:"missing_attempts"`
	Coverage        string     `json:"coverage"`
	Confidence      string     `json:"confidence"`
	Source          string     `json:"source"`
}

// WorkBuddyBudgetSampleReference exposes a first-limit 24h observation as an
// individual low-confidence sample. It is never averaged into complete-window
// references or used as an automatic local limit.
type WorkBuddyBudgetSampleReference struct {
	AccountID   string                `json:"account_id"`
	Plan        string                `json:"plan,omitempty"`
	RequestPath string                `json:"request_path,omitempty"`
	Scope       string                `json:"scope"`
	Sample      WorkBuddyBudgetSample `json:"sample"`
}

// WorkBuddyBudgetTarget adds a visible catalog account/model pair without
// claiming that it has usage or an upstream budget observation.
type WorkBuddyBudgetTarget struct {
	AccountID   string
	Region      string
	Model       string
	Plan        string
	RequestPath string
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

// CompactModelUsageStore serves the paginated read path used by the console.
// The legacy endpoint continues to use ModelUsageStore for compatibility.
type CompactModelUsageStore interface {
	ReadModelUsageOverview(context.Context, ModelUsageOverviewQuery) (ModelUsageOverviewData, error)
	ReadModelUsageDetail(context.Context, ModelUsageDetailQuery) (ModelUsageDetailData, error)
	ReadModelUsageDiagnostics(context.Context, ModelUsageDiagnosticsQuery) (ModelUsageDiagnosticsData, error)
}
