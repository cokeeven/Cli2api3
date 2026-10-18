package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	RequestStatusStarted    = "started"
	RequestStatusStreaming  = "streaming"
	RequestStatusOK         = "ok"
	RequestStatusIncomplete = "incomplete"
	RequestStatusError      = "error"
	RequestStatusCanceled   = "canceled"

	AttemptStatusStarted  = "started"
	AttemptStatusOK       = "ok"
	AttemptStatusError    = "error"
	AttemptStatusCanceled = "canceled"
	AttemptStatusFailover = "failover"
)

var ErrRequestLogNotFound = errors.New("request log not found")

type RequestLog struct {
	ID                      string                    `json:"id"`
	CreatedAt               time.Time                 `json:"created_at"`
	FinishedAt              *time.Time                `json:"finished_at,omitempty"`
	Stream                  bool                      `json:"stream"`
	Status                  string                    `json:"status"`
	RequestedModel          string                    `json:"requested_model"`
	MappedModel             string                    `json:"mapped_model,omitempty"`
	RequestedReasoning      string                    `json:"requested_reasoning,omitempty"`
	ResolvedReasoning       string                    `json:"resolved_reasoning,omitempty"`
	AccountID               string                    `json:"account_id,omitempty"`
	Provider                string                    `json:"provider,omitempty"`
	Routing                 string                    `json:"routing,omitempty"`
	PromptTokens            *int                      `json:"prompt_tokens,omitempty"`
	CompletionTokens        *int                      `json:"completion_tokens,omitempty"`
	CacheReadTokens         *int                      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens        *int                      `json:"cache_write_tokens,omitempty"`
	UsageSource             string                    `json:"usage_source,omitempty"`
	Credits                 *float64                  `json:"credits,omitempty"`
	LatencyMs               *int                      `json:"latency_ms,omitempty"`
	TTFBMs                  *int                      `json:"ttfb_ms,omitempty"`
	ErrorKind               string                    `json:"error_kind,omitempty"`
	ErrorCode               string                    `json:"error_code,omitempty"`
	ErrorMessage            string                    `json:"error_message,omitempty"`
	FinishReason            string                    `json:"finish_reason,omitempty"`
	FinishReasonSource      string                    `json:"finish_reason_source,omitempty"`
	AttemptCount            int                       `json:"attempt_count"`
	ClientRequestID         string                    `json:"client_request_id,omitempty"`
	RequestPath             string                    `json:"request_path,omitempty"`
	SourceFingerprint       string                    `json:"source_fingerprint,omitempty"`
	OutputLimit             *int                      `json:"output_limit_tokens,omitempty"`
	OutputLimitSource       string                    `json:"output_limit_source,omitempty"`
	OutputBudgetEnforcement string                    `json:"output_budget_enforcement,omitempty"`
	PromptStripped          *bool                     `json:"prompt_stripped,omitempty"`
	MessageCount            int                       `json:"message_count,omitempty"`
	MessageShapeKnown       bool                      `json:"message_shape_known"`
	EmptyMessageIndexes     []int                     `json:"empty_message_indexes,omitempty"`
	MessageRoles            []string                  `json:"message_roles,omitempty"`
	Attempts                []RequestAttempt          `json:"attempts,omitempty"`
	StreamDiagnostic        *RequestStreamDiagnostic  `json:"stream_diagnostic,omitempty"`
	RoutingDiagnostic       *RequestRoutingDiagnostic `json:"routing_diagnostic,omitempty"`
	UsageDetail             *RequestUsageDetail       `json:"usage_detail,omitempty"`
}

type RequestUsageDetail struct {
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
	Provider  string    `json:"provider,omitempty"`
	Credit    *float64  `json:"credit,omitempty"`
	Unit      string    `json:"unit,omitempty"`
}

type RequestStreamDiagnostic struct {
	RequestID                  string     `json:"request_id"`
	CreatedAt                  time.Time  `json:"created_at"`
	FinishedAt                 *time.Time `json:"finished_at,omitempty"`
	UpstreamStatus             *int       `json:"upstream_status,omitempty"`
	UpstreamRequestID          string     `json:"upstream_request_id,omitempty"`
	ContextErr                 string     `json:"context_err,omitempty"`
	CancellationSource         string     `json:"cancellation_source,omitempty"`
	RelayError                 string     `json:"relay_error,omitempty"`
	SSEEventCount              int        `json:"sse_event_count"`
	BytesRead                  int64      `json:"bytes_read"`
	ContentLength              *int       `json:"content_length,omitempty"`
	LastEvent                  string     `json:"last_event,omitempty"`
	SawDone                    bool       `json:"saw_done"`
	UpstreamFinishReason       string     `json:"upstream_finish_reason,omitempty"`
	UpstreamFinishReasonSource string     `json:"upstream_finish_reason_source,omitempty"`
	ExternalFinishReason       string     `json:"external_finish_reason,omitempty"`
	ExternalFinishReasonSource string     `json:"external_finish_reason_source,omitempty"`
	ContentChars               *int       `json:"content_chars,omitempty"`
	ReasoningChars             *int       `json:"reasoning_chars,omitempty"`
	ToolEventCount             *int       `json:"tool_event_count,omitempty"`
	CharCountUnit              string     `json:"char_count_unit,omitempty"`
	CancellationStage          string     `json:"cancellation_stage,omitempty"`
	PrepareDurationMs          *int       `json:"prepare_duration_ms,omitempty"`
	UpstreamResponseDurationMs *int       `json:"upstream_response_duration_ms,omitempty"`
	FirstEventDurationMs       *int       `json:"first_event_duration_ms,omitempty"`
	FirstContentDurationMs     *int       `json:"first_content_duration_ms,omitempty"`
	TimeoutPhase               string     `json:"timeout_phase,omitempty"`
	TotalDurationMs            *int       `json:"total_duration_ms,omitempty"`
	OutputBudgetEnforcement    string     `json:"output_budget_enforcement,omitempty"`
}

type RequestAttempt struct {
	Provider         string     `json:"provider,omitempty"`
	Region           string     `json:"region,omitempty"`
	ProviderPlan     string     `json:"provider_plan,omitempty"`
	Model            string     `json:"model,omitempty"`
	KeyID            string     `json:"key_id,omitempty"`
	Credits          *float64   `json:"credits,omitempty"`
	UsageState       string     `json:"usage_state,omitempty"`
	ErrorScope       string     `json:"error_scope,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	UpstreamResetAt  *time.Time `json:"upstream_reset_at,omitempty"`
	RouteReason      string     `json:"route_reason,omitempty"`
	ID               string     `json:"id"`
	RequestID        string     `json:"request_id"`
	AttemptIndex     int        `json:"attempt_index"`
	AccountID        string     `json:"account_id,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	Status           string     `json:"status"`
	HTTPStatus       *int       `json:"http_status,omitempty"`
	ErrorKind        string     `json:"error_kind,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	LatencyMs        *int       `json:"latency_ms,omitempty"`
	PromptTokens     *int       `json:"prompt_tokens,omitempty"`
	CompletionTokens *int       `json:"completion_tokens,omitempty"`
	UsageSource      string     `json:"usage_source,omitempty"`
}

type RequestLogFilter struct {
	AccountID string
	Status    string
	Stream    *bool
	ErrorKind string
	Model     string
	ID        string
	Query     string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

type RequestLogList struct {
	Items  []RequestLog `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

type RequestStats struct {
	Window    RequestStatsWindow   `json:"window"`
	Totals    RequestStatsTotals   `json:"totals"`
	Latency   RequestStatsLatency  `json:"latency"`
	Tokens    RequestStatsTokens   `json:"tokens"`
	Status    []RequestStatsBucket `json:"status"`
	Errors    []RequestStatsBucket `json:"errors"`
	Models    []RequestStatsNamed  `json:"models"`
	Accounts  []RequestStatsNamed  `json:"accounts"`
	Providers []RequestStatsNamed  `json:"providers"`
	Series    []RequestStatsPoint  `json:"series"`
}

type RequestStatsWindow struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Hours int       `json:"hours"`
}

type RequestStatsTotals struct {
	Requests    int     `json:"requests"`
	OK          int     `json:"ok"`
	Incomplete  int     `json:"incomplete"`
	Error       int     `json:"error"`
	Canceled    int     `json:"canceled"`
	Streaming   int     `json:"streaming"`
	SuccessRate float64 `json:"success_rate"`
}

type RequestStatsLatency struct {
	AvgMs     *int `json:"avg_ms,omitempty"`
	P50Ms     *int `json:"p50_ms,omitempty"`
	P95Ms     *int `json:"p95_ms,omitempty"`
	TTFBAvgMs *int `json:"ttfb_avg_ms,omitempty"`
}

type RequestStatsTokens struct {
	Prompt     int64 `json:"prompt"`
	Completion int64 `json:"completion"`
	CacheRead  int64 `json:"cache_read"`
	Total      int64 `json:"total"`
}

type RequestStatsBucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type RequestStatsNamed struct {
	Key          string `json:"key"`
	Count        int    `json:"count"`
	OK           int    `json:"ok"`
	Error        int    `json:"error"`
	LatencyAvgMs *int   `json:"latency_avg_ms,omitempty"`
}

type RequestStatsPoint struct {
	At       time.Time `json:"at"`
	Requests int       `json:"requests"`
	OK       int       `json:"ok"`
	Error    int       `json:"error"`
}

func NewRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(raw)
}

func NewAttemptID() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("att_%d", time.Now().UnixNano())
	}
	return "att_" + hex.EncodeToString(raw)
}
