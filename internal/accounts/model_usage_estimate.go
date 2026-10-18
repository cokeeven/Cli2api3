package accounts

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	workBuddyLimitCode        = "6004"
	budgetStatisticalTimezone = "UTC"
	budgetRecentWeightRule    = "3_utc_days_6_3_1"
)

type budgetAnchor struct {
	at    time.Time
	reset time.Time
}

type budgetWindowSample struct {
	at     time.Time
	tokens int64
}

type budgetGroup struct {
	identity [5]string // account, region, actual model, known plan, public request path
	items    []ModelBudgetObservation
	samples  []budgetWindowSample
	latest   budgetAnchor
}

// EstimateWorkBuddyBudgets derives account-level estimates only from explicit
// 6004/model/reset attempts. It never applies a fixed window length. The
// compatibility entry point uses the supplied observation span; the console
// should call EstimateWorkBuddyBudgetsInRange with its bounded query range.
func EstimateWorkBuddyBudgets(observations []ModelBudgetObservation, asOf time.Time) []WorkBuddyBudgetEstimate {
	var from time.Time
	for _, item := range observations {
		if item.At.IsZero() {
			continue
		}
		if from.IsZero() || item.At.Before(from) {
			from = item.At
		}
	}
	return EstimateWorkBuddyBudgetsInRange(observations, asOf, from, false)
}

// EstimateWorkBuddyBudgetsInRange keeps numerical estimates inside the known
// observation horizon. Complete windows use their explicit prior reset as the
// start; windows that began before the bounded query are not treated as whole.
// The newest three UTC statistical days use 6/3/1 weights, normalized only over
// days that actually contain complete samples. Truncated data never produces
// numeric estimates, but explicit active restrictions remain visible.
func EstimateWorkBuddyBudgetsInRange(observations []ModelBudgetObservation, asOf, observationFrom time.Time, truncated bool) []WorkBuddyBudgetEstimate {
	if asOf.IsZero() {
		return nil
	}
	asOf = asOf.UTC()
	if !observationFrom.IsZero() {
		observationFrom = observationFrom.UTC()
	}
	groups := make(map[string]*budgetGroup)
	for _, item := range observations {
		if item.At.IsZero() || item.AccountID == "" || item.Region == "" || item.Model == "" || item.At.After(asOf) {
			continue
		}
		item.Region = strings.ToLower(strings.TrimSpace(item.Region))
		item.Model = strings.TrimSpace(item.Model)
		item.Plan = strings.ToLower(strings.TrimSpace(item.Plan))
		item.RequestPath = strings.TrimSpace(item.RequestPath)
		identity := [5]string{item.AccountID, item.Region, item.Model, item.Plan, item.RequestPath}
		key := budgetAccountGroupKey(identity)
		group := groups[key]
		if group == nil {
			group = &budgetGroup{identity: identity}
			groups[key] = group
		}
		group.items = append(group.items, item)
	}
	if len(groups) == 0 {
		return nil
	}

	rows := make([]WorkBuddyBudgetEstimate, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.items, func(i, j int) bool { return group.items[i].At.Before(group.items[j].At) })
		anchors := explicitBudgetAnchors(group.items)
		if len(anchors) == 0 {
			continue
		}
		group.latest = anchors[len(anchors)-1]
		for index := 1; index < len(anchors); index++ {
			start, end := anchors[index-1].reset, anchors[index].at
			if start.IsZero() || !start.Before(end) || (!observationFrom.IsZero() && start.Before(observationFrom)) {
				continue
			}
			used, complete, _ := sumBudgetUsage(group.items, start, end)
			if complete {
				group.samples = append(group.samples, budgetWindowSample{at: end.UTC(), tokens: used})
			}
		}

		recent := recentBudgetSamples(group.samples, asOf)
		displayed := recent
		freshness := "recent"
		if len(displayed) == 0 {
			displayed = group.samples
			if len(displayed) > 0 {
				freshness = "stale"
			} else {
				freshness = "none"
			}
		}
		row := WorkBuddyBudgetEstimate{
			AccountID: group.identity[0], Region: group.identity[1], Model: group.identity[2],
			Plan: group.identity[3], RequestPath: group.identity[4], CompleteWindows: len(group.samples),
			StatisticalTimezone: budgetStatisticalTimezone, WeightRule: budgetRecentWeightRule,
			SampleFreshness: freshness, ObservationTruncated: truncated,
			Basis: "insufficient_recent_windows",
		}
		if truncated {
			row.Basis = "truncated_observations"
			row.SampleFreshness = "truncated"
		}
		row.SampleCount = len(recent)
		row.StatisticalDays = populatedRecentBudgetDays(recent, asOf)
		setBudgetSampleRange(&row.SampleMinTokens, &row.SampleMaxTokens, &row.SampleFrom, &row.SampleTo, displayed)

		latest := group.latest
		row.ResetAt = timePointer(latest.reset)
		row.RestrictionObservedAt = timePointer(latest.at)
		row.Restricted = latest.reset.After(asOf)
		row.Cooling = row.Restricted
		if row.Restricted {
			row.RestrictionSource = "upstream_6004_explicit_reset"
			row.CoverageState = "upstream_restricted"
		} else if !observationFrom.IsZero() && latest.reset.Before(observationFrom) {
			row.CoverageState = "observation_range_boundary"
		} else {
			observed, complete, missing := sumBudgetUsage(group.items, latest.reset, asOf.Add(time.Nanosecond))
			row.MissingAttempts = missing
			row.CoverageComplete = complete
			if complete {
				row.ObservedSinceReset = int64Pointer(observed)
				row.CoverageState = "complete"
			} else if missing > 0 {
				row.CoverageState = "usage_missing"
			} else {
				row.CoverageState = "no_billable_usage_observed"
			}
		}

		if !truncated && row.RequestPath != "" && len(recent) >= 2 {
			if estimated, ok := weightedRecentBudgetMean(recent, asOf); ok {
				row.EstimatedTokens = int64Pointer(estimated)
				row.Basis = "explicit_6004_reset_windows_weighted_recent"
				if row.CoverageComplete && row.ObservedSinceReset != nil {
					remaining := estimated - *row.ObservedSinceReset
					if remaining < 0 {
						remaining = 0
					}
					row.EstimatedRemaining = int64Pointer(remaining)
				}
			}
		} else if !truncated && row.RequestPath == "" {
			row.Basis = "unknown_request_path"
		} else if !truncated && len(group.samples) > 0 && len(recent) == 0 {
			row.Basis = "stale_samples"
		}
		rows = append(rows, row)
	}

	if !truncated {
		attachWorkBuddyBudgetReferences(rows, groups, asOf)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Region != rows[j].Region {
			return rows[i].Region < rows[j].Region
		}
		if rows[i].AccountID != rows[j].AccountID {
			return rows[i].AccountID < rows[j].AccountID
		}
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		return rows[i].RequestPath < rows[j].RequestPath
	})
	return rows
}

func explicitBudgetAnchors(items []ModelBudgetObservation) []budgetAnchor {
	byReset := make(map[int64]budgetAnchor)
	for _, item := range items {
		if item.Code != workBuddyLimitCode || item.Kind != KindRateLimit || item.Scope != "model" ||
			(item.Status != AttemptStatusError && item.Status != AttemptStatusFailover) || item.UpstreamResetAt == nil {
			continue
		}
		reset := item.UpstreamResetAt.UTC()
		if !reset.After(item.At) {
			continue
		}
		key := reset.UnixNano()
		anchor, exists := byReset[key]
		if !exists || item.At.Before(anchor.at) {
			byReset[key] = budgetAnchor{at: item.At.UTC(), reset: reset}
		}
	}
	anchors := make([]budgetAnchor, 0, len(byReset))
	for _, anchor := range byReset {
		anchors = append(anchors, anchor)
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].at.Equal(anchors[j].at) {
			return anchors[i].reset.Before(anchors[j].reset)
		}
		return anchors[i].at.Before(anchors[j].at)
	})
	return anchors
}

func sumBudgetUsage(items []ModelBudgetObservation, start, end time.Time) (int64, bool, int) {
	var total int64
	seen := 0
	missing := 0
	for _, item := range items {
		if item.At.Before(start) || !item.At.Before(end) || item.Status == AttemptStatusStarted || item.UsageState == "non_billable" {
			continue
		}
		seen++
		if item.UsageState != "actual" || item.PromptTokens == nil || item.CompletionTokens == nil {
			missing++
			continue
		}
		total += int64(*item.PromptTokens) + int64(*item.CompletionTokens)
	}
	return total, missing == 0 && seen > 0, missing
}

func attachWorkBuddyBudgetReferences(rows []WorkBuddyBudgetEstimate, groups map[string]*budgetGroup, asOf time.Time) {
	for i := range rows {
		row := &rows[i]
		if row.Plan == "" || row.RequestPath == "" {
			continue
		}
		cohort := [4]string{row.Region, row.Model, row.Plan, row.RequestPath}
		accountMeans := make([]int64, 0)
		allSamples := make([]budgetWindowSample, 0)
		for _, group := range groups {
			if group.identity[0] == row.AccountID || group.identity[1] != cohort[0] || group.identity[2] != cohort[1] || group.identity[3] != cohort[2] || group.identity[4] != cohort[3] {
				continue
			}
			recent := recentBudgetSamples(group.samples, asOf)
			if len(recent) == 0 {
				continue
			}
			mean, ok := weightedRecentBudgetMean(recent, asOf)
			if !ok {
				continue
			}
			accountMeans = append(accountMeans, mean)
			allSamples = append(allSamples, recent...)
		}
		if len(accountMeans) == 0 {
			continue
		}
		var accountTotal int64
		for _, value := range accountMeans {
			accountTotal += value
		}
		mean := int64(math.Round(float64(accountTotal) / float64(len(accountMeans))))
		ref := &WorkBuddyBudgetReference{
			EstimatedTokens: int64Pointer(mean), SampleCount: len(allSamples), AccountCount: len(accountMeans),
			Basis: "other_accounts_3_utc_days_weight_6_3_1",
		}
		setBudgetSampleRange(&ref.SampleMinTokens, &ref.SampleMaxTokens, &ref.SampleFrom, &ref.SampleTo, allSamples)
		row.Reference = ref
	}
}

func recentBudgetSamples(samples []budgetWindowSample, asOf time.Time) []budgetWindowSample {
	day := asOf.UTC().Truncate(24 * time.Hour)
	out := make([]budgetWindowSample, 0, len(samples))
	for _, sample := range samples {
		sampleDay := sample.at.UTC().Truncate(24 * time.Hour)
		age := int(day.Sub(sampleDay) / (24 * time.Hour))
		if age >= 0 && age < 3 {
			out = append(out, sample)
		}
	}
	return out
}

func weightedRecentBudgetMean(samples []budgetWindowSample, asOf time.Time) (int64, bool) {
	day := asOf.UTC().Truncate(24 * time.Hour)
	type daySum struct {
		total  float64
		count  int
		weight float64
	}
	days := map[int]*daySum{}
	for _, sample := range samples {
		sampleDay := sample.at.UTC().Truncate(24 * time.Hour)
		age := int(day.Sub(sampleDay) / (24 * time.Hour))
		weight := 0.0
		switch age {
		case 0:
			weight = 6
		case 1:
			weight = 3
		case 2:
			weight = 1
		default:
			continue
		}
		if days[age] == nil {
			days[age] = &daySum{weight: weight}
		}
		days[age].total += float64(sample.tokens)
		days[age].count++
	}
	var weighted, totalWeight float64
	for _, entry := range days {
		weighted += (entry.total / float64(entry.count)) * entry.weight
		totalWeight += entry.weight
	}
	if totalWeight == 0 {
		return 0, false
	}
	return int64(math.Round(weighted / totalWeight)), true
}

func populatedRecentBudgetDays(samples []budgetWindowSample, asOf time.Time) int {
	days := map[int]struct{}{}
	day := asOf.UTC().Truncate(24 * time.Hour)
	for _, sample := range samples {
		age := int(day.Sub(sample.at.UTC().Truncate(24*time.Hour)) / (24 * time.Hour))
		if age >= 0 && age < 3 {
			days[age] = struct{}{}
		}
	}
	return len(days)
}

func setBudgetSampleRange(minimum, maximum **int64, from, to **time.Time, samples []budgetWindowSample) {
	if len(samples) == 0 {
		return
	}
	sorted := append([]budgetWindowSample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].at.Before(sorted[j].at) })
	minTokens, maxTokens := sorted[0].tokens, sorted[0].tokens
	for _, sample := range sorted[1:] {
		if sample.tokens < minTokens {
			minTokens = sample.tokens
		}
		if sample.tokens > maxTokens {
			maxTokens = sample.tokens
		}
	}
	*minimum = int64Pointer(minTokens)
	*maximum = int64Pointer(maxTokens)
	*from = timePointer(sorted[0].at.UTC())
	*to = timePointer(sorted[len(sorted)-1].at.UTC())
}

func budgetAccountGroupKey(identity [5]string) string {
	return strings.Join(identity[:], "\x00")
}

func int64Pointer(value int64) *int64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }
