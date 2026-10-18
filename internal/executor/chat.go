package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type providerRegistry = providers.Registry

type AttemptHook func(accounts.RequestAttempt)

type ChatExecutor struct {
	Pool      *Pool
	WorkerKey string
	// WorkerKeySource, when set, supplies the live key shared by executor copies.
	WorkerKeySource         func() string
	HTTPClient              *http.Client
	Providers               *providerRegistry
	OnAttempt               AttemptHook
	OnRoute                 func(accounts.RequestRoutingDiagnostic)
	MaxAttempts             int
	RequestTimeout          time.Duration
	CapacityQueueMaxWaiters int
	CapacityQueueWait       time.Duration
	ChatTimeouts            providers.ChatTimeouts
	Budgets                 accounts.UsageBudgetStore
	SessionAffinity         *SessionAffinity
}

type ChatResult struct {
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
	ConsumedCredits          *float64
	AccountID                string
	Provider                 string
	AttemptCount             int
	RawNote                  string
	Routing                  string
	ReasoningLevel           string
	OutputLimit              *int
	OutputLimitSource        string
	OutputBudgetEvidence     *providers.OutputBudgetEvidence
	PromptStripped           *bool
}

type StreamCompletion struct {
	PromptTokens, CompletionTokens *int
	UsageSource                    string
	Credits                        *float64
	Err                            error
	Completed                      bool
}

type StreamResult struct {
	Finalize             func(StreamCompletion)
	Response             *http.Response
	AccountID            string
	Provider             string
	AttemptCount         int
	TTFBMs               int
	Routing              string
	ReasoningLevel       string
	OutputLimit          *int
	OutputLimitSource    string
	OutputBudgetEvidence *providers.OutputBudgetEvidence
	PromptStripped       *bool
	// NativeResponses marks the body as an upstream OpenAI Responses SSE stream
	// that /v1/responses can relay verbatim instead of translating.
	NativeResponses bool
}

type NativeResponseResult struct {
	Response       json.RawMessage
	AccountID      string
	Provider       string
	AttemptCount   int
	Routing        string
	ReasoningLevel string
	FinishReason   string
	PromptTokens   *int
	OutputTokens   *int
	CachedTokens   *int
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	if strings.TrimSpace(id) == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type allowedProvidersKey struct{}

func WithAllowedProviders(ctx context.Context, providers []string) context.Context {
	if len(providers) == 0 {
		return ctx
	}
	copied := append([]string{}, providers...)
	return context.WithValue(ctx, allowedProvidersKey{}, copied)
}

func allowedProvidersFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	providers, _ := ctx.Value(allowedProvidersKey{}).([]string)
	return providers
}

func requestContextDone(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil)
}

const (
	routingPool         = "pool"
	routingPin          = "pin"
	routingSticky       = "sticky"
	routingStickyEscape = "sticky_escape"
)

// maxWorkerResponseBytes caps a fully buffered worker response. Non-stream
// completions are decoded from a single []byte; without a limit one inflated
// upstream payload could pin unbounded memory per in-flight request. 64 MiB
// matches translate.MaxCollectedResponseBytes used by the other collectors.
const maxWorkerResponseBytes = 64 << 20

type routingPlan struct {
	Source         string
	SessionKey     string
	BoundAccount   string
	PublicModel    string
	BindingVersion uint64
	Migrate        bool
}

func (e ChatExecutor) bindSession(plan routingPlan, accountID string) {
	if plan.Source == routingPin || plan.SessionKey == "" || e.SessionAffinity == nil {
		return
	}
	if plan.BoundAccount != "" && plan.BoundAccount != accountID && !plan.Migrate {
		return
	}
	e.SessionAffinity.BindVersion(plan.SessionKey, accountID, plan.BindingVersion)
}

func (e ChatExecutor) observeRouting(plan *routingPlan, accountID string) {
	if plan == nil || plan.Source != routingSticky || plan.BoundAccount == "" {
		return
	}
	if strings.TrimSpace(accountID) == plan.BoundAccount {
		return
	}
	plan.Source = routingStickyEscape
	if e.SessionAffinity != nil {
		e.SessionAffinity.RecordEscape(e.stickyEscapeReason(plan))
	}
}

func (e ChatExecutor) stickyEscapeReason(plan *routingPlan) string {
	if plan == nil || e.Pool == nil {
		return "upstream_failover"
	}
	item, ok := e.Pool.ByID(plan.BoundAccount)
	if !ok {
		return "account_missing"
	}
	if item.Ready != nil && !*item.Ready {
		return "not_ready"
	}
	if !item.DownUntil.IsZero() && time.Now().Before(item.DownUntil) {
		return "account_cooldown"
	}
	model := accounts.CanonicalModelID(plan.PublicModel)
	if model != "auto" {
		if until, ok := item.ModelDownUntil[model]; ok && time.Now().Before(until) {
			return "model_cooldown"
		}
	}
	if item.MaxInFlight > 0 && max(item.InFlight, item.LiveInFlight) >= item.MaxInFlight {
		return "concurrency_saturated"
	}
	return "upstream_failover"
}

// CommitSession binds a successfully completed stream. The executor cannot know
// whether an SSE response reached [DONE], so the relay calls this only after it
// has finished without an upstream or client error.
func (e ChatExecutor) CommitSession(ctx context.Context, req translate.ChatRequest, routing, accountID string) {
	if routing == routingPin || e.SessionAffinity == nil {
		return
	}
	e.SessionAffinity.Bind(resolveSessionKey(ctx, req), accountID)
}

func itemProvider(item Item) string {
	return accounts.NormalizeProviderFamily(item.Provider)
}

// stickyAccountCanServeModel keeps a bound cooling empty-catalog account on
// the same model so regional escape still works, but does not let that
// unknown catalog pin a later, different model (Devin → Deepseek compact).
func stickyAccountCanServeModel(item Item, publicModel string) bool {
	if item.Models != nil {
		return ItemCouldServeModel(item, publicModel)
	}
	if strings.TrimSpace(publicModel) == "" {
		return true
	}
	if len(item.ProvenModels) == 0 {
		return true
	}
	probe := item
	probe.Models = []string{}
	return ItemCouldServeModel(probe, publicModel)
}

func (e ChatExecutor) prepareRouting(ctx context.Context, prefer, providerFilter string, req translate.ChatRequest) (string, string, string, routingPlan) {
	prefer = strings.TrimSpace(prefer)
	providerFilter = strings.ToLower(strings.TrimSpace(providerFilter))
	publicModel := req.Model
	if prefer != "" {
		return prefer, providerFilter, "", routingPlan{Source: routingPin, PublicModel: publicModel}
	}

	plan := routingPlan{Source: routingPool, SessionKey: resolveSessionKey(ctx, req), PublicModel: publicModel}
	if plan.SessionKey == "" || e.SessionAffinity == nil || e.Pool == nil {
		return "", providerFilter, "", plan
	}
	accountID, version, ok := e.SessionAffinity.GetVersion(plan.SessionKey)
	plan.BindingVersion = version
	if !ok {
		return "", providerFilter, "", plan
	}
	item, ok := e.Pool.ByID(accountID)
	if !ok {
		e.SessionAffinity.RecordEscape("account_missing")
		e.SessionAffinity.Forget(plan.SessionKey)
		plan.BindingVersion = 0
		return "", providerFilter, "", plan
	}
	if providerFilter != "" && providerFilter != itemProvider(item) {
		e.SessionAffinity.RecordEscape("provider_mismatch")
		return "", providerFilter, "", plan
	}
	if !accounts.ProviderRegionAllowed(itemProvider(item), accounts.NormalizeRegion(item.Region), allowedProvidersFrom(ctx)) {
		e.SessionAffinity.RecordEscape("provider_not_allowed")
		return "", providerFilter, "", plan
	}
	if !stickyAccountCanServeModel(item, publicModel) {
		e.SessionAffinity.RecordEscape("model_unavailable")
		return "", providerFilter, "", plan
	}
	migrate := item.LastKind == KindQuota || (item.Quota != nil && item.Quota.Exceeded)
	if item.ModelLastKind[routeModel(publicModel)] == KindQuota && itemModelDown(item, RouteQuery{PublicModel: publicModel}, time.Now()) {
		migrate = true
	}
	return item.ID, itemProvider(item), accounts.NormalizeRegion(item.Region), routingPlan{
		Source: routingSticky, SessionKey: plan.SessionKey, BoundAccount: item.ID, PublicModel: publicModel, BindingVersion: version, Migrate: migrate,
	}
}

func NewChatExecutor(pool *Pool, workerKey string) ChatExecutor {
	if pool == nil {
		pool = NewPool(nil, nil)
	}
	return ChatExecutor{
		Pool:                    pool,
		WorkerKey:               strings.TrimSpace(workerKey),
		MaxAttempts:             64,
		RequestTimeout:          5 * time.Minute,
		CapacityQueueMaxWaiters: 64,
		CapacityQueueWait:       5 * time.Second,
		SessionAffinity:         NewSessionAffinity(defaultSessionAffinityTTL, defaultSessionAffinityCapacity),
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (e ChatExecutor) routeQuery(prefer, providerFilter, regionFilter, publicModel string, allowed []string, excluded map[string]struct{}, eligible func(Item) bool) RouteQuery {
	return RouteQuery{
		PublicModel:      publicModel,
		PreferAccount:    prefer,
		ProviderFilter:   providerFilter,
		RegionFilter:     regionFilter,
		AllowedProviders: allowed,
		Excluded:         excluded,
		Eligible:         eligible,
	}
}

func (e ChatExecutor) pick(requestID, prefer, providerFilter, regionFilter, publicModel string, allowed []string, excluded map[string]struct{}, eligible func(Item) bool) (Item, error) {
	query := e.routeQuery(prefer, providerFilter, regionFilter, publicModel, allowed, excluded, eligible)
	if e.Pool != nil {
		if item, ok := e.Pool.PickRoute(query); ok {
			if item.Quota != nil && item.Quota.Exceeded && !itemModelFree(item, publicModel) {
				return Item{}, quotaBlockedError()
			}
			if retryAfter := e.Pool.RetryAfter(item, publicModel); retryAfter > 0 {
				return Item{}, coolingPickError(item, publicModel, retryAfter)
			}
			return item, nil
		}
		// PickRoute returned false with an eligible route: every
		// eligible account is concurrency-saturated (a cooling account
		// would have been surfaced with ok=true for a retry-after hint).
		// Sending the request would just round-trip into the worker's
		// 429 busy, so report local capacity instead of an upstream limit.
		// This must precede the model_not_available
		// check: saturated means the model IS served, just at capacity.
		if e.Pool.LenRoute(query) > 0 {
			return Item{}, NewExecutionError(Classified{
				Kind: accounts.KindConcurrencyCapacity, Status: 429, Code: "concurrency_capacity",
				Type: "api_error", Scope: "local_capacity", Message: "all eligible accounts are at local capacity",
			}, nil)
		}
		// PickRoute filters a snapshot-exhausted paid account out, while
		// preserving free routes. Keep that operator-visible cause when no
		// live candidate remains instead of reporting a missing model/account.
		for _, item := range e.Pool.Items() {
			if item.Quota == nil || !item.Quota.Exceeded || itemModelFree(item, publicModel) {
				continue
			}
			item.Quota = nil
			if routeBaseMatches(item, query) && itemCouldServeModel(item, publicModel) {
				return Item{}, quotaBlockedError()
			}
		}
		if publicModel != "" && publicModel != "auto" {
			unfiltered := query
			unfiltered.PublicModel = ""
			if e.Pool.LenRoute(unfiltered) > 0 {
				e.logModelRouteMiss(requestID, query)
				return Item{}, fmt.Errorf("model_not_available: %s is not available for the selected accounts", publicModel)
			}
		}
	}
	if len(allowed) > 0 && providerFilter != "" && !accounts.ProviderAllowed(providerFilter, allowed) {
		return Item{}, fmt.Errorf("api key cannot use provider %s", providerFilter)
	}
	if providerFilter != "" && regionFilter != "" {
		return Item{}, fmt.Errorf("no %s/%s accounts available", providerFilter, regionFilter)
	}
	if providerFilter != "" {
		// A key whose grant list narrows this family to a single region
		// should fail with that region in the message, even when the
		// request itself did not pin one.
		if regionFilter, narrowed := keyGrantedSingleRegion(providerFilter, allowed); narrowed {
			return Item{}, fmt.Errorf("no %s/%s accounts available", providerFilter, regionFilter)
		}
		return Item{}, fmt.Errorf("no %s accounts available", providerFilter)
	}
	if len(allowed) > 0 {
		return Item{}, fmt.Errorf("no accounts available for this api key")
	}
	return Item{}, fmt.Errorf("no worker accounts configured")
}

func (e ChatExecutor) logModelRouteMiss(requestID string, query RouteQuery) {
	if e.Pool == nil {
		return
	}
	allowed := append([]string(nil), query.AllowedProviders...)
	sort.Strings(allowed)
	excluded := make([]string, 0, len(query.Excluded))
	for id := range query.Excluded {
		excluded = append(excluded, id)
	}
	sort.Strings(excluded)
	summaries := make([]string, 0, e.Pool.Len())
	for _, item := range e.Pool.Items() {
		summaries = append(summaries, modelRouteAccountSummary(item, query.PublicModel, query.Excluded))
	}
	log.Printf("model route unavailable request_id=%q model=%q provider=%q region=%q prefer=%q allowed=%q excluded=%q accounts=[%s]",
		requestID, query.PublicModel, query.ProviderFilter, query.RegionFilter, query.PreferAccount,
		strings.Join(allowed, ","), strings.Join(excluded, ","), strings.Join(summaries, " "))
}

func modelRouteAccountSummary(item Item, publicModel string, excluded map[string]struct{}) string {
	ready := item.Ready == nil || *item.Ready
	hot := item.Hot != nil && *item.Hot
	quotaExceeded := item.Quota != nil && item.Quota.Exceeded
	_, isExcluded := excluded[item.ID]
	want := accounts.CanonicalModelID(publicModel)
	catalogHas, provenHas := false, false
	for _, model := range item.Models {
		catalogHas = catalogHas || accounts.CanonicalModelID(model) == want
	}
	for _, model := range item.ProvenModels {
		provenHas = provenHas || accounts.CanonicalModelID(model) == want
	}
	catalog := "unknown"
	if item.Models != nil {
		catalog = fmt.Sprintf("count:%d models:%s", len(item.Models), compactModelList(item.Models, 12))
	}
	catalogAge := "unknown"
	if !item.ModelsAt.IsZero() {
		catalogAge = time.Since(item.ModelsAt).Round(time.Second).String()
	}
	modelDownUntil := time.Time{}
	if item.ModelDownUntil != nil {
		modelDownUntil = item.ModelDownUntil[want]
	}
	return fmt.Sprintf("{id:%q provider:%q region:%q ready:%t hot:%t quota_exceeded:%t down_until:%q model_down_until:%q in_flight:%d/%d excluded:%t catalog_has:%t proven_has:%t catalog_age:%q catalog:%q}",
		item.ID, item.Provider, item.Region, ready, hot, quotaExceeded, logTime(item.DownUntil), logTime(modelDownUntil),
		item.InFlight, item.MaxInFlight, isExcluded, catalogHas, provenHas, catalogAge, catalog)
}

func compactModelList(models []string, limit int) string {
	if len(models) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(models) {
		limit = len(models)
	}
	shown := append([]string(nil), models[:limit]...)
	if limit < len(models) {
		return fmt.Sprintf("[%s,+%d]", strings.Join(shown, ","), len(models)-limit)
	}
	return "[" + strings.Join(shown, ",") + "]"
}

func logTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func coolingPickError(item Item, publicModel string, retryAfter time.Duration) error {
	failover := true
	kind := accounts.KindRateLimit
	code := "rate_limit"
	typ := "api_error"
	message := "all accounts are cooling down"
	if !item.DownUntil.IsZero() && time.Now().Before(item.DownUntil) && item.LastKind == accounts.KindQuota {
		kind = accounts.KindQuota
		code = "insufficient_quota"
		typ = "insufficient_quota"
		failover = false
		if strings.TrimSpace(publicModel) != "" {
			message = fmt.Sprintf("all accounts that can serve %s are on quota cooldown", publicModel)
		} else {
			message = "all accounts are on quota cooldown"
		}
	} else if strings.TrimSpace(publicModel) != "" {
		if until, ok := item.ModelDownUntil[accounts.CanonicalModelID(publicModel)]; ok && !until.IsZero() {
			message = fmt.Sprintf("model %s is cooling down on all available accounts", publicModel)
		}
	}
	return NewExecutionError(Classified{
		Kind: kind, Status: 429, Code: code, Type: typ, Message: message,
		Cooldown: retryAfter, RetryAfter: retryAfter, Failover: failover,
	}, nil)
}

func (e ChatExecutor) attemptsFor(providerFilter, regionFilter, publicModel string, allowed []string, eligible ...func(Item) bool) int {
	var predicate func(Item) bool
	if len(eligible) > 0 {
		predicate = eligible[0]
	}
	maxAttempts := retryCap(e.MaxAttempts, expandedFailover(providerFilter))
	if e.Pool != nil {
		if n := e.Pool.LenRoute(e.routeQuery("", providerFilter, regionFilter, publicModel, allowed, nil, predicate)); n > 0 {
			if n > maxAttempts {
				return maxAttempts
			}
			return n
		}
	}
	return 1
}

func pinRegion(current, next string) string {
	if current != "" {
		return current
	}
	return accounts.NormalizeRegion(next)
}

// keyGrantedSingleRegion inspects an API key allowlist for one provider
// family. It returns the single region the grants narrow the family to, with
// narrowed=true. A bare family entry or an empty allowlist means every region
// (narrowed=false); two or more distinct region grants also stay false — in
// that case region stickiness keeps working exactly as before: the first
// picked account decides the region for the rest of the request.
func keyGrantedSingleRegion(providerFilter string, allowed []string) (string, bool) {
	if providerFilter == "" || len(allowed) == 0 {
		return "", false
	}
	regions, allRegions := accounts.GrantedRegions(providerFilter, allowed)
	if allRegions || len(regions) != 1 {
		return "", false
	}
	return regions[0], true
}

// qoderDirect reports whether this Qoder account chats through the registered
// adapter instead of its login worker. Tests that stub a worker URL and do not
// register the adapter stay on the worker path.
func (e ChatExecutor) qoderDirect(item Item) bool {
	if itemProvider(item) != "qoder" || e.Providers == nil {
		return false
	}
	adapter, ok := e.Providers.Get("qoder")
	return ok && adapter.Chat != nil
}

func isInProcessItem(item Item) bool {
	if item.Runtime == string(providers.RuntimeInProcess) {
		return true
	}
	if itemProvider(item) != "qoder" && item.URL == "" {
		return true
	}
	return false
}

// ObserveStreamFailure applies a post-headers streaming failure to the pool.
// Upstream can answer 200 and then fail inside the SSE body, which happens
// after the executor already returned a successful StreamResult; the relay
// error is the first place the failure is observable. Same-account retry is
// impossible at that point (bytes are on the wire), so this only records the
// classified state for the next request's scheduling.
func (e ChatExecutor) ObserveStreamFailure(accountID string, err error, model string) {
	if e.Pool == nil || accountID == "" || err == nil || requestContextDone(nil, err) {
		return
	}
	classified := e.classifyInProcessError(err)
	if classified.Kind == "" || classified.Kind == accounts.KindCanceled {
		return
	}
	if classified.Kind == accounts.KindInvalidRequest {
		// The request body was rejected; the account itself is healthy.
		return
	}
	if classified.Kind == accounts.KindModelNotAvailable {
		e.handleModelAvailabilityFailure("", "stream_body", accountID, model, classified)
		return
	}

	e.markClassified(accountID, classified, model)
}

func (e ChatExecutor) handleModelAvailabilityFailure(requestID, source, accountID, model string, classified Classified) {
	if e.Pool == nil || accountID == "" {
		return
	}
	item, _ := e.Pool.ByID(accountID)
	action := "preserve_catalog"
	if shouldEvictUnavailableModel(classified) {
		e.Pool.RemoveModel(accountID, model)
		action = "evict_model"
	}
	log.Printf("model route account failure request_id=%q source=%q account=%q provider=%q region=%q model=%q kind=%q code=%q status=%d action=%q message=%q account_state=%s",
		requestID, source, accountID, item.Provider, item.Region, model, classified.Kind, classified.Code,
		classified.Status, action, truncateLogValue(classified.Message, 300), modelRouteAccountSummary(item, model, nil))
}

func shouldEvictUnavailableModel(classified Classified) bool {
	if classified.Kind != accounts.KindModelNotAvailable {
		return false
	}
	searchable := strings.ToLower(strings.Join([]string{classified.Code, classified.Message}, " "))
	return !strings.Contains(searchable, "model_catalog_unavailable") &&
		!strings.Contains(searchable, "dynamic model catalog is unavailable") &&
		!strings.Contains(searchable, "model catalog unavailable")
}

func truncateLogValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

// markClassified records a classified failure. model scopes the cooldown to
// the requested public model so one rate-limited model does not take the
// whole account offline; pass "" for an account-wide cooldown.
func (e ChatExecutor) markClassified(id string, c Classified, model string) {
	if e.Pool == nil || id == "" || c.Kind == KindConcurrencyCapacity {
		return
	}
	if c.Scope == "platform" {
		failed, ok := e.Pool.ByID(id)
		if !ok {
			return
		}
		c.Model = ""
		for _, item := range e.Pool.Items() {
			if itemProvider(item) == itemProvider(failed) && itemRegion(item) == itemRegion(failed) {
				e.Pool.MarkClassified(item.ID, c)
			}
		}
		return
	}
	if c.Model == "" && ((c.Kind == accounts.KindRateLimit && c.Scope != "account") || c.Scope == "model") {
		c.Model = model
	}
	e.Pool.MarkClassified(id, c)
}

// markOK records a success scoped to the requested public model so a 200
// on one model does not discard a cooldown recorded for another. Pass an
// empty model for a true account-level recovery.
func (e ChatExecutor) markOK(id, model string, version ...uint64) {
	if e.Pool == nil {
		return
	}
	if len(version) > 0 {
		e.Pool.MarkOKVersion(id, model, version[0])
	} else {
		e.Pool.MarkOK(id, model)
	}
}

func (e ChatExecutor) recordAttempt(ctx context.Context, attempt accounts.RequestAttempt, failure ...error) {
	if e.OnAttempt == nil {
		return
	}
	if len(failure) > 0 && failure[0] != nil {
		classified := ClassifyError(failure[0])
		if attempt.ErrorKind == "" {
			attempt.ErrorKind = classified.Kind
		}
		if attempt.ErrorScope == "" {
			attempt.ErrorScope = classified.Scope
		}
		if attempt.ErrorCode == "" {
			attempt.ErrorCode = classified.Code
		}
		var providerErr *providers.Error
		if errors.As(failure[0], &providerErr) && providerErr != nil && providerErr.UpstreamResetAt != nil {
			reset := providerErr.UpstreamResetAt.UTC()
			attempt.UpstreamResetAt = &reset
		}
	}
	meta, _ := ctx.Value(attemptMetadataKey{}).(attemptMetadata)
	attempt.Provider = meta.Provider
	attempt.Model = meta.Model
	attempt.RouteReason = meta.Reason
	attempt.KeyID = routingIdentityFrom(ctx).KeyID
	if attempt.Provider == "" && e.Pool != nil {
		if item, ok := e.Pool.ByID(attempt.AccountID); ok {
			attempt.Provider = item.Provider
			if attempt.Region == "" {
				attempt.Region = item.Region
			}
			if item.Quota != nil && attempt.ProviderPlan == "" {
				attempt.ProviderPlan = strings.TrimSpace(item.Quota.Plan)
			}
		}
	} else if attempt.Region == "" && e.Pool != nil {
		if item, ok := e.Pool.ByID(attempt.AccountID); ok {
			attempt.Region = item.Region
			if item.Quota != nil && attempt.ProviderPlan == "" {
				attempt.ProviderPlan = strings.TrimSpace(item.Quota.Plan)
			}
		}
	} else if e.Pool != nil && attempt.ProviderPlan == "" {
		if item, ok := e.Pool.ByID(attempt.AccountID); ok && item.Quota != nil {
			attempt.ProviderPlan = strings.TrimSpace(item.Quota.Plan)
		}
	}
	if attempt.UsageState == "" {
		attempt.UsageState = "unknown"
		if strings.Contains(attempt.UsageSource, "estimat") {
			attempt.UsageState = "estimated"
		} else if attempt.PromptTokens != nil && attempt.CompletionTokens != nil && attempt.UsageSource != "" {
			attempt.UsageState = "actual"
		}
	}
	if attempt.ID == "" {
		attempt.ID = meta.ID
		if attempt.ID == "" {
			attempt.ID = accounts.NewAttemptID()
		}
	}
	if attempt.RequestID == "" {
		attempt.RequestID = RequestIDFromContext(ctx)
	}
	if attempt.RequestID == "" {
		return
	}
	e.OnAttempt(attempt)
}

// buildWorkerChatPayload is the OpenAI-shaped body a non-Qoder worker stub
// still accepts. Qoder production chat does not use it.
func buildWorkerChatPayload(req translate.ChatRequest, stream bool) map[string]any {
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   stream,
	}
	if len(req.MaxCompletionTokens) > 0 {
		payload["max_tokens"] = json.RawMessage(req.MaxCompletionTokens)
	} else if len(req.MaxTokens) > 0 {
		payload["max_tokens"] = json.RawMessage(req.MaxTokens)
	}
	if len(req.Temperature) > 0 {
		payload["temperature"] = json.RawMessage(req.Temperature)
	}
	if len(req.Tools) > 0 {
		payload["tools"] = json.RawMessage(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		payload["tool_choice"] = json.RawMessage(req.ToolChoice)
	}
	if len(req.ReasoningEffort) > 0 {
		payload["reasoning_effort"] = json.RawMessage(req.ReasoningEffort)
	}
	return payload
}

func (e ChatExecutor) newWorkerRequest(ctx context.Context, item Item, payload []byte, prefer string) (*http.Request, error) {
	account := prefer
	if account == "" {
		account = item.ID
	}
	key := e.WorkerKey
	if e.WorkerKeySource != nil {
		key = e.WorkerKeySource()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(item.URL, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	if account != "" {
		httpReq.Header.Set("X-Qoder-Account", account)
	}
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		httpReq.Header.Set("X-Request-Id", requestID)
	}
	return httpReq, nil
}

func classifyWorkerErr(resp *http.Response, body string) Classified {
	status := 0
	retryAfter := ""
	kind := ""
	failover := ""
	if resp != nil {
		status = resp.StatusCode
		retryAfter = resp.Header.Get("Retry-After")
		kind = resp.Header.Get("X-Qoder-Error-Kind")
		failover = resp.Header.Get("X-Qoder-Failover")
	}
	classified := Classify(status, body, retryAfter, kind, failover)
	classified.EvidenceSource = "qoder_worker_response"
	return classified
}

type routeLoop struct {
	budgetInitialized     bool
	expanded              bool
	initialCandidates     int
	lastAccount           string
	stopReason            string
	capacityQueueStatus   string
	capacityQueueWaitMs   int64
	capacityQueuePosition int
	failures              map[string]Classified
	guardReasons          map[string]string
	requestID             string
	prefer                string
	providerFilter        string
	regionFilter          string
	routing               routingPlan
	excluded              map[string]struct{}
	lastErr               error
	allowed               []string
	attempts              int
	pinned                string
	index                 int
	// eligible narrows candidates to one protocol capability; nil admits all.
	eligible      func(Item) bool
	ctx           context.Context
	identity      routingIdentity
	platforms     []string
	platformIndex int
	hardProvider  string
	release       func()
	reservation   string
	started       time.Time
	request       translate.ChatRequest
	strictAccount string
	native        bool
	cancel        context.CancelFunc
}

func (e ChatExecutor) newRouteLoop(ctx context.Context, prefer, providerFilter string, req translate.ChatRequest) routeLoop {
	hardProvider := providerFilter
	prefer, providerFilter, regionFilter, routing := e.prepareRouting(ctx, prefer, providerFilter, req)
	strictAccount := strictAccountFrom(ctx)
	if strictAccount != "" && e.Pool != nil {
		if item, ok := e.Pool.ByID(strictAccount); ok {
			prefer, providerFilter, regionFilter = strictAccount, item.Provider, item.Region
			hardProvider = item.Provider
		}
	}
	allowed := allowedProvidersFrom(ctx)
	// A key whose grants narrow the selected family to exactly one region
	// pre-seeds the region filter: scheduling never even considers accounts
	// of other regions — including a pin to an out-of-grant account (the
	// grant overrides the pinned account's region, so the pin falls back
	// into the granted region) and every failover hop.
	if providerFilter != "" {
		if granted, narrowed := keyGrantedSingleRegion(providerFilter, allowed); narrowed {
			regionFilter = granted
		}
	}
	if regionFilter == "" && prefer != "" && e.Pool != nil {
		if pinnedItem, ok := e.Pool.ByID(prefer); ok {
			regionFilter = pinRegion("", pinnedItem.Region)
		}
	}
	attempts := retryCap(e.MaxAttempts, true)
	loop := routeLoop{
		ctx: ctx, identity: routingIdentityFrom(ctx), hardProvider: hardProvider, request: req, strictAccount: strictAccount,
		requestID:      RequestIDFromContext(ctx),
		prefer:         prefer,
		providerFilter: providerFilter,
		regionFilter:   regionFilter,
		routing:        routing,
		excluded:       map[string]struct{}{},
		allowed:        allowed,
		attempts:       attempts,
	}
	if routing.Source == routingPin {
		loop.pinned = prefer
	}
	return loop
}

func (l *routeLoop) initializePlatforms(e ChatExecutor, publicModel string) {
	if l.platforms == nil {
		if order, ok := l.ctx.Value(platformOrderContext{}).([]string); ok {
			l.platforms = append([]string{}, order...)
		}
	}
	if l.platforms == nil && e.Pool != nil {
		q := e.routeQuery(l.prefer, l.hardProvider, "", publicModel, l.allowed, nil, l.routeEligibility())
		l.platforms = e.Pool.platformOrder(q, l.identity)
		if l.prefer != "" {
			if item, ok := e.Pool.ByID(l.prefer); ok {
				anchor := platformID(item.Provider, item.Region)
				for i, p := range l.platforms {
					if p == anchor {
						copy(l.platforms[1:i+1], l.platforms[:i])
						l.platforms[0] = anchor
						break
					}
				}
			}
		}
		if !l.identity.Policy.CrossProviderFailover || l.hardProvider != "" {
			if len(l.platforms) > 1 {
				l.platforms = l.platforms[:1]
			}
		}
	}
}

func (l *routeLoop) pickNext(e ChatExecutor, publicModel string) (Item, int, error) {
	if err := l.ctx.Err(); err != nil {
		return Item{}, l.index, routeContextError(l.ctx, err)
	}
	l.initializePlatforms(e, publicModel)
	l.initializeBudget(e, publicModel)
	var last error
	for l.platformIndex < len(l.platforms) {
		l.providerFilter, l.regionFilter = splitPlatform(l.platforms[l.platformIndex])
		q := e.routeQuery(l.prefer, l.providerFilter, l.regionFilter, publicModel, l.allowed, l.excluded, l.routeEligibility())
		if l.platformIndex > 0 {
			base := q.Eligible
			q.Eligible = func(item Item) bool {
				return (base == nil || base(item)) && routeModel(publicModel) != "" && (itemHasCatalogModel(item, routeModel(publicModel)) || itemHasProvenModel(item, routeModel(publicModel)))
			}
		}
		if l.strictAccount == "" {
			q.PreferAccount = l.preferExpiry(e, q)
		}
		item, release, ok := e.Pool.acquireRoute(q, l.identity)
		if !ok {
			// Distinguish a key platform lease from per-account saturation.
			key := l.identity.KeyID + "|" + platformID(l.providerFilter, l.regionFilter)
			for _, row := range l.identity.Policy.Platforms {
				if row.MaxInFlight > 0 && platformID(row.Provider, row.Region) == platformID(l.providerFilter, l.regionFilter) && e.Pool.PlatformInFlight()[key] >= row.MaxInFlight {
					if l.guardReasons == nil {
						l.guardReasons = map[string]string{}
					}
					for _, candidate := range e.Pool.Items() {
						if routeMatches(candidate, q) {
							l.guardReasons[candidate.ID] = "key_platform_concurrency"
						}
					}
				}
			}
			_, pickErr := e.pick(l.requestID, l.prefer, l.providerFilter, l.regionFilter, publicModel, l.allowed, l.excluded, l.routeEligibility())
			if last == nil {
				last = pickErr
			}
			if last == nil {
				last = capacityError()
			}
			l.platformIndex++
			continue
		}
		if retry := e.Pool.RetryAfter(item, publicModel); retry > 0 {
			release()
			last = coolingPickError(item, publicModel, retry)
			l.platformIndex++
			continue
		}
		if item.Quota != nil && item.Quota.Exceeded && !itemModelFree(item, publicModel) {
			release()
			last = quotaBlockedError()
			l.platformIndex++
			continue
		}
		l.release = release
		l.started = time.Now()
		if err := l.reserve(e, item); err != nil {
			release()
			l.release = nil
			l.excluded[item.ID] = struct{}{}
			if l.guardReasons == nil {
				l.guardReasons = map[string]string{}
			}
			l.guardReasons[item.ID] = ClassifyError(err).Code
			last = err
			if !strings.HasPrefix(ClassifyError(err).Code, "usage_guard_") {
				return Item{}, l.index, err
			}
			continue
		}
		attemptIndex := l.index
		l.lastAccount = item.ID
		e.observeRouting(&l.routing, item.ID)
		l.prefer = ""
		l.index++
		return item, attemptIndex, nil
	}
	if last == nil {
		_, last = e.pick(l.requestID, l.prefer, l.providerFilter, l.regionFilter, publicModel, l.allowed, l.excluded, l.routeEligibility())
	}
	if isConcurrencyCapacityError(last) {
		l.stopReason = "capacity"
	} else {
		l.stopReason = "no_candidates"
	}
	return Item{}, l.index, routeContextError(l.ctx, last)
}

func (l *routeLoop) pickNextQueued(e ChatExecutor, publicModel string) (Item, int, error) {
	// Finish the previous failed attempt before entering fair admission. When an
	// equivalent request is already queued, the selection callback below will
	// not run until this request reaches the head; retaining the previous lease
	// or usage reservation while waiting can prevent that earlier waiter from
	// using the released capacity.
	l.finishAttempt(e, nil, nil, false)
	if e.Pool == nil {
		return l.pickNext(e, publicModel)
	}
	l.initializePlatforms(e, publicModel)
	l.initializeBudget(e, publicModel)
	key := capacityRouteKey(l, publicModel)
	tryCount := 0
	item, index, err, observation := e.Pool.waitForCapacity(l.ctx, key, e.CapacityQueueMaxWaiters, e.CapacityQueueWait, func() (Item, int, error, bool) {
		if tryCount > 0 {
			l.platformIndex = 0
			l.stopReason = ""
		}
		tryCount++
		item, index, err := l.pickNext(e, publicModel)
		return item, index, err, isConcurrencyCapacityError(err)
	})
	l.capacityQueueStatus = observation.Status
	l.capacityQueueWaitMs = observation.WaitMs
	l.capacityQueuePosition = observation.Position
	switch observation.Status {
	case "timeout":
		l.stopReason = "queue_timeout"
	case "full":
		l.stopReason = "queue_full"
	case "canceled":
		l.stopReason = "request_canceled"
	case "shutdown":
		l.stopReason = "application_shutdown"
	case "disabled":
		l.stopReason = "capacity"
	case "admitted":
		l.stopReason = ""
	}
	return item, index, err
}

func (l *routeLoop) routeEligibility() func(Item) bool {
	base := l.eligible
	if l.strictAccount == "" {
		return base
	}
	return func(item Item) bool {
		return item.ID == l.strictAccount && (base == nil || base(item))
	}
}

func capacityError() error {
	return NewExecutionError(Classified{
		Kind: accounts.KindConcurrencyCapacity, Status: 429, Code: "concurrency_capacity",
		Type: "api_error", Scope: "local_capacity", Message: "account or key platform concurrency limit reached",
		RetryAfter: 5 * time.Second,
	}, nil)
}

func quotaBlockedError() error {
	return NewExecutionError(Classified{Kind: accounts.KindQuota, Status: 429, Code: "insufficient_quota", Type: "insufficient_quota", Message: "account quota exhausted; waiting for a confirmed quota refresh"}, nil)
}

func (l routeLoop) canFailover(classified Classified) bool {
	return classified.Failover && l.index < l.attempts
}

func (l *routeLoop) exclude(item Item) {
	l.excluded[item.ID] = struct{}{}
	if l.lastErr != nil {
		l.noteFailure(item, l.lastErr)
	}
}

func (l routeLoop) headerAccount(item Item, attemptIndex int) string {
	if attemptIndex == 0 && l.pinned != "" {
		return l.pinned
	}
	return item.ID
}

func (l routeLoop) resultProvider(item Item) string {
	return firstNonEmpty(item.Provider, "qoder")
}

func (l routeLoop) pickFailure(err error) (int, string, string, error) {
	if l.lastErr != nil && l.expanded && !requestContextDone(l.ctx, err) && !isConcurrencyCapacityError(err) {
		c := ClassifyError(err)
		if c.Kind == KindAuth || c.Kind == KindUnavailable {
			err = routeStoppedError("no_candidates", c)
		}
	}
	return l.index, l.lastAccount, l.providerFilter, err
}

func (e ChatExecutor) ChatNonStream(ctx context.Context, req translate.ChatRequest, prefer, providerFilter string) (result ChatResult, returnErr error) {
	ctx, cancel := e.executionContext(ctx)
	defer cancel()
	loop := e.newRouteLoop(ctx, prefer, providerFilter, req)
	defer func() {
		result.Routing = loop.routing.Source
		loop.finishAttempt(e, nil, nil, false)
		returnErr = loop.finishRouting(e, returnErr, loop.stopReason)
	}()
	for loop.index < loop.attempts {
		item, i, err := loop.pickNextQueued(e, req.Model)
		if err != nil {
			attempts, accountID, provider, pickErr := loop.pickFailure(err)
			if loop.lastErr != nil {
				return ChatResult{AttemptCount: attempts, AccountID: accountID, Provider: provider}, pickErr
			}
			return ChatResult{}, pickErr
		}
		ctx = withAttemptMetadata(ctx, item, req.Model, loop.routeReason(e), loop.reservation)
		if isInProcessItem(item) || e.qoderDirect(item) {
			result, classified, err := e.chatInProcessNonStreamAttempt(ctx, item, req, i, loop.index < loop.attempts)
			if err == nil {
				result.AttemptCount = i + 1
				e.observeRouting(&loop.routing, result.AccountID)
				e.bindSession(loop.routing, result.AccountID)
				loop.finishAttempt(e, &result, nil, true)
				return result, nil
			}
			loop.lastErr = err
			result.AttemptCount = i + 1
			if result.HasUsageEvidence() {
				loop.finishAttempt(e, &result, nil, false)
			}
			e.observeFailedRoute(&loop, item, classified)
			if requestContextDone(ctx, err) {
				return result, err
			}
			if loop.canFailover(classified) {
				loop.exclude(item)
				continue
			}
			return result, err
		}
		payload, err := json.Marshal(buildWorkerChatPayload(req, false))
		if err != nil {
			return ChatResult{}, err
		}
		headerAccount := loop.headerAccount(item, i)
		httpReq, err := e.newWorkerRequest(ctx, item, payload, headerAccount)
		if err != nil {
			return ChatResult{}, err
		}
		started := time.Now()
		resp, err := e.HTTPClient.Do(httpReq)
		if err != nil {
			if requestContextDone(ctx, err) {
				latency := int(time.Since(started).Milliseconds())
				e.recordAttempt(ctx, accounts.RequestAttempt{
					AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: ptrTime(time.Now().UTC()),
					Status: accounts.AttemptStatusCanceled, ErrorKind: accounts.KindCanceled, ErrorMessage: err.Error(), LatencyMs: &latency,
				})
				return ChatResult{AttemptCount: i + 1, AccountID: item.ID, Provider: item.Provider}, err
			}
			classified := Classify(0, err.Error(), "", accounts.KindUnavailable, "")
			loop.lastErr = NewExecutionError(classified, fmt.Errorf("worker %s request failed: %w", item.ID, err))
			e.markClassified(item.ID, classified, req.Model)
			latency := int(time.Since(started).Milliseconds())
			e.recordAttempt(ctx, accounts.RequestAttempt{
				AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: ptrTime(time.Now().UTC()),
				Status: accounts.AttemptStatusFailover, ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified), ErrorMessage: loop.lastErr.Error(), LatencyMs: &latency,
			})
			loop.exclude(item)
			continue
		}
		// Non-stream worker responses are buffered whole; cap them so a
		// pathological upstream cannot pin unbounded memory per request.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxWorkerResponseBytes))
		resp.Body.Close()
		finished := time.Now().UTC()
		latency := int(finished.Sub(started).Milliseconds())
		if account := resp.Header.Get("X-Qoder-Account"); account != "" {
			item.ID = account
		}
		if resp.StatusCode >= 300 {
			msg := strings.TrimSpace(string(body))
			classified := classifyWorkerErr(resp, msg)
			loop.lastErr = NewExecutionError(classified, nil)
			e.observeFailedRoute(&loop, item, classified)
			if classified.Kind == accounts.KindModelNotAvailable {
				e.handleModelAvailabilityFailure(RequestIDFromContext(ctx), "worker_non_stream", item.ID, req.Model, classified)
			}
			e.markClassified(item.ID, classified, req.Model)
			status := accounts.AttemptStatusError
			if loop.canFailover(classified) {
				status = accounts.AttemptStatusFailover
				loop.exclude(item)
				e.recordAttempt(ctx, accounts.RequestAttempt{
					AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
					Status: status, HTTPStatus: ptrInt(resp.StatusCode), ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified),
					ErrorMessage: truncateErr(msg), LatencyMs: &latency,
				})
				continue
			}
			e.recordAttempt(ctx, accounts.RequestAttempt{
				AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
				Status: status, HTTPStatus: ptrInt(resp.StatusCode), ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified),
				ErrorMessage: truncateErr(msg), LatencyMs: &latency,
			})
			return ChatResult{AttemptCount: i + 1, AccountID: item.ID, Provider: loop.resultProvider(item)}, loop.lastErr
		}
		result, err := decodeChatResult(req, body)
		if err != nil {
			e.recordAttempt(ctx, accounts.RequestAttempt{
				AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
				Status: accounts.AttemptStatusError, HTTPStatus: ptrInt(resp.StatusCode),
				ErrorKind: accounts.KindUnavailable, ErrorMessage: truncateErr(err.Error()), LatencyMs: &latency,
			})
			return ChatResult{AttemptCount: i + 1, AccountID: item.ID, Provider: loop.resultProvider(item)}, err
		}
		result.AccountID = item.ID
		result.Provider = loop.resultProvider(item)
		result.AttemptCount = i + 1
		result.ReasoningLevel = RequestedReasoningLevel(req)
		if result.ReasoningLevel != "" {
			logResolvedReasoning(ctx, "chat_non_stream", item, req.Model, result.ReasoningLevel)
		}
		e.observeRouting(&loop.routing, item.ID)
		e.bindSession(loop.routing, item.ID)
		e.markOK(item.ID, req.Model, item.StateVersion)
		e.recordAttempt(ctx, accounts.RequestAttempt{
			AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
			Status: accounts.AttemptStatusOK, HTTPStatus: ptrInt(resp.StatusCode), LatencyMs: &latency,
			PromptTokens: ptrInt(result.PromptTokens), CompletionTokens: ptrInt(result.CompletionTokens),
			UsageSource: result.UsageSource, Credits: result.Credits,
		})
		loop.finishAttempt(e, &result, nil, true)
		return result, nil
	}
	if loop.lastErr == nil {
		loop.lastErr = fmt.Errorf("no worker accounts available")
	}
	return ChatResult{AttemptCount: loop.attempts}, loop.lastErr
}

// sanitizeForItem applies the account-level system-prompt policy. Provider
// families with upstream content screening (WorkBuddy) strip caller system
// prompts when the account opts in; Qoder workers intentionally preserve them.
// WorkBuddy still needs a leading system slot after the strip (code 11128);
// the adapter inserts an empty placeholder, this helper only drops caller text.
func sanitizeForItem(item Item, req translate.ChatRequest) translate.ChatRequest {
	sanitized, _ := sanitizeForItemWithDecision(item, req)
	return sanitized
}

func boolPointer(value bool) *bool { return &value }

func sanitizeForItemWithDecision(item Item, req translate.ChatRequest) (translate.ChatRequest, bool) {
	if native := NativeModelID(item, req.Model); native != "" {
		req.Model = native
	}
	if item.DropSystemPrompt && itemProvider(item) != "qoder" {
		stripped := false
		for _, message := range req.Messages {
			if strings.EqualFold(strings.TrimSpace(message.Role), "system") || strings.EqualFold(strings.TrimSpace(message.Role), "developer") {
				stripped = true
				break
			}
		}
		return translate.DropSystemMessages(req), stripped
	}
	return req, false
}

func (e ChatExecutor) chatInProcessNonStreamAttempt(ctx context.Context, item Item, req translate.ChatRequest, attemptIndex int, allowFailover bool) (ChatResult, Classified, error) {
	adapter, _ := e.Providers.Get(itemProvider(item))
	if adapter.Chat == nil {
		return ChatResult{}, Classified{}, fmt.Errorf("provider %s does not implement chat", item.Provider)
	}
	started := time.Now()
	sanitized, promptStripped := sanitizeForItemWithDecision(item, req)
	outcome, err := adapter.Chat.ChatNonStream(ctx, item.ID, sanitized)
	err = providers.ChatContextError(ctx, err)
	finished := time.Now().UTC()
	latency := int(finished.Sub(started).Milliseconds())
	if err == nil {
		logResolvedReasoning(ctx, "chat_non_stream", item, req.Model, outcome.ReasoningLevel)
	}
	if err != nil {
		classified := e.classifyInProcessError(err)
		partial := chatResultFromOutcome(outcome, item, promptStripped)
		hasOutput := outcome.Content != "" || outcome.Reasoning != "" || (len(outcome.ToolCalls) > 0 && string(outcome.ToolCalls) != "null")
		knownUsage := partial.HasUsageEvidence()
		if hasOutput || knownUsage {
			classified.Failover = false
			classified.RetrySafe = false
		}
		if classified.Kind == accounts.KindModelNotAvailable {
			e.handleModelAvailabilityFailure(RequestIDFromContext(ctx), "provider_non_stream", item.ID, req.Model, classified)
		}
		if !requestContextDone(ctx, err) {
			e.markClassified(item.ID, classified, req.Model)
		}
		status := accounts.AttemptStatusError
		if requestContextDone(ctx, err) {
			status = accounts.AttemptStatusCanceled
		}
		if classified.Failover && allowFailover {
			status = accounts.AttemptStatusFailover
		}
		attempt := accounts.RequestAttempt{
			AttemptIndex: attemptIndex, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
			Status: status, ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified), ErrorMessage: truncateErr(err.Error()), LatencyMs: &latency,
		}
		if knownUsage {
			attempt.PromptTokens, attempt.CompletionTokens = partial.UsageTokenEvidence()
			attempt.UsageSource = outcome.UsageSource
			attempt.Credits = outcome.Credits
		}
		e.recordAttempt(ctx, attempt, err)
		return partial, classified, NewExecutionError(classified, err)
	}
	e.markOK(item.ID, req.Model, item.StateVersion)
	result := chatResultFromOutcome(outcome, item, promptStripped)
	prompt, completion := result.UsageTokenEvidence()
	e.recordAttempt(ctx, accounts.RequestAttempt{
		AttemptIndex: attemptIndex, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
		Status: accounts.AttemptStatusOK, LatencyMs: &latency,
		PromptTokens: prompt, CompletionTokens: completion,
		UsageSource: outcome.UsageSource, Credits: outcome.Credits,
	})
	return result, Classified{}, nil
}

func chatResultFromOutcome(outcome providers.ChatOutcome, item Item, promptStripped bool) ChatResult {
	return ChatResult{
		Model:                    outcome.Model,
		Content:                  outcome.Content,
		Reasoning:                outcome.Reasoning,
		ToolCalls:                outcome.ToolCalls,
		FinishReason:             outcome.FinishReason,
		FinishReasonSource:       outcome.FinishReasonSource,
		PromptTokens:             outcome.PromptTokens,
		CompletionTokens:         outcome.CompletionTokens,
		ReportedPromptTokens:     outcome.ReportedPromptTokens,
		ReportedCompletionTokens: outcome.ReportedCompletionTokens,
		UsageExtensions:          outcome.UsageExtensions,
		CacheReadTokens:          outcome.CacheReadTokens,
		CacheWriteTokens:         outcome.CacheWriteTokens,
		CachedTokens:             outcome.CachedTokens,
		UsageSource:              outcome.UsageSource,
		ConsumedCredits:          outcome.Credits,
		AccountID:                item.ID,
		Provider:                 item.Provider,
		ReasoningLevel:           outcome.ReasoningLevel,
		OutputLimit:              outcome.OutputLimit,
		OutputLimitSource:        outcome.OutputLimitSource,
		OutputBudgetEvidence:     outcome.OutputBudgetEvidence,
		PromptStripped:           boolPointer(promptStripped),
	}
}

func (r ChatResult) UsageTokenEvidence() (*int, *int) {
	if r.ReportedPromptTokens != nil || r.ReportedCompletionTokens != nil {
		return r.ReportedPromptTokens, r.ReportedCompletionTokens
	}
	if r.UsageSource == "upstream" || r.UsageSource == "provider" || r.UsageSource == "estimate" {
		return ptrInt(r.PromptTokens), ptrInt(r.CompletionTokens)
	}
	return nil, nil
}

func (r ChatResult) HasUsageEvidence() bool {
	return r.UsageSource == "upstream" || r.UsageSource == "provider" || r.ReportedPromptTokens != nil || r.ReportedCompletionTokens != nil || r.CacheReadTokens != nil || r.CacheWriteTokens != nil || r.CachedTokens != nil || r.Credits != nil || r.ConsumedCredits != nil
}

func (e ChatExecutor) chatInProcessStreamAttempt(ctx context.Context, item Item, req translate.ChatRequest, native *translate.NativeResponsesRequest, attemptIndex int, preferNativeResponses bool, allowFailover bool) (StreamResult, Classified, error) {
	adapter, ok := e.Providers.Get(itemProvider(item))
	if !ok {
		return StreamResult{}, Classified{}, fmt.Errorf("provider %s is not registered", item.Provider)
	}
	if !preferNativeResponses && adapter.Chat == nil {
		return StreamResult{}, Classified{}, fmt.Errorf("provider %s does not implement chat", item.Provider)
	}
	if preferNativeResponses && native != nil && adapter.NativeResponses == nil {
		return StreamResult{}, Classified{}, providers.ErrUnsupported
	}
	started := time.Now()
	var resp *http.Response
	var resolved providers.ResolvedChat
	var err error
	nativeResponses := false
	var promptStripped *bool
	if preferNativeResponses && native != nil && adapter.NativeResponses != nil {
		options := providers.RequestOptions{
			Model:            NativeModelID(item, req.Model),
			DropSystemPrompt: item.DropSystemPrompt && itemProvider(item) != "qoder",
		}
		resp, resolved, err = adapter.NativeResponses.ResponsesStream(ctx, item.ID, native, options)
		nativeResponses = err == nil
	} else {
		sanitized, stripped := sanitizeForItemWithDecision(item, req)
		promptStripped = boolPointer(stripped)
		resp, resolved, err = adapter.Chat.ChatStream(ctx, item.ID, sanitized)
	}
	if err == nil && (resp == nil || resp.Body == nil) {
		err = StreamIncompleteError()
	}
	partial := StreamResult{AccountID: item.ID, Provider: item.Provider, ReasoningLevel: resolved.ReasoningLevel, OutputLimit: resolved.OutputLimit, OutputLimitSource: resolved.OutputLimitSource, OutputBudgetEvidence: resolved.OutputBudgetEvidence, PromptStripped: promptStripped}
	err = providers.ChatContextError(ctx, err)
	if err == nil {
		logResolvedReasoning(ctx, "chat_stream", item, req.Model, resolved.ReasoningLevel)
	}
	if err != nil {
		finished := time.Now().UTC()
		latency := int(finished.Sub(started).Milliseconds())
		if errors.Is(err, providers.ErrUnsupported) {
			return partial, Classified{Kind: accounts.KindInvalidRequest, Message: err.Error()}, err
		}
		if requestContextDone(ctx, err) {
			e.recordAttempt(ctx, accounts.RequestAttempt{AttemptIndex: attemptIndex, AccountID: item.ID, StartedAt: started, FinishedAt: &finished, Status: accounts.AttemptStatusCanceled, ErrorKind: accounts.KindCanceled, ErrorMessage: err.Error(), LatencyMs: &latency})
			return partial, Classified{Kind: accounts.KindUnavailable, Message: err.Error()}, err
		}
		classified := e.classifyInProcessError(err)
		if classified.Kind == accounts.KindModelNotAvailable {
			e.handleModelAvailabilityFailure(RequestIDFromContext(ctx), "provider_stream", item.ID, req.Model, classified)
		}
		e.markClassified(item.ID, classified, req.Model)
		status := accounts.AttemptStatusError
		if classified.Failover && allowFailover {
			status = accounts.AttemptStatusFailover
		}
		e.recordAttempt(ctx, accounts.RequestAttempt{
			AttemptIndex: attemptIndex, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
			Status: status, ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified), ErrorMessage: truncateErr(err.Error()), LatencyMs: &latency,
		}, err)
		return partial, classified, NewExecutionError(classified, err)
	}
	ttfb := int(time.Since(started).Milliseconds())
	return StreamResult{
		Response: resp, AccountID: item.ID, Provider: item.Provider, TTFBMs: ttfb,
		ReasoningLevel: resolved.ReasoningLevel, OutputLimit: resolved.OutputLimit,
		OutputLimitSource: resolved.OutputLimitSource, OutputBudgetEvidence: resolved.OutputBudgetEvidence, PromptStripped: promptStripped,
		NativeResponses: nativeResponses,
	}, Classified{}, nil
}

func (e ChatExecutor) classifyInProcessError(err error) Classified { return ClassifyError(err) }

func lastAccountID(excluded map[string]struct{}) string {
	for id := range excluded {
		return id
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func decodeChatResult(req translate.ChatRequest, body []byte) (ChatResult, error) {
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			CacheReadTokens  *int     `json:"cache_read_tokens"`
			CacheWriteTokens *int     `json:"cache_write_tokens"`
			Source           string   `json:"source"`
			Credits          *float64 `json:"credits"`
			PromptDetails    struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ChatResult{}, fmt.Errorf("decode worker response: %w; body=%s", err, truncateErr(string(body)))
	}
	content := ""
	reasoning := ""
	var toolCalls json.RawMessage
	finishReason := "stop"
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
		reasoning = parsed.Choices[0].Message.ReasoningContent
		toolCalls = parsed.Choices[0].Message.ToolCalls
		if parsed.Choices[0].FinishReason != "" {
			finishReason = parsed.Choices[0].FinishReason
		} else if len(toolCalls) > 0 && string(toolCalls) != "null" {
			finishReason = "tool_calls"
		}
	}
	model := parsed.Model
	if model == "" {
		model = req.Model
	}
	source := parsed.Usage.Source
	if source == "" {
		source = "estimate"
	}
	return ChatResult{
		Model:            model,
		Content:          content,
		Reasoning:        reasoning,
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		CacheReadTokens:  parsed.Usage.CacheReadTokens,
		CacheWriteTokens: parsed.Usage.CacheWriteTokens,
		CachedTokens:     parsed.Usage.PromptDetails.CachedTokens,
		UsageSource:      source,
		Credits:          parsed.Usage.Credits,
	}, nil
}

func (e ChatExecutor) streamHTTPClient() *http.Client {
	client := e.HTTPClient
	if client == nil {
		return http.DefaultClient
	}
	if client.Timeout > 0 {
		streamClient := *client
		streamClient.Timeout = 0
		return &streamClient
	}
	return client
}

func (e ChatExecutor) ChatStreamProxy(ctx context.Context, req translate.ChatRequest, prefer, providerFilter string) (result StreamResult, returnErr error) {
	return e.chatStreamProxy(ctx, req, nil, prefer, providerFilter, false)
}

// ChatStreamProxyNativeResponses sends the original Responses request to a
// native-capable adapter. Once this path starts, retries remain native-only.
func (e ChatExecutor) ChatStreamProxyNativeResponses(ctx context.Context, req translate.ChatRequest, native *translate.NativeResponsesRequest, prefer, providerFilter string) (result StreamResult, returnErr error) {
	return e.chatStreamProxy(ctx, req, native, prefer, providerFilter, true)
}

func (e ChatExecutor) NativeResponsesNonStream(ctx context.Context, req translate.ChatRequest, native *translate.NativeResponsesRequest, prefer, providerFilter string) (result NativeResponseResult, returnErr error) {
	upstream, err := e.ChatStreamProxyNativeResponses(ctx, req, native, prefer, providerFilter)
	if err != nil {
		return NativeResponseResult{AccountID: upstream.AccountID, Provider: upstream.Provider, AttemptCount: upstream.AttemptCount, Routing: upstream.Routing, ReasoningLevel: upstream.ReasoningLevel}, err
	}
	if upstream.Response == nil || upstream.Response.Body == nil {
		return NativeResponseResult{AccountID: upstream.AccountID, Provider: upstream.Provider, AttemptCount: upstream.AttemptCount, Routing: upstream.Routing, ReasoningLevel: upstream.ReasoningLevel}, fmt.Errorf("native responses upstream returned no body")
	}
	defer upstream.Response.Body.Close()
	collected, err := translate.CollectResponses(upstream.Response.Body)
	result = NativeResponseResult{
		Response: collected.Response, AccountID: upstream.AccountID, Provider: upstream.Provider,
		AttemptCount: upstream.AttemptCount, Routing: upstream.Routing, ReasoningLevel: upstream.ReasoningLevel,
		FinishReason: collected.FinishReason, PromptTokens: collected.InputTokens,
		OutputTokens: collected.OutputTokens, CachedTokens: collected.CachedTokens,
	}
	if err != nil {
		if upstream.Finalize != nil {
			upstream.Finalize(StreamCompletion{Err: err})
		}
		return result, err
	}
	if upstream.Finalize != nil {
		upstream.Finalize(StreamCompletion{PromptTokens: collected.InputTokens, CompletionTokens: collected.OutputTokens, UsageSource: "upstream", Completed: true})
	}
	return result, nil
}

// SelectResponsesRoute anchors one platform before deciding whether native
// protocol handling is useful. The returned context must be used by execution.
func (e ChatExecutor) SelectResponsesRoute(ctx context.Context, req translate.ChatRequest, prefer, providerFilter string, requireNative bool) (context.Context, bool) {
	if e.Pool == nil {
		return ctx, false
	}
	loop := e.newRouteLoop(ctx, prefer, providerFilter, req)
	nativeEligible := func(item Item) bool {
		if e.Providers == nil {
			return false
		}
		adapter, ok := e.Providers.Get(itemProvider(item))
		return ok && adapter.NativeResponses != nil
	}
	if requireNative {
		loop.eligible = nativeEligible
	}
	loop.initializePlatforms(e, req.Model)
	order := append([]string{}, loop.platforms...)
	ctx = context.WithValue(ctx, platformOrderContext{}, order)
	if len(order) == 0 {
		return ctx, false
	}
	provider, region := splitPlatform(order[0])
	for _, item := range e.Pool.Items() {
		if itemProvider(item) == provider && itemRegion(item) == region && providerAllowed(item.Provider, item.Region, loop.allowed) && itemCouldServeModel(item, req.Model) && nativeEligible(item) {
			return ctx, true
		}
	}
	return ctx, false
}

func (e ChatExecutor) HasNativeResponsesRoute(ctx context.Context, req translate.ChatRequest, prefer, providerFilter string) bool {
	_, native := e.SelectResponsesRoute(ctx, req, prefer, providerFilter, false)
	return native
}

func (e ChatExecutor) chatStreamProxy(ctx context.Context, req translate.ChatRequest, native *translate.NativeResponsesRequest, prefer, providerFilter string, preferNativeResponses bool) (result StreamResult, returnErr error) {
	ctx, cancel := e.executionContext(ctx)
	loop := e.newRouteLoop(ctx, prefer, providerFilter, req)
	loop.cancel = cancel
	defer func() {
		if loop.cancel != nil {
			loop.cancel()
		}
	}()
	loop.native = native != nil
	if preferNativeResponses && native != nil && e.Providers == nil {
		return StreamResult{}, providers.ErrUnsupported
	}
	if preferNativeResponses && native != nil {
		loop.eligible = func(item Item) bool {
			adapter, ok := e.Providers.Get(itemProvider(item))
			return ok && adapter.NativeResponses != nil
		}

	}
	defer func() {
		result.Routing = loop.routing.Source
		loop.finishAttempt(e, nil, nil, false)
		if returnErr != nil {
			returnErr = loop.finishRouting(e, returnErr, loop.stopReason)
		}
	}()
	startedAll := time.Now()
	for loop.index < loop.attempts {
		item, i, err := loop.pickNextQueued(e, req.Model)
		if err != nil {
			attempts, accountID, provider, pickErr := loop.pickFailure(err)
			if loop.lastErr != nil {
				return StreamResult{AttemptCount: attempts, AccountID: accountID, Provider: provider}, pickErr
			}
			return StreamResult{}, pickErr
		}
		ctx = withAttemptMetadata(ctx, item, req.Model, loop.routeReason(e), loop.reservation)
		if isInProcessItem(item) || (e.qoderDirect(item) && !(preferNativeResponses && native != nil)) {
			result, classified, err := e.chatInProcessStreamAttempt(ctx, item, req, native, i, preferNativeResponses, loop.index < loop.attempts)
			result.AttemptCount = i + 1

			if err == nil {
				result.AttemptCount = i + 1
				e.observeRouting(&loop.routing, result.AccountID)
				e.finalizeStream(ctx, req, &result, &loop, item, i)
				return result, nil
			}
			loop.lastErr = err
			e.observeFailedRoute(&loop, item, classified)
			if requestContextDone(ctx, err) {
				return result, err
			}
			if loop.canFailover(classified) {
				loop.exclude(item)
				continue
			}
			return result, err
		}
		payload, err := json.Marshal(buildWorkerChatPayload(req, true))
		if err != nil {
			return StreamResult{}, err
		}
		headerAccount := loop.headerAccount(item, i)
		httpReq, err := e.newWorkerRequest(ctx, item, payload, headerAccount)
		if err != nil {
			return StreamResult{}, err
		}
		started := time.Now()
		resp, err := e.streamHTTPClient().Do(httpReq)
		if err != nil {
			if requestContextDone(ctx, err) {
				latency := int(time.Since(started).Milliseconds())
				e.recordAttempt(ctx, accounts.RequestAttempt{
					AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: ptrTime(time.Now().UTC()),
					Status: accounts.AttemptStatusCanceled, ErrorKind: accounts.KindCanceled, ErrorMessage: err.Error(), LatencyMs: &latency,
				})
				return StreamResult{AttemptCount: i + 1, AccountID: item.ID, Provider: item.Provider}, err
			}
			classified := Classify(0, err.Error(), "", accounts.KindUnavailable, "")
			loop.lastErr = NewExecutionError(classified, fmt.Errorf("worker %s stream request failed: %w", item.ID, err))
			e.markClassified(item.ID, classified, req.Model)
			latency := int(time.Since(started).Milliseconds())
			e.recordAttempt(ctx, accounts.RequestAttempt{
				AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: ptrTime(time.Now().UTC()),
				Status: accounts.AttemptStatusFailover, ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified), ErrorMessage: loop.lastErr.Error(), LatencyMs: &latency,
			})
			loop.exclude(item)
			continue
		}
		if account := resp.Header.Get("X-Qoder-Account"); account != "" {
			item.ID = account
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxWorkerResponseBytes))
			resp.Body.Close()
			msg := strings.TrimSpace(string(body))
			classified := classifyWorkerErr(resp, msg)
			loop.lastErr = NewExecutionError(classified, nil)
			e.observeFailedRoute(&loop, item, classified)
			if classified.Kind == accounts.KindModelNotAvailable {
				e.handleModelAvailabilityFailure(RequestIDFromContext(ctx), "worker_stream", item.ID, req.Model, classified)
			}
			e.markClassified(item.ID, classified, req.Model)
			finished := time.Now().UTC()
			latency := int(finished.Sub(started).Milliseconds())
			status := accounts.AttemptStatusError
			if loop.canFailover(classified) {
				status = accounts.AttemptStatusFailover
				loop.exclude(item)
				e.recordAttempt(ctx, accounts.RequestAttempt{
					AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
					Status: status, HTTPStatus: ptrInt(resp.StatusCode), ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified),
					ErrorMessage: truncateErr(msg), LatencyMs: &latency,
				})
				continue
			}
			e.recordAttempt(ctx, accounts.RequestAttempt{
				AttemptIndex: i, AccountID: item.ID, StartedAt: started, FinishedAt: &finished,
				Status: status, HTTPStatus: ptrInt(resp.StatusCode), ErrorKind: classified.Kind, ErrorScope: classified.Scope, UsageState: rejectedUsageState(classified),
				ErrorMessage: truncateErr(msg), LatencyMs: &latency,
			})
			return StreamResult{AttemptCount: i + 1, AccountID: item.ID, Provider: loop.resultProvider(item)}, loop.lastErr
		}
		ttfb := int(time.Since(startedAll).Milliseconds())
		e.observeRouting(&loop.routing, item.ID)
		resolved := RequestedReasoningLevel(req)
		if resolved != "" {
			logResolvedReasoning(ctx, "chat_stream", item, req.Model, resolved)
		}
		result = StreamResult{
			Response:       resp,
			AccountID:      item.ID,
			Provider:       loop.resultProvider(item),
			AttemptCount:   i + 1,
			TTFBMs:         ttfb,
			ReasoningLevel: resolved,
		}
		e.finalizeStream(ctx, req, &result, &loop, item, i)
		return result, nil
	}
	if loop.lastErr == nil {
		loop.lastErr = fmt.Errorf("no worker accounts available")
	}
	return StreamResult{AttemptCount: loop.attempts}, loop.lastErr
}

func ptrInt(value int) *int { return &value }

func ptrTime(value time.Time) *time.Time { return &value }

func truncateErr(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) <= 500 {
		return msg
	}
	return msg[:500]
}

// logResolvedReasoning emits one line per successful chat attempt showing the
// reasoning level actually sent upstream. Empty levels are omitted to keep
// noise down for providers that do not accept a reasoning knob.
func logResolvedReasoning(ctx context.Context, source string, item Item, model, level string) {
	if level == "" {
		return
	}
	log.Printf("chat reasoning resolved request_id=%q source=%q account=%q provider=%q model=%q level=%q",
		RequestIDFromContext(ctx), source, item.ID, item.Provider, model, level)
}
