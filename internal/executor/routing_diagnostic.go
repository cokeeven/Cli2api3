package executor

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func expandedFailover(provider string) bool { return provider == "workbuddy" || provider == "trae" }

func retryCap(configured int, expanded bool) int {
	if configured <= 0 {
		configured = 64
	}
	if !expanded {
		return min(4, configured)
	}
	return min(64, configured)
}

// Budget is fixed from the initial qualified set. New accounts arriving during
// this request do not enlarge it; every actual failure is excluded permanently.
func (l *routeLoop) initializeBudget(e ChatExecutor, model string) {
	if l.budgetInitialized {
		return
	}
	l.budgetInitialized = true
	expanded := len(l.platforms) > 0
	for _, platform := range l.platforms {
		provider, _ := splitPlatform(platform)
		expanded = expanded && expandedFailover(provider)
	}
	l.expanded = expanded
	count := 0
	for i, platform := range l.platforms {
		provider, region := splitPlatform(platform)
		q := e.routeQuery("", provider, region, model, l.allowed, nil, l.routeEligibility())
		for _, item := range e.Pool.Items() {
			if i > 0 && !(itemHasCatalogModel(item, routeModel(model)) || itemHasProvenModel(item, routeModel(model))) {
				continue
			}
			if routeMatches(item, q) && !itemDown(item, time.Now()) && !itemModelDown(item, q, time.Now()) {
				count++
			}
		}
	}
	l.initialCandidates = count
	l.attempts = max(1, min(count, retryCap(e.MaxAttempts, expanded)))
}

func (l *routeLoop) noteFailure(item Item, err error) {
	l.lastErr = err
	if l.failures == nil {
		l.failures = map[string]Classified{}
	}
	l.failures[item.ID] = ClassifyError(err)
}

func (e ChatExecutor) observeFailedRoute(l *routeLoop, item Item, c Classified) {
	if !(c.Kind == accounts.KindAuth && expandedFailover(itemProvider(item))) && c.Kind != accounts.KindQuota && !(c.Scope == "model" && c.Code == "6004") {
		return
	}
	l.routing.Migrate = true
	// Keep other platforms' existing binding behavior. For these adapters a
	// confirmed credential/quota rejection invalidates the failed binding now.
	if expandedFailover(itemProvider(item)) && l.routing.BoundAccount == item.ID && l.routing.Source != routingPin &&
		e.SessionAffinity.ForgetVersion(l.routing.SessionKey, item.ID, l.routing.BindingVersion) {
		l.routing.BindingVersion = 0
	}
}

func (l *routeLoop) finishRouting(e ChatExecutor, err error, stop string) error {
	if err != nil && l.lastErr != nil && l.lastAccount != "" {
		l.noteFailure(Item{ID: l.lastAccount}, l.lastErr)
	}
	if err != nil {
		c := ClassifyError(err)
		if requestContextDone(l.ctx, err) {
			stop = "request_canceled"
		} else if stop == "" {
			stop = "upstream_rejected"
			if c.Failover && l.index >= l.attempts {
				stop = "candidates_exhausted"
				if l.attempts < l.initialCandidates {
					stop = "attempt_limit"
				}
				if l.expanded {
					err = routeStoppedError(stop, c)
				}
			}
		}
	} else if stop == "" {
		stop = "completed"
	}
	if e.OnRoute == nil {
		return err
	}
	d := accounts.RequestRoutingDiagnostic{
		RequestID: l.requestID, InitialCandidates: l.initialCandidates, AttemptBudget: l.attempts,
		Attempted: l.index, StopReason: stop, LastAccountID: l.lastAccount,
		CapacityQueueStatus:   l.capacityQueueStatus,
		CapacityQueueWaitMs:   l.capacityQueueWaitMs,
		CapacityQueuePosition: l.capacityQueuePosition,
	}
	if l.lastErr != nil {
		c := ClassifyError(l.lastErr)
		d.LastUpstreamKind, d.LastUpstreamCode, d.LastUpstreamScope = c.Kind, c.Code, c.Scope
	}
	if err != nil {
		c := ClassifyError(err)
		d.FinalSelectionKind, d.FinalSelectionCode = c.Kind, c.Code
	}
	q := e.routeQuery("", l.hardProvider, "", l.request.Model, l.allowed, l.excluded, l.routeEligibility())
	for _, item := range e.Pool.Items() {
		row := accounts.RouteAccountDiagnostic{AccountID: item.ID, Provider: itemProvider(item), Region: itemRegion(item), Reason: l.accountReason(item, q), CatalogKnown: item.Models != nil, QuotaKnown: item.Quota != nil}
		if !item.ModelsAt.IsZero() {
			value := item.ModelsAt
			row.CatalogAt = &value
		}
		if item.Quota != nil {
			if value, err := time.Parse(time.RFC3339, item.Quota.FetchedAt); err == nil {
				row.QuotaAt = &value
			}
		}
		if failure, ok := l.failures[item.ID]; ok {
			row.FailureKind, row.FailureCode, row.FailureScope = failure.Kind, failure.Code, failure.Scope
		}
		if reason := l.guardReasons[item.ID]; reason != "" {
			row.Reason = reason
		}
		d.Accounts = append(d.Accounts, row)
	}
	sort.Slice(d.Accounts, func(i, j int) bool { return d.Accounts[i].AccountID < d.Accounts[j].AccountID })
	e.OnRoute(d)
	return err
}

func (l *routeLoop) accountReason(item Item, q RouteQuery) string {
	switch {
	case !providerAllowed(item.Provider, item.Region, q.AllowedProviders):
		return "provider_permission"
	case q.ProviderFilter != "" && itemProvider(item) != q.ProviderFilter:
		return "provider_filter"
	case l.regionFilter != "" && l.providerFilter == itemProvider(item) && itemRegion(item) != l.regionFilter:
		return "region_filter"
	case !itemReady(item):
		return "not_ready"
	case q.Eligible != nil && !q.Eligible(item):
		return "protocol_capability"
	case !itemCouldServeModel(item, q.PublicModel):
		return "model_catalog"
	}
	platform := platformID(item.Provider, item.Region)
	if len(l.platforms) > 0 {
		allowed := false
		for _, p := range l.platforms {
			allowed = allowed || p == platform
		}
		if !allowed {
			return "platform_policy"
		}
	}
	if _, tried := l.failures[item.ID]; tried {
		return "upstream_failed"
	}
	if _, excluded := l.excluded[item.ID]; excluded {
		return "already_attempted"
	}
	if item.Quota != nil && item.Quota.Exceeded && !itemModelFree(item, q.PublicModel) {
		return "quota_exhausted"
	}
	if itemDown(item, time.Now()) {
		return "account_cooldown"
	}
	if itemModelDown(item, q, time.Now()) {
		return "model_cooldown"
	}
	if itemSaturated(item) {
		return "account_concurrency"
	}
	if item.ID == l.lastAccount {
		return "selected"
	}
	return "eligible"
}

func routeStoppedError(stop string, prior Classified) error {
	c := Classified{Kind: accounts.KindUnavailable, Status: 503, Code: "route_" + stop, Type: "api_error", Message: "routing stopped: " + strings.ReplaceAll(stop, "_", " ")}
	if prior.Kind == accounts.KindRateLimit || prior.Kind == accounts.KindQuota {
		c.Kind, c.Status, c.RetryAfter = prior.Kind, 429, prior.RetryAfter
	}
	return NewExecutionError(c, nil)
}

// The context's typed deadline cause must survive a selection failure too.
func routeContextError(ctx context.Context, err error) error {
	return providers.ChatContextError(ctx, err)
}
