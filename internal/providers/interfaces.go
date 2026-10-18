package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

// ErrUnsupported is returned by capability interfaces a provider does not
// implement. Callers must treat nil adapters as missing registrations, not as
// implicit support.
var ErrUnsupported = errors.New("provider capability unsupported")

type ModelCapabilities struct {
	ContextWindow    int `json:"context_window,omitempty"`
	ContextWindowMax int `json:"context_window_max,omitempty"`
	MaxOutput        int `json:"max_output_tokens,omitempty"`
	PromptMaxTokens  int `json:"prompt_max_tokens,omitempty"`
	// MaxMode tiers: when a model declares a second (Max/Max-mode) tier, these
	// carry the Max-tier prompt/output ceilings so the console can show the
	// value that matches the max-mode toggle instead of the default tier.
	PromptMaxTokensMax   int                   `json:"prompt_max_tokens_max,omitempty"`
	MaxOutputMax         int                   `json:"max_output_tokens_max,omitempty"`
	MaxMode              bool                  `json:"max_mode,omitempty"`
	Tools                bool                  `json:"tools"`
	Images               bool                  `json:"images"`
	Reasoning            bool                  `json:"reasoning"`
	ReasoningOptions     []string              `json:"reasoning_options,omitempty"`
	ReasoningDefault     string                `json:"reasoning_default,omitempty"`
	ReasoningType        string                `json:"reasoning_type,omitempty"`
	CanDisableThinking   bool                  `json:"can_disable_thinking,omitempty"`
	OutputBudgetEvidence *OutputBudgetEvidence `json:"output_budget_evidence,omitempty"`
}

// OutputBudgetEvidence is narrowly-scoped provider evidence. It describes an
// observed behavior for one provider path/client version; it is not a promise
// about untested models or future upstream behavior.
type OutputBudgetEvidence struct {
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	Region           string `json:"region"`
	EndpointPath     string `json:"endpoint_path"`
	Function         string `json:"function"`
	RequestParameter string `json:"request_parameter"`
	TokenBasis       string `json:"token_basis"`
	EvidenceDate     string `json:"evidence_date"`
	EvidenceSource   string `json:"evidence_source,omitempty"`
	ClientVersion    string `json:"client_version"`
	ClientBuild      string `json:"client_build"`
	AppVersion       string `json:"app_version"`
	EffectiveCeiling *int   `json:"effective_ceiling,omitempty"`
}

// FirstIntPointer returns the first reported integer value, preserving an
// explicit zero while allowing absent/null aliases to fall through.
func FirstIntPointer(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

type ModelInfo struct {
	NativeModel  string            `json:"native_model"`
	PublicModel  string            `json:"public_model"`
	DisplayName  string            `json:"display_name,omitempty"`
	Credits      string            `json:"credits,omitempty"`
	Free         bool              `json:"free,omitempty"`
	Capabilities ModelCapabilities `json:"capabilities"`
	// Scene is the provider-native scene/function a chat for this model must be
	// sent under. Empty means the provider has no scene split. Providers use it
	// to keep a model routed to the scene that actually serves it; it is
	// provider-internal and never a public API field.
	Scene string `json:"-"`
}

// CredentialCodec validates and stores provider credentials.
type CredentialCodec interface {
	Validate(payload []byte) error
}

type CredentialMaintainer interface {
	Keepalive(context.Context, string) error
}

// CredentialPayloadCASStore persists a refreshed credential only when the
// stored payload still matches the snapshot used for the refresh. This keeps a
// late token rotation from overwriting a newer login or refresh result.
type CredentialPayloadCASStore interface {
	SaveCredentialPayloadIfCurrent(ctx context.Context, accountID, expectedFormat string, expectedPayload []byte, format string, payload []byte) (bool, error)
}

// LoginSession describes one browser-login round for an account.
type LoginSession struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state,omitempty"`
}

// LoginSessionProvider starts and polls provider-native browser login.
type LoginSessionProvider interface {
	StartLogin(ctx context.Context, accountID string) (LoginSession, error)
	PollLogin(ctx context.Context, accountID string) (done bool, message string, err error)
}

// LoginCompleter accepts a provider callback URL copied from the browser
// when the automatic loopback redirect cannot reach this process.
type LoginCompleter interface {
	CompleteLogin(ctx context.Context, accountID, callbackURL string) error
}

// ChatOutcome is the provider-neutral non-stream result.
type ChatOutcome struct {
	Model                    string
	Content                  string
	Reasoning                string
	ToolCalls                json.RawMessage
	FinishReason             string
	FinishReasonSource       string
	PromptTokens             int
	CompletionTokens         int
	ReportedPromptTokens     *int
	ReportedCompletionTokens *int
	UsageExtensions          map[string]any
	CacheReadTokens          *int
	CacheWriteTokens         *int
	CachedTokens             *int
	UsageSource              string
	Credits                  *float64
	// ReasoningLevel is the clamped reasoning level actually sent upstream,
	// empty when the provider did not include one in the payload.
	ReasoningLevel string
	// OutputLimit and OutputLimitSource describe the actual max_tokens value
	// placed in the provider request. A nil limit means the adapter left the
	// provider default in control; its numeric value is unknown.
	OutputLimit          *int
	OutputLimitSource    string
	OutputBudgetEvidence *OutputBudgetEvidence
}

// ResolvedChat carries provider-side metadata for a stream request that the
// API layer may want to log alongside the relayed upstream response.
type ResolvedChat struct {
	// ReasoningLevel is the clamped reasoning level actually sent upstream,
	// empty when the provider did not include one in the payload.
	ReasoningLevel       string
	OutputLimit          *int
	OutputLimitSource    string
	OutputBudgetEvidence *OutputBudgetEvidence
}

func (r ResolvedChat) OutcomeMetadata() ChatOutcome {
	return ChatOutcome{ReasoningLevel: r.ReasoningLevel, OutputLimit: r.OutputLimit, OutputLimitSource: r.OutputLimitSource, OutputBudgetEvidence: r.OutputBudgetEvidence, UsageSource: "unknown"}
}

// ProviderChat executes chat for one account. Stream implementations return
// the raw upstream response for the API layer to relay.
type ProviderChat interface {
	ChatNonStream(ctx context.Context, accountID string, req translate.ChatRequest) (ChatOutcome, error)
	ChatStream(ctx context.Context, accountID string, req translate.ChatRequest) (*http.Response, ResolvedChat, error)
}

// StreamFormatNames the SSE dialect a ProviderChat.ChatStream body speaks.
// Empty means OpenAI chat-completions deltas (the shared internal contract);
// "responses" means the upstream already emits OpenAI Responses events and the
// /v1/responses endpoint can relay them without translation.
const StreamFormatResponses = "responses"

// RequestOptions are the per-attempt execution decisions the executor makes
// for one picked account. They travel explicitly, never through ctx, and are
// recomputed for every failover attempt.
type RequestOptions struct {
	// Model is the upstream model id resolved for this account (its catalog
	// spelling), not the public id the client sent.
	Model string
	// DropSystemPrompt removes caller system/developer prompts before send
	// (account policy for upstreams with content screening).
	DropSystemPrompt bool
}

// NativeResponsesStreamer is the optional native OpenAI Responses capability.
// An adapter implements it only when its upstream speaks Responses and it can
// build the upstream request from the client's original Responses body, so
// item identity, reasoning replay, and unknown members survive. The returned
// body is always an upstream Responses SSE stream; a non-stream client call
// collects it. Adapters without it serve /v1/responses through ChatStream /
// ChatNonStream and the shared chat form.
type NativeResponsesStreamer interface {
	ResponsesStream(ctx context.Context, accountID string, req *translate.NativeResponsesRequest, options RequestOptions) (*http.Response, ResolvedChat, error)
}

// ModelCatalogProvider lists models an account can currently serve.
type ModelCatalogProvider interface {
	Models(ctx context.Context, accountID string) ([]ModelInfo, error)
}

// ErrorClassifier maps provider errors to the internal taxonomy.
type ErrorClassifier interface {
	Classify(status int, body string) ClassifiedError
}

type ClassifiedError struct {
	Scope     string `json:"scope,omitempty"`
	RetrySafe bool   `json:"retry_safe,omitempty"`
	Code      string `json:"code,omitempty"`
	Kind      string `json:"kind"`
	Status    int    `json:"status"`
	Message   string `json:"message"`
}

// ImportExporter validates and exports provider-specific credential JSON.
type ImportExporter interface {
	ValidateImport(payload []byte) error
	Export(ctx context.Context, accountID string) (map[string]any, error)
}

// AccountHealth is the provider-neutral readiness snapshot for in-process
// accounts that have no child-process /health endpoint.
type AccountHealth struct {
	Ready         bool
	Hot           bool
	UID           string
	InFlight      int
	LastError     string
	LastErrorKind string
}

// QuotaWindow is one provider-native usage period. Console cards may render
// each window; routing still uses the tighter top-level QuotaInfo values.
type QuotaWindow struct {
	ID         string
	Label      string
	Used       float64
	Total      float64
	Remaining  float64
	Percentage float64
	Unit       string
	ResetAt    string
	Exceeded   bool
}

// QuotaPackage is one upstream credit/resource pack with its own expiry.
// EndsAt is a Unix second; EndTime is the provider-native wall-clock string.
type QuotaPackage struct {
	ID           string
	Source       string
	Status       string
	ExpirySource string
	Remain       float64
	Used         float64
	Size         float64
	Unit         string
	EndsAt       int64
	EndTime      string
}

// QuotaInfo is account usage for the console and exhausted-account routing.
// Callers must treat probe readiness and quota independently; quota errors
// never flip Ready.
type QuotaInfo struct {
	Used       float64
	Total      float64
	Remaining  float64
	Percentage float64
	Unit       string
	Exceeded   bool
	FetchedAt  string
	Windows    []QuotaWindow
	// ProviderID identifies which adapter produced this info. Callers use it
	// to gate provider-specific fields (e.g. package expiry) instead of
	// trusting that every adapter populates them.
	ProviderID string
	// Plan is the upstream subscription tier, such as Codex plan_type "plus".
	// Empty means the provider did not report one.
	Plan string
	// ExpiresAt is the soonest package expiry (Unix seconds); 0 means the
	// provider did not report one. ExpiringRemain is the remaining amount that
	// expires at that time. Packages carries the per-pack expiry detail.
	ExpiresAt      int64
	ExpiringRemain float64
	Packages       []QuotaPackage
}

// AccountProber refreshes provider-native readiness and optional display quota.
type AccountProber interface {
	Probe(ctx context.Context, accountID string) (AccountHealth, error)
	Quota(ctx context.Context, accountID string) (*QuotaInfo, error)
}

// Error is the in-process adapter error the executor classifies for
// cooldown and failover. Kind must be an accounts taxonomy value.
type Error struct {
	// Scope and RetrySafe describe an explicit upstream rejection. Empty
	// scope means unknown; transports must never infer billability from it.
	Scope            string
	RetrySafe        bool
	Kind             string
	Status           int
	Message          string
	Code             string
	Type             string
	RetryAfter       time.Duration
	RetryAfterHeader string
	// UpstreamResetAt is the explicit provider-supplied reset instant when the
	// adapter could parse one. Local cooldowns are never written here.
	UpstreamResetAt *time.Time
}

// WithRetryAfterHeader leaves cooldown parsing/policy in the executor.
func WithRetryAfterHeader(err error, header string) error {
	var upstream *Error
	if errors.As(err, &upstream) && len(header) <= 1024 {
		upstream.RetryAfterHeader = header
	}
	return err
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Kind
}

// Adapter bundles the optional capability interfaces. Every field may be nil;
// use Supports() before calling so unsupported paths fail explicitly.
type Adapter struct {
	ID                    string
	Credential            CredentialCodec
	Login                 LoginSessionProvider
	Chat                  ProviderChat
	Models                ModelCatalogProvider
	Classifier            ErrorClassifier
	ImportExport          ImportExporter
	Prober                AccountProber
	Checkin               AccountCheckiner
	CredentialMaintenance CredentialMaintainer
	// StreamFormat declares the SSE dialect ChatStream returns (see
	// StreamFormat* constants). Empty means chat-completions deltas.
	StreamFormat string
	// NativeResponses is the optional native Responses capability. When set,
	// /v1/responses requests routed to this adapter skip the chat form.
	NativeResponses NativeResponsesStreamer
}

func (a Adapter) Supports(capability string) bool {
	switch capability {
	case "credential":
		return a.Credential != nil
	case "login":
		return a.Login != nil
	case "chat":
		return a.Chat != nil
	case "models":
		return a.Models != nil
	case "classifier":
		return a.Classifier != nil
	case "import_export":
		return a.ImportExport != nil
	case "prober":
		return a.Prober != nil
	case "checkin":
		return a.Checkin != nil
	case "native_responses":
		return a.NativeResponses != nil
	default:
		return false
	}
}

// CredentialImport prepares a canonical payload without persistence. Ready controls
// whether an imported account may be enabled immediately.
type CredentialImport struct {
	Payload []byte
	Ready   bool
}
type CredentialImporter interface {
	Format() string
	PrepareImport([]byte) (CredentialImport, error)
}

// AdminRequest/Response carry worker protocol data, never a public HTTP writer.
type AdminRequest struct {
	AccountID, Action, Method, ContentType string
	Body                                   []byte
}
type AdminResponse struct {
	Status int
	Header map[string][]string
	Body   []byte
}
type ActionError struct {
	Code string
	Err  error
}

func (e *ActionError) Error() string { return e.Err.Error() }
func (e *ActionError) Unwrap() error { return e.Err }
