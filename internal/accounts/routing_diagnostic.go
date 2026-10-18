package accounts

import "time"

// RequestRoutingDiagnostic contains scheduling facts, never request/response
// content or credentials. Missing historical diagnostics remain unknown.
type RequestRoutingDiagnostic struct {
	RequestID             string                   `json:"request_id"`
	InitialCandidates     int                      `json:"initial_candidates"`
	AttemptBudget         int                      `json:"attempt_budget"`
	Attempted             int                      `json:"attempted"`
	StopReason            string                   `json:"stop_reason"`
	FinalSelectionKind    string                   `json:"final_selection_kind,omitempty"`
	FinalSelectionCode    string                   `json:"final_selection_code,omitempty"`
	LastUpstreamKind      string                   `json:"last_upstream_kind,omitempty"`
	LastUpstreamCode      string                   `json:"last_upstream_code,omitempty"`
	LastUpstreamScope     string                   `json:"last_upstream_scope,omitempty"`
	LastAccountID         string                   `json:"last_account_id,omitempty"`
	CapacityQueueStatus   string                   `json:"capacity_queue_status,omitempty"`
	CapacityQueueWaitMs   int64                    `json:"capacity_queue_wait_ms,omitempty"`
	CapacityQueuePosition int                      `json:"capacity_queue_position,omitempty"`
	Accounts              []RouteAccountDiagnostic `json:"accounts"`
	AccountsComplete      bool                     `json:"accounts_complete"`
}

type RouteAccountDiagnostic struct {
	AccountID    string     `json:"account_id"`
	Provider     string     `json:"provider"`
	Region       string     `json:"region"`
	Reason       string     `json:"reason"`
	CatalogKnown bool       `json:"catalog_known"`
	CatalogAt    *time.Time `json:"catalog_at,omitempty"`
	QuotaKnown   bool       `json:"quota_known"`
	QuotaAt      *time.Time `json:"quota_at,omitempty"`
	FailureKind  string     `json:"failure_kind,omitempty"`
	FailureCode  string     `json:"failure_code,omitempty"`
	FailureScope string     `json:"failure_scope,omitempty"`
}
