package accounts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	QuotaRefreshIntervalSecret         = "quota_refresh_interval_minutes"
	DefaultQuotaRefreshIntervalMinutes = 30
	MinQuotaRefreshIntervalMinutes     = 5
	MaxQuotaRefreshIntervalMinutes     = 1440
)

type QuotaSettingsReader interface {
	GetSecret(context.Context, string) (string, bool, error)
}

// QuotaRefreshIntervalMinutes reads the shared periodic quota refresh interval.
// A missing value keeps the established default; invalid persisted values fail
// closed so the scheduler does not turn a corrupted setting into rapid polling.
func QuotaRefreshIntervalMinutes(ctx context.Context, store QuotaSettingsReader) (int, error) {
	if store == nil {
		return DefaultQuotaRefreshIntervalMinutes, nil
	}
	value, found, err := store.GetSecret(ctx, QuotaRefreshIntervalSecret)
	if err != nil {
		return 0, err
	}
	if !found {
		return DefaultQuotaRefreshIntervalMinutes, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < MinQuotaRefreshIntervalMinutes || parsed > MaxQuotaRefreshIntervalMinutes {
		return 0, fmt.Errorf("quota refresh interval must be between %d and %d minutes", MinQuotaRefreshIntervalMinutes, MaxQuotaRefreshIntervalMinutes)
	}
	return parsed, nil
}

// QuotaWindow is one provider-native usage period, such as Devin daily
// and weekly included quota. The top-level QuotaSnapshot fields stay the
// tighter of these windows for routing.
type QuotaWindow struct {
	ID         string  `json:"id"`
	Label      string  `json:"label,omitempty"`
	Used       float64 `json:"used"`
	Total      float64 `json:"total"`
	Remaining  float64 `json:"remaining"`
	Percentage float64 `json:"percentage"`
	Unit       string  `json:"unit,omitempty"`
	ResetAt    string  `json:"reset_at,omitempty"`
	Exceeded   bool    `json:"exceeded,omitempty"`
}

// QuotaPackage is one upstream credit/resource pack that carries its own
// expiry. EndsAt is a Unix second; EndTime is the provider-native wall-clock
// string for display.
type QuotaPackage struct {
	ExpirySource string  `json:"expiry_source,omitempty"`
	ID           string  `json:"id,omitempty"`
	Source       string  `json:"source,omitempty"`
	Status       string  `json:"status,omitempty"`
	Remain       float64 `json:"remain"`
	Used         float64 `json:"used"`
	Size         float64 `json:"size"`
	Unit         string  `json:"unit,omitempty"`
	EndsAt       int64   `json:"ends_at,omitempty"`
	EndTime      string  `json:"end_time,omitempty"`
}

// QuotaRestrictionEvidence records a known quota restriction independently of
// local cooldown timers. Upstream may report a quota error without providing a
// reset time; the nil reset must remain unknown and must not be converted into
// a synthetic deadline. This type deliberately excludes provider messages and
// request content so it is safe to persist with the quota snapshot.
type QuotaRestrictionEvidence struct {
	FirstObservedAt *time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt  *time.Time `json:"last_observed_at,omitempty"`
	Code            string     `json:"code,omitempty"`
	Source          string     `json:"source,omitempty"`
	Scope           string     `json:"scope,omitempty"`
	ReportedScope   string     `json:"reported_scope,omitempty"`
	UpstreamResetAt *time.Time `json:"upstream_reset_at,omitempty"`
}

// QuotaSnapshot is the quota state for one account.
// A zero value means "unknown"; only Exceeded influences routing.
type QuotaSnapshot struct {
	Used       float64       `json:"used"`
	Total      float64       `json:"total"`
	Remaining  float64       `json:"remaining"`
	Percentage float64       `json:"percentage"`
	Unit       string        `json:"unit"`
	Exceeded   bool          `json:"exceeded"`
	Windows    []QuotaWindow `json:"windows,omitempty"`
	// ExpiresAt is the soonest package expiry (Unix seconds); 0 means the
	// provider did not report one. ExpiringRemain is the remaining amount that
	// expires at that time.
	ExpiresAt                int64          `json:"expires_at,omitempty"`
	ExpiringRemain           float64        `json:"expiring_remain,omitempty"`
	Packages                 []QuotaPackage `json:"packages,omitempty"`
	HasAddOn                 bool           `json:"has_add_on"`
	AddOnUsed                float64        `json:"add_on_used"`
	AddOnTotal               float64        `json:"add_on_total"`
	AddOnRemaining           float64        `json:"add_on_remaining"`
	AddOnUnit                string         `json:"add_on_unit"`
	AddOnAvailable           *bool          `json:"add_on_available,omitempty"`
	HasResourcePackage       bool           `json:"has_resource_package"`
	ResourcePackageUsed      float64        `json:"resource_package_used"`
	ResourcePackageTotal     float64        `json:"resource_package_total"`
	ResourcePackageRemaining float64        `json:"resource_package_remaining"`
	ResourcePackageUnit      string         `json:"resource_package_unit"`
	ResourcePackageAvailable *bool          `json:"resource_package_available,omitempty"`
	FetchedAt                string         `json:"fetched_at"`
	// RestrictionEvidence is the latest observed account-level quota error.
	// It is separate from CooldownEvidence because quota exhaustion can be
	// known even when neither upstream nor local code supplied a deadline.
	RestrictionEvidence *QuotaRestrictionEvidence `json:"restriction_evidence,omitempty"`
	// Plan is the upstream subscription tier. Empty means not reported.
	Plan string `json:"plan,omitempty"`
}
