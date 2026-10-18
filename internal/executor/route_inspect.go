package executor

import (
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// RouteCandidate is a read-only explanation of how one account relates to a
// provider/model route. It is computed from the same pool snapshot and
// eligibility predicates used by PickRoute, without advancing rotation state.
type RouteCandidate struct {
	ID                      string                 `json:"id"`
	Provider                string                 `json:"provider"`
	Region                  string                 `json:"region"`
	Reason                  string                 `json:"reason"`
	Schedulable             bool                   `json:"schedulable"`
	CatalogKnown            bool                   `json:"catalog_known"`
	ModelCooldownUntil      *time.Time             `json:"model_cooldown_until,omitempty"`
	AccountCooldownUntil    *time.Time             `json:"account_cooldown_until,omitempty"`
	ModelCooldownEvidence   *RouteCooldownEvidence `json:"model_cooldown_evidence,omitempty"`
	AccountCooldownEvidence *RouteCooldownEvidence `json:"account_cooldown_evidence,omitempty"`
	AccountQuotaEvidence    *RouteQuotaEvidence    `json:"account_quota_evidence,omitempty"`
	QuotaKnown              bool                   `json:"quota_known"`
	QuotaRemaining          *float64               `json:"quota_remaining,omitempty"`
	QuotaUnit               string                 `json:"quota_unit,omitempty"`
	QuotaFetchedAt          string                 `json:"quota_fetched_at,omitempty"`
	InFlight                int                    `json:"in_flight"`
	MaxInFlight             int                    `json:"max_in_flight,omitempty"`
}

// RouteCooldownEvidence is a safe view of a restriction's provenance. Message
// is deliberately omitted because provider error bodies may contain details
// unsuitable for a diagnostic API.
type RouteCooldownEvidence struct {
	FirstObservedAt   *time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt    *time.Time `json:"last_observed_at,omitempty"`
	Code              string     `json:"code,omitempty"`
	Source            string     `json:"source,omitempty"`
	Scope             string     `json:"scope,omitempty"`
	ReportedScope     string     `json:"reported_scope,omitempty"`
	UpstreamResetAt   *time.Time `json:"upstream_reset_at,omitempty"`
	LocalBackoffUntil *time.Time `json:"local_backoff_until,omitempty"`
}

// RouteQuotaEvidence explains a known account-level quota restriction without
// implying that a reset or local cooldown deadline exists.
type RouteQuotaEvidence struct {
	FirstObservedAt *time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt  *time.Time `json:"last_observed_at,omitempty"`
	Code            string     `json:"code,omitempty"`
	Source          string     `json:"source,omitempty"`
	Scope           string     `json:"scope,omitempty"`
	ReportedScope   string     `json:"reported_scope,omitempty"`
	UpstreamResetAt *time.Time `json:"upstream_reset_at,omitempty"`
}

func (p *Pool) InspectRoute(q RouteQuery) []RouteCandidate {
	if p == nil {
		return []RouteCandidate{}
	}
	p.mu.Lock()
	items := make([]Item, len(p.items))
	for i := range p.items {
		items[i] = p.items[i].clone()
		items[i].LiveInFlight = p.accountLive[items[i].ID]
	}
	p.mu.Unlock()

	now := time.Now()
	out := make([]RouteCandidate, 0, len(items))
	for _, item := range items {
		reason := inspectRouteItem(item, q, now)
		row := RouteCandidate{
			ID: item.ID, Provider: itemProvider(item), Region: itemRegion(item), Reason: reason,
			Schedulable: reason == "available", CatalogKnown: item.Models != nil,
			InFlight: item.LiveInFlight, MaxInFlight: item.MaxInFlight,
		}
		if item.Quota != nil {
			row.QuotaKnown, row.QuotaRemaining, row.QuotaUnit, row.QuotaFetchedAt = true, &item.Quota.Remaining, item.Quota.Unit, item.Quota.FetchedAt
			if item.Quota.Exceeded {
				row.AccountQuotaEvidence = quotaRestrictionEvidenceView(item.Quota.RestrictionEvidence)
			}
		}
		if !item.DownUntil.IsZero() {
			value := item.DownUntil
			row.AccountCooldownUntil = &value
			row.AccountCooldownEvidence = cooldownEvidenceView(item.CooldownEvidence[""])
		}
		if until := resumeAt(item, q); !until.IsZero() && itemModelDown(item, q, now) {
			value := until
			row.ModelCooldownUntil = &value
			row.ModelCooldownEvidence = cooldownEvidenceView(item.CooldownEvidence[routeModel(q.PublicModel)])
		}
		out = append(out, row)
	}
	return out
}

func quotaRestrictionEvidenceView(evidence *accounts.QuotaRestrictionEvidence) *RouteQuotaEvidence {
	if evidence == nil {
		return nil
	}
	return &RouteQuotaEvidence{
		FirstObservedAt: cloneRouteTime(evidence.FirstObservedAt),
		LastObservedAt:  cloneRouteTime(evidence.LastObservedAt),
		Code:            evidence.Code,
		Source:          evidence.Source,
		Scope:           evidence.Scope,
		ReportedScope:   evidence.ReportedScope,
		UpstreamResetAt: cloneRouteTime(evidence.UpstreamResetAt),
	}
}

func cloneRouteTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func cooldownEvidenceView(evidence accounts.CooldownEvidence) *RouteCooldownEvidence {
	view := &RouteCooldownEvidence{
		Code: evidence.Code, Source: evidence.Source, Scope: evidence.Scope, ReportedScope: evidence.ReportedScope,
	}
	if !evidence.FirstObservedAt.IsZero() {
		value := evidence.FirstObservedAt
		view.FirstObservedAt = &value
	}
	if !evidence.LastObservedAt.IsZero() {
		value := evidence.LastObservedAt
		view.LastObservedAt = &value
	}
	if evidence.UpstreamResetAt != nil && !evidence.UpstreamResetAt.IsZero() {
		value := *evidence.UpstreamResetAt
		view.UpstreamResetAt = &value
	}
	if !evidence.LocalBackoffUntil.IsZero() {
		value := evidence.LocalBackoffUntil
		view.LocalBackoffUntil = &value
	}
	return view
}

func inspectRouteItem(item Item, q RouteQuery, now time.Time) string {
	if !providerAllowed(item.Provider, itemRegion(item), q.AllowedProviders) {
		return "provider_permission"
	}
	if q.ProviderFilter != "" && itemProvider(item) != itemProvider(Item{Provider: q.ProviderFilter}) {
		return "provider_filter"
	}
	if q.RegionFilter != "" && itemRegion(item) != itemRegion(Item{Region: q.RegionFilter}) {
		return "region_filter"
	}
	if !itemReady(item) {
		return "not_ready"
	}
	if q.Eligible != nil && !q.Eligible(item) {
		return "protocol_capability"
	}
	if _, excluded := q.Excluded[item.ID]; excluded {
		return "already_attempted"
	}
	if item.Quota != nil && item.Quota.Exceeded && !itemModelFree(item, q.PublicModel) {
		return "quota_exhausted"
	}
	if !itemCouldServeModel(item, q.PublicModel) {
		return "model_catalog"
	}
	if itemDown(item, now) {
		return "account_cooldown"
	}
	if itemModelDown(item, q, now) {
		return "model_cooldown"
	}
	if itemSaturated(item) {
		return "account_concurrency"
	}
	if routeMatches(item, q) {
		return "available"
	}
	if q.PublicModel != "" && !itemHasModel(item, q.PublicModel) {
		return "model_catalog_unknown"
	}
	return "route_ineligible"
}
