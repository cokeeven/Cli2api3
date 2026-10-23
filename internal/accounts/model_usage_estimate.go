package accounts

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	workBuddyLimitCode                   = "6004"
	budgetStatisticalTimezone            = "UTC"
	budgetRecentWeightRule               = "3_utc_days_6_3_1"
	maxWeakReferencesPerRow              = 16
	maxWeakReferencePathsPerRow          = 64
	maxLowConfidenceReferencesPerRow     = 16
	maxLowConfidenceReferencePathsPerRow = 64
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
	identity            [5]string // account, region, actual model, known plan, public request path
	items               []ModelBudgetObservation
	samples             []budgetWindowSample
	latest              budgetAnchor
	firstLimit24hSample *WorkBuddyBudgetSample
}

type budgetUsageCoverage struct {
	tokens  int64
	seen    int
	known   int
	missing int
}

type budgetReferenceSample struct {
	accountID string
	budgetWindowSample
}

type budgetReferenceAccount struct {
	samples []budgetWindowSample
	mean    int64
}

type budgetReferenceCohort struct {
	accounts     map[string]budgetReferenceAccount
	meanTotal    float64
	accountCount int
	sampleCount  int
	byTokens     []budgetReferenceSample
	byTime       []budgetReferenceSample
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
	return EstimateWorkBuddyBudgetsForTargetsInRange(observations, nil, asOf, observationFrom, truncated)
}

// EstimateWorkBuddyBudgetsForTargetsInRange also returns bounded catalog
// account/model pairs that have no local attempt history. Such rows can show
// references, but unknown usage and upstream limits remain nil.
func EstimateWorkBuddyBudgetsForTargetsInRange(observations []ModelBudgetObservation, targets []WorkBuddyBudgetTarget, asOf, observationFrom time.Time, truncated bool) []WorkBuddyBudgetEstimate {
	if asOf.IsZero() {
		return nil
	}
	asOf = asOf.UTC()
	if !observationFrom.IsZero() {
		observationFrom = observationFrom.UTC()
	}
	groups := make(map[string]*budgetGroup)
	accountModelGroups := make(map[string][]ModelBudgetObservation)
	observedAccountModels := make(map[string]struct{})
	for _, item := range observations {
		if item.At.IsZero() || item.AccountID == "" || item.Region == "" || item.Model == "" || item.At.After(asOf) {
			continue
		}
		item.Region = strings.ToLower(strings.TrimSpace(item.Region))
		item.Model = strings.TrimSpace(item.Model)
		item.Plan = strings.ToLower(strings.TrimSpace(item.Plan))
		item.RequestPath = strings.TrimSpace(item.RequestPath)
		identity := [5]string{item.AccountID, item.Region, item.Model, item.Plan, item.RequestPath}
		accountModelKey := budgetAccountModelKey(item.AccountID, item.Region, item.Model)
		accountModelGroups[accountModelKey] = append(accountModelGroups[accountModelKey], item)
		observedAccountModels[accountModelKey] = struct{}{}
		key := budgetAccountGroupKey(identity)
		group := groups[key]
		if group == nil {
			group = &budgetGroup{identity: identity}
			groups[key] = group
		}
		group.items = append(group.items, item)
	}
	for _, target := range targets {
		target.AccountID = strings.TrimSpace(target.AccountID)
		target.Region = strings.ToLower(strings.TrimSpace(target.Region))
		target.Model = strings.TrimSpace(target.Model)
		target.Plan = strings.ToLower(strings.TrimSpace(target.Plan))
		target.RequestPath = strings.TrimSpace(target.RequestPath)
		if target.AccountID == "" || target.Region == "" || target.Model == "" {
			continue
		}
		accountModelKey := budgetAccountModelKey(target.AccountID, target.Region, target.Model)
		if _, exists := observedAccountModels[accountModelKey]; exists {
			continue
		}
		identity := [5]string{target.AccountID, target.Region, target.Model, target.Plan, target.RequestPath}
		key := budgetAccountGroupKey(identity)
		if groups[key] == nil {
			groups[key] = &budgetGroup{identity: identity}
		}
		observedAccountModels[accountModelKey] = struct{}{}
	}
	if len(groups) == 0 {
		return nil
	}

	rows := make([]WorkBuddyBudgetEstimate, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.items, func(i, j int) bool { return group.items[i].At.Before(group.items[j].At) })
		anchors := explicitBudgetAnchors(group.items)
		if len(anchors) > 0 {
			group.latest = anchors[len(anchors)-1]
		}
		for index := 1; index < len(anchors); index++ {
			start, end := anchors[index-1].reset, anchors[index].at
			if start.IsZero() || !start.Before(end) || (!observationFrom.IsZero() && start.Before(observationFrom)) {
				continue
			}
			coverage := sumBudgetUsageWindow(group.items, start, end)
			if hasBudgetIdentityDrift(accountModelGroups[budgetAccountModelKey(group.identity[0], group.identity[1], group.identity[2])], start, end, group.identity[3], group.identity[4]) {
				continue
			}
			if coverage.seen > 0 && coverage.missing == 0 {
				group.samples = append(group.samples, budgetWindowSample{at: end.UTC(), tokens: coverage.tokens})
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
			SampleFreshness: freshness, EstimatedAt: timePointer(asOf), ObservationTruncated: truncated,
			Basis: "no_verified_reset_anchor",
		}
		if len(anchors) > 0 {
			row.Basis = "insufficient_recent_windows"
		}
		if truncated {
			row.Basis = "truncated_observations"
			row.SampleFreshness = "truncated"
		}
		row.SampleCount = len(recent)
		row.StatisticalDays = populatedRecentBudgetDays(recent, asOf)
		setBudgetSampleRange(&row.SampleMinTokens, &row.SampleMaxTokens, &row.SampleFrom, &row.SampleTo, displayed)

		if len(anchors) == 0 {
			if len(group.items) == 0 {
				row.CoverageState = "no_observations"
			} else {
				row.CoverageState = "no_reset_anchor"
			}
		} else {
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
				coverage := sumBudgetUsageWindow(group.items, latest.reset, asOf.Add(time.Nanosecond))
				row.MissingAttempts = coverage.missing
				row.CoverageComplete = coverage.seen > 0 && coverage.missing == 0
				if row.CoverageComplete {
					row.ObservedSinceReset = int64Pointer(coverage.tokens)
					row.CoverageState = "complete"
				} else if coverage.missing > 0 {
					row.CoverageState = "usage_missing"
				} else {
					row.CoverageState = "no_billable_usage_observed"
				}
			}
		}
		setObserved24h(&row, group.items, asOf, observationFrom, truncated)
		if len(anchors) == 1 {
			group.firstLimit24hSample = firstLimit24hSample(group.items, group.latest.at, observationFrom, truncated)
			row.FirstLimit24hSample = group.firstLimit24hSample
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
		} else if !truncated && row.RequestPath == "" && len(anchors) > 0 {
			row.Basis = "unknown_request_path"
		} else if !truncated && len(group.samples) > 0 && len(recent) == 0 {
			row.Basis = "stale_samples"
		}
		rows = append(rows, row)
	}

	if !truncated {
		attachWorkBuddyBudgetReferences(rows, groups, asOf)
	}
	attachFirstLimitBudgetReferences(rows, groups)
	for index := range rows {
		setEstimated24hRemaining(&rows[index])
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Region != rows[j].Region {
			return rows[i].Region < rows[j].Region
		}
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		if rows[i].AccountID != rows[j].AccountID {
			return rows[i].AccountID < rows[j].AccountID
		}
		if rows[i].Plan != rows[j].Plan {
			return rows[i].Plan < rows[j].Plan
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

func sumBudgetUsageWindow(items []ModelBudgetObservation, start, end time.Time) budgetUsageCoverage {
	coverage := budgetUsageCoverage{}
	for _, item := range items {
		if item.At.Before(start) || !item.At.Before(end) || item.Status == AttemptStatusStarted || item.UsageState == "non_billable" {
			continue
		}
		coverage.seen++
		if item.UsageState != "actual" || item.PromptTokens == nil || item.CompletionTokens == nil {
			coverage.missing++
			continue
		}
		coverage.known++
		coverage.tokens += int64(*item.PromptTokens) + int64(*item.CompletionTokens)
	}
	return coverage
}

func hasBudgetIdentityDrift(items []ModelBudgetObservation, start, end time.Time, plan, requestPath string) bool {
	for _, item := range items {
		if item.At.Before(start) || !item.At.Before(end) {
			continue
		}
		if strings.ToLower(strings.TrimSpace(item.Plan)) != plan || strings.TrimSpace(item.RequestPath) != requestPath {
			return true
		}
	}
	return false
}

func setObserved24h(row *WorkBuddyBudgetEstimate, items []ModelBudgetObservation, asOf, observationFrom time.Time, truncated bool) {
	from := asOf.Add(-24 * time.Hour)
	to := asOf.Add(time.Nanosecond)
	coverage := sumBudgetUsageWindow(items, from, to)
	row.Observed24hMissing = coverage.missing
	if coverage.known > 0 {
		row.Observed24hTokens = int64Pointer(coverage.tokens)
		row.Observed24hFrom = timePointer(from)
		row.Observed24hTo = timePointer(asOf)
	}
	switch {
	case coverage.seen == 0:
		row.Observed24hCoverage = "no_observed_attempts"
	case truncated:
		row.Observed24hCoverage = "truncated_lower_bound"
	case !observationFrom.IsZero() && from.Before(observationFrom):
		row.Observed24hCoverage = "range_boundary_lower_bound"
	case coverage.missing > 0:
		row.Observed24hCoverage = "missing_usage_lower_bound"
	case coverage.known > 0:
		row.Observed24hCoverage = "complete"
	default:
		row.Observed24hCoverage = "usage_unknown"
	}
}

func firstLimit24hSample(items []ModelBudgetObservation, limitAt, observationFrom time.Time, truncated bool) *WorkBuddyBudgetSample {
	from := limitAt.Add(-24 * time.Hour)
	coverage := sumBudgetUsageWindow(items, from, limitAt)
	if coverage.known == 0 {
		return nil
	}
	state := "complete"
	switch {
	case truncated:
		state = "truncated_lower_bound"
	case !observationFrom.IsZero() && from.Before(observationFrom):
		state = "range_boundary_lower_bound"
	case coverage.missing > 0:
		state = "missing_usage_lower_bound"
	case coverage.known == 0:
		state = "usage_unknown"
	}
	return &WorkBuddyBudgetSample{
		Tokens: int64Pointer(coverage.tokens), From: timePointer(from), To: timePointer(limitAt), MissingAttempts: coverage.missing,
		Coverage: state, Confidence: "low", Source: "first_6004_in_observation_range_24h_actual_usage",
	}
}

func buildBudgetReferenceCohorts(groups map[string]*budgetGroup, asOf time.Time) (map[[4]string]*budgetReferenceCohort, map[[3]string]*budgetReferenceCohort) {
	strong := make(map[[4]string]*budgetReferenceCohort)
	weak := make(map[[3]string]*budgetReferenceCohort)
	for _, group := range groups {
		recent := recentBudgetSamples(group.samples, asOf)
		if len(recent) == 0 {
			continue
		}
		var strongKey [4]string
		var weakKey [3]string
		var cohort *budgetReferenceCohort
		if group.identity[3] != "" && group.identity[4] != "" {
			strongKey = [4]string{group.identity[1], group.identity[2], group.identity[3], group.identity[4]}
			cohort = strong[strongKey]
			if cohort == nil {
				cohort = &budgetReferenceCohort{accounts: make(map[string]budgetReferenceAccount)}
				strong[strongKey] = cohort
			}
		}
		if group.identity[3] == "" {
			weakKey = [3]string{group.identity[1], group.identity[2], group.identity[4]}
			cohort = weak[weakKey]
			if cohort == nil {
				cohort = &budgetReferenceCohort{accounts: make(map[string]budgetReferenceAccount)}
				weak[weakKey] = cohort
			}
		}
		if cohort == nil {
			continue
		}
		mean, ok := weightedRecentBudgetMean(recent, asOf)
		if !ok {
			continue
		}
		accountID := group.identity[0]
		cohort.accounts[accountID] = budgetReferenceAccount{samples: recent, mean: mean}
		cohort.meanTotal += float64(mean)
		cohort.accountCount++
		cohort.sampleCount += len(recent)
		for _, sample := range recent {
			entry := budgetReferenceSample{accountID: accountID, budgetWindowSample: sample}
			cohort.byTokens = append(cohort.byTokens, entry)
			cohort.byTime = append(cohort.byTime, entry)
		}
	}
	for _, cohort := range strong {
		finalizeBudgetReferenceCohort(cohort)
	}
	for _, cohort := range weak {
		finalizeBudgetReferenceCohort(cohort)
	}
	return strong, weak
}

func finalizeBudgetReferenceCohort(cohort *budgetReferenceCohort) {
	sort.Slice(cohort.byTokens, func(i, j int) bool {
		if cohort.byTokens[i].tokens == cohort.byTokens[j].tokens {
			return cohort.byTokens[i].accountID < cohort.byTokens[j].accountID
		}
		return cohort.byTokens[i].tokens < cohort.byTokens[j].tokens
	})
	sort.Slice(cohort.byTime, func(i, j int) bool {
		if cohort.byTime[i].at.Equal(cohort.byTime[j].at) {
			return cohort.byTime[i].accountID < cohort.byTime[j].accountID
		}
		return cohort.byTime[i].at.Before(cohort.byTime[j].at)
	})
}

func budgetReferenceFor(cohort *budgetReferenceCohort, excludedAccount string, plan, requestPath, scope string) *WorkBuddyBudgetReference {
	if cohort == nil {
		return nil
	}
	accountCount := cohort.accountCount
	sampleCount := cohort.sampleCount
	meanTotal := cohort.meanTotal
	if own, exists := cohort.accounts[excludedAccount]; exists {
		accountCount--
		sampleCount -= len(own.samples)
		meanTotal -= float64(own.mean)
	}
	if accountCount <= 0 || sampleCount <= 0 {
		return nil
	}
	ref := &WorkBuddyBudgetReference{
		EstimatedTokens: int64Pointer(int64(math.Round(meanTotal / float64(accountCount)))),
		SampleCount:     sampleCount, AccountCount: accountCount, Plan: plan, RequestPath: requestPath,
		Scope: scope, Basis: "other_accounts_3_utc_days_weight_6_3_1",
	}
	if sampleCount == 1 {
		ref.Confidence = "single_complete_window"
	} else {
		ref.Confidence = "repeated_complete_windows"
	}
	for _, sample := range cohort.byTokens {
		if sample.accountID != excludedAccount {
			ref.SampleMinTokens = int64Pointer(sample.tokens)
			break
		}
	}
	for i := len(cohort.byTokens) - 1; i >= 0; i-- {
		if cohort.byTokens[i].accountID != excludedAccount {
			ref.SampleMaxTokens = int64Pointer(cohort.byTokens[i].tokens)
			break
		}
	}
	for _, sample := range cohort.byTime {
		if sample.accountID != excludedAccount {
			ref.SampleFrom = timePointer(sample.at.UTC())
			break
		}
	}
	for i := len(cohort.byTime) - 1; i >= 0; i-- {
		if cohort.byTime[i].accountID != excludedAccount {
			ref.SampleTo = timePointer(cohort.byTime[i].at.UTC())
			break
		}
	}
	return ref
}

func attachWorkBuddyBudgetReferences(rows []WorkBuddyBudgetEstimate, groups map[string]*budgetGroup, asOf time.Time) {
	strong, weak := buildBudgetReferenceCohorts(groups, asOf)
	weakByModel := make(map[[2]string][][3]string)
	for key := range weak {
		modelKey := [2]string{key[0], key[1]}
		weakByModel[modelKey] = append(weakByModel[modelKey], key)
	}
	for modelKey := range weakByModel {
		sort.Slice(weakByModel[modelKey], func(i, j int) bool { return weakByModel[modelKey][i][2] < weakByModel[modelKey][j][2] })
	}
	for i := range rows {
		row := &rows[i]
		if row.Plan != "" && row.RequestPath != "" {
			key := [4]string{row.Region, row.Model, row.Plan, row.RequestPath}
			row.Reference = budgetReferenceFor(strong[key], row.AccountID, row.Plan, row.RequestPath, "same_known_plan_and_request_path")
		}
		weakKeys := weakByModel[[2]string{row.Region, row.Model}]
		seenPaths := 0
		for _, key := range weakKeys {
			if row.RequestPath != "" && key[2] != row.RequestPath {
				continue
			}
			if row.RequestPath == "" {
				seenPaths++
				if seenPaths > maxWeakReferencePathsPerRow {
					row.WeakReferencesTruncated = true
					break
				}
			}
			ref := budgetReferenceFor(weak[key], row.AccountID, "", key[2], "same_region_model_plan_unverified")
			if ref != nil {
				ref.Basis = "unknown_plan_same_region_model_path_separated"
				row.WeakReferences = append(row.WeakReferences, *ref)
				if len(row.WeakReferences) >= maxWeakReferencesPerRow && row.RequestPath == "" {
					row.WeakReferencesTruncated = len(weakKeys) > seenPaths
					break
				}
			}
		}
	}
}

func attachFirstLimitBudgetReferences(rows []WorkBuddyBudgetEstimate, groups map[string]*budgetGroup) {
	byModelPlan := make(map[[3]string]map[string][]WorkBuddyBudgetSampleReference)
	for _, group := range groups {
		if group.firstLimit24hSample == nil || group.firstLimit24hSample.Tokens == nil {
			continue
		}
		key := [3]string{group.identity[1], group.identity[2], group.identity[3]}
		if byModelPlan[key] == nil {
			byModelPlan[key] = make(map[string][]WorkBuddyBudgetSampleReference)
		}
		ref := WorkBuddyBudgetSampleReference{
			AccountID: group.identity[0], Plan: group.identity[3], RequestPath: group.identity[4],
			Scope: firstLimitSampleScope(group.identity[3], group.identity[4]), Sample: *group.firstLimit24hSample,
		}
		byModelPlan[key][group.identity[4]] = append(byModelPlan[key][group.identity[4]], ref)
	}
	pathIndex := make(map[[3]string][]string, len(byModelPlan))
	for key, paths := range byModelPlan {
		pathIndex[key] = make([]string, 0, len(paths))
		for path := range paths {
			pathIndex[key] = append(pathIndex[key], path)
			sort.Slice(paths[path], func(i, j int) bool {
				left, right := paths[path][i], paths[path][j]
				if left.Sample.To != nil && right.Sample.To != nil && !left.Sample.To.Equal(*right.Sample.To) {
					return left.Sample.To.After(*right.Sample.To)
				}
				return left.AccountID < right.AccountID
			})
		}
		sort.Strings(pathIndex[key])
	}

	for i := range rows {
		row := &rows[i]
		planKeys := []string{""}
		if row.Plan != "" {
			planKeys = []string{row.Plan, ""}
		}
		seenPaths := 0
		candidateCount := 0
		for _, plan := range planKeys {
			modelPlanKey := [3]string{row.Region, row.Model, plan}
			pathMap := byModelPlan[modelPlanKey]
			pathKeys := pathIndex[modelPlanKey]
			if row.RequestPath != "" {
				if _, exists := pathMap[row.RequestPath]; exists {
					pathKeys = []string{row.RequestPath}
				} else {
					pathKeys = nil
				}
			}
			for _, path := range pathKeys {
				if row.RequestPath == "" {
					seenPaths++
					if seenPaths > maxLowConfidenceReferencePathsPerRow {
						row.LowConfidenceReferencesTruncated = true
						break
					}
				}
				for _, ref := range pathMap[path] {
					candidateCount++
					if candidateCount > maxLowConfidenceReferencesPerRow*4 {
						row.LowConfidenceReferencesTruncated = true
						break
					}
					if ref.AccountID == row.AccountID {
						continue
					}
					row.LowConfidenceReferences = append(row.LowConfidenceReferences, ref)
					if len(row.LowConfidenceReferences) >= maxLowConfidenceReferencesPerRow {
						row.LowConfidenceReferencesTruncated = true
						break
					}
				}
				if candidateCount > maxLowConfidenceReferencesPerRow*4 || len(row.LowConfidenceReferences) >= maxLowConfidenceReferencesPerRow {
					break
				}
			}
		}
		if candidateCount > maxLowConfidenceReferencesPerRow*4 || len(row.LowConfidenceReferences) >= maxLowConfidenceReferencesPerRow {
			break
		}
	}
}

func firstLimitSampleScope(plan, requestPath string) string {
	switch {
	case plan == "" && requestPath == "":
		return "plan_and_request_path_unverified"
	case plan == "":
		return "plan_unverified_path_separated"
	case requestPath == "":
		return "request_path_unverified"
	default:
		return "known_plan_and_request_path"
	}
}

func setEstimated24hRemaining(row *WorkBuddyBudgetEstimate) {
	if row == nil || row.ObservationTruncated {
		return
	}
	var budget *int64
	var basis string
	switch {
	case row.EstimatedTokens != nil:
		budget = row.EstimatedTokens
		basis = "own_complete_reset_window_reference"
	case row.Reference != nil:
		budget = row.Reference.EstimatedTokens
		basis = "same_known_plan_and_request_path_reference"
	case len(row.WeakReferences) == 1:
		budget = row.WeakReferences[0].EstimatedTokens
		basis = "unverified_plan_same_region_model_reference"
	default:
		return
	}
	if budget == nil {
		return
	}
	row.Estimated24hBudgetTokens = int64Pointer(*budget)
	row.Estimated24hBasis = basis
	if row.Observed24hCoverage != "complete" || row.Observed24hTokens == nil {
		return
	}
	remaining := *budget - *row.Observed24hTokens
	if remaining < 0 {
		remaining = 0
	}
	row.Estimated24hRemaining = int64Pointer(remaining)
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

func budgetAccountModelKey(accountID, region, model string) string {
	return strings.Join([]string{accountID, region, model}, "\x00")
}

func int64Pointer(value int64) *int64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }
