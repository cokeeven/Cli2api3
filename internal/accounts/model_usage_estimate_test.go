package accounts

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWorkBuddyBudgetRetainsOriginal24hReferenceAfterOverrun(t *testing.T) {
	tests := []struct {
		name string
		row  WorkBuddyBudgetEstimate
		want int64
	}{
		{
			name: "own complete window",
			row:  WorkBuddyBudgetEstimate{EstimatedTokens: int64Pointer(100)},
			want: 100,
		},
		{
			name: "strong peer reference",
			row: WorkBuddyBudgetEstimate{
				Reference: &WorkBuddyBudgetReference{EstimatedTokens: int64Pointer(120)},
			},
			want: 120,
		},
		{
			name: "single weak reference",
			row: WorkBuddyBudgetEstimate{
				WeakReferences: []WorkBuddyBudgetReference{{EstimatedTokens: int64Pointer(80)}},
			},
			want: 80,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.Observed24hCoverage = "complete"
			tc.row.Observed24hTokens = int64Pointer(150)
			setEstimated24hRemaining(&tc.row)
			wire, err := json.Marshal(tc.row)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatal(err)
			}
			var budget, remaining *int64
			if value, ok := fields["estimated_24h_budget_tokens"]; ok {
				if err := json.Unmarshal(value, &budget); err != nil {
					t.Fatal(err)
				}
			}
			if value, ok := fields["estimated_24h_remaining"]; ok {
				if err := json.Unmarshal(value, &remaining); err != nil {
					t.Fatal(err)
				}
			}
			if budget == nil || *budget != tc.want {
				t.Fatalf("original 24h reference was lost: want=%d JSON=%s", tc.want, wire)
			}
			if remaining == nil || *remaining != 0 {
				t.Fatalf("overrun must retain zero estimated remaining without rewriting the reference: JSON=%s", wire)
			}
		})
	}
}

func TestWorkBuddyBudgetEstimateIncludesCalculationTime(t *testing.T) {
	asOf := time.Date(2026, 10, 23, 8, 30, 0, 0, time.UTC)
	rows := EstimateWorkBuddyBudgetsForTargetsInRange(
		nil,
		[]WorkBuddyBudgetTarget{{AccountID: "wb-synthetic", Region: "cn", Model: "glm-5.3"}},
		asOf,
		asOf.Add(-24*time.Hour),
		false,
	)
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	wire, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	var estimatedAt *time.Time
	if value, ok := fields["estimated_at"]; ok {
		if err := json.Unmarshal(value, &estimatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if estimatedAt == nil || !estimatedAt.Equal(asOf) {
		t.Fatalf("estimated_at must identify the calculation time %s; JSON=%s", asOf, wire)
	}
}

func TestWorkBuddyBudgetRequiresDistinctComplete6004ResetWindows(t *testing.T) {
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	reset1, reset2, reset3 := base.Add(time.Hour), base.Add(3*time.Hour), base.Add(5*time.Hour)
	actual := func(at time.Time, prompt, completion int64) ModelBudgetObservation {
		return ModelBudgetObservation{
			At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &prompt, CompletionTokens: &completion,
		}
	}
	limit := func(at, reset time.Time) ModelBudgetObservation {
		return ModelBudgetObservation{
			At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: AttemptStatusFailover, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model",
			UpstreamResetAt: &reset, UsageState: "unknown",
		}
	}
	observations := []ModelBudgetObservation{
		limit(base, reset1),
		actual(reset1.Add(time.Minute), 80, 20),
		limit(base.Add(2*time.Hour), reset2),
		// Repeated failures with the same upstream reset are one anchor.
		limit(base.Add(2*time.Hour+time.Minute), reset2),
		actual(reset2.Add(time.Minute), 240, 60),
		limit(base.Add(4*time.Hour), reset3),
		actual(reset3.Add(time.Minute), 40, 10),
	}
	got := EstimateWorkBuddyBudgets(observations, base.Add(6*time.Hour))
	if len(got) != 1 {
		t.Fatalf("estimates = %+v", got)
	}
	row := got[0]
	if row.SampleCount != 2 || row.SampleMinTokens == nil || *row.SampleMinTokens != 100 || row.SampleMaxTokens == nil || *row.SampleMaxTokens != 300 {
		t.Fatalf("sample range = %+v", row)
	}
	if row.EstimatedTokens == nil || *row.EstimatedTokens != 200 || row.ObservedSinceReset == nil || *row.ObservedSinceReset != 50 || row.EstimatedRemaining == nil || *row.EstimatedRemaining != 150 {
		t.Fatalf("estimate = %+v", row)
	}
	if row.Basis != "explicit_6004_reset_windows_weighted_recent" || !row.CoverageComplete || row.Cooling {
		t.Fatalf("basis/state = %+v", row)
	}
}

func TestWorkBuddyBudgetDoesNotAssumeWindowOrMergeAccountsAndPlans(t *testing.T) {
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	reset1, reset2, reset3 := base.Add(time.Hour), base.Add(3*time.Hour), base.Add(5*time.Hour)
	makeGroup := func(account, plan string, missing bool) []ModelBudgetObservation {
		makeLimit := func(at, reset time.Time) ModelBudgetObservation {
			return ModelBudgetObservation{At: at, AccountID: account, Region: "cn", Model: "glm-5.3", Plan: plan, RequestPath: "openai_chat_completions",
				Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset}
		}
		items := []ModelBudgetObservation{makeLimit(base, reset1)}
		for i, reset := range []time.Time{reset1, reset2} {
			at := reset.Add(time.Minute)
			if missing && i == 0 {
				items = append(items, ModelBudgetObservation{At: at, AccountID: account, Region: "cn", Model: "glm-5.3", Plan: plan, RequestPath: "openai_chat_completions", Status: AttemptStatusError, UsageState: "unknown"})
			} else {
				prompt, completion := int64(20), int64(0)
				items = append(items, ModelBudgetObservation{At: at, AccountID: account, Region: "cn", Model: "glm-5.3", Plan: plan, RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &prompt, CompletionTokens: &completion})
			}
			items = append(items, makeLimit(base.Add(time.Duration(i*2+2)*time.Hour), []time.Time{reset2, reset3}[i]))
		}
		return items
	}
	items := append(makeGroup("account-a", "pro", false), makeGroup("account-b", "", false)...)
	items = append(items, makeGroup("account-c", "pro", true)...)
	// A malformed 6004 without a reset is never an anchor; the estimator has
	// no fixed 24-hour fallback.
	items = append(items, ModelBudgetObservation{At: base, AccountID: "account-d", Region: "cn", Model: "glm-5.3", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model"})
	got := EstimateWorkBuddyBudgets(items, base.Add(6*time.Hour))
	if len(got) != 4 {
		t.Fatalf("estimates = %+v", got)
	}
	for _, row := range got {
		if row.AccountID == "account-c" && (row.SampleCount != 1 || row.EstimatedTokens != nil || row.EstimatedRemaining != nil) {
			t.Fatalf("missing usage produced an estimate: %+v", row)
		}
		if row.AccountID == "account-a" && row.Plan != "pro" {
			t.Fatalf("plan cohort merged: %+v", row)
		}
		if row.AccountID == "account-b" && row.Plan != "" {
			t.Fatalf("unknown plan was fabricated: %+v", row)
		}
		if row.AccountID == "account-d" && (row.EstimatedTokens != nil || row.EstimatedRemaining != nil || row.Basis != "no_verified_reset_anchor") {
			t.Fatalf("a malformed 6004 without reset must remain visible without creating a budget: %+v", row)
		}
	}
}

func TestWorkBuddyBudgetIgnoresNonBillableRefusalsButKeepsCanceledActualUsage(t *testing.T) {
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	reset1, reset2, reset3 := base.Add(time.Hour), base.Add(3*time.Hour), base.Add(5*time.Hour)
	limit := func(at, reset time.Time) ModelBudgetObservation {
		return ModelBudgetObservation{
			At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: AttemptStatusFailover, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset,
		}
	}
	actual := func(at time.Time, status string, prompt, completion int64) ModelBudgetObservation {
		return ModelBudgetObservation{At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: status, UsageState: "actual", PromptTokens: &prompt, CompletionTokens: &completion}
	}
	observations := []ModelBudgetObservation{
		limit(base, reset1),
		actual(reset1.Add(time.Minute), AttemptStatusOK, 90, 10),
		actual(reset1.Add(2*time.Minute), AttemptStatusCanceled, 5, 5),
		{At: reset1.Add(3 * time.Minute), AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusError, UsageState: "non_billable"},
		limit(base.Add(2*time.Hour), reset2),
		actual(reset2.Add(time.Minute), AttemptStatusOK, 40, 10),
		limit(base.Add(4*time.Hour), reset3),
	}
	got := EstimateWorkBuddyBudgets(observations, base.Add(6*time.Hour))
	if len(got) != 1 {
		t.Fatalf("estimates = %+v", got)
	}
	row := got[0]
	if row.SampleCount != 2 || row.SampleMinTokens == nil || *row.SampleMinTokens != 50 || row.SampleMaxTokens == nil || *row.SampleMaxTokens != 110 {
		t.Fatalf("non-billable refusal damaged coverage or canceled actual usage was lost: %+v", row)
	}
	if row.EstimatedTokens == nil || *row.EstimatedTokens != 80 {
		t.Fatalf("complete-window mean = %+v, want 80", row)
	}
}

func TestWorkBuddyBudgetRecentSamplesUseThreeUTCdaysWithSixThreeOneWeights(t *testing.T) {
	asOf := time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC)
	today := asOf.Truncate(24 * time.Hour)
	limit := func(at, reset time.Time) ModelBudgetObservation {
		return ModelBudgetObservation{At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset}
	}
	actual := func(at time.Time, tokens int64) ModelBudgetObservation {
		return ModelBudgetObservation{At: at, AccountID: "wb-a", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
			Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &tokens, CompletionTokens: int64Pointer(0)}
	}
	anchors := []budgetAnchor{
		{at: today.Add(-48 * time.Hour), reset: today.Add(-47 * time.Hour)},
		{at: today.Add(-36 * time.Hour), reset: today.Add(-23 * time.Hour)},
		{at: today.Add(-12 * time.Hour), reset: today.Add(time.Hour)},
		{at: today.Add(12 * time.Hour), reset: today.Add(25 * time.Hour)},
	}
	observations := make([]ModelBudgetObservation, 0, 7)
	for _, anchor := range anchors {
		observations = append(observations, limit(anchor.at, anchor.reset))
	}
	observations = append(observations,
		actual(anchors[0].reset.Add(time.Minute), 100),
		actual(anchors[1].reset.Add(time.Minute), 300),
		actual(anchors[2].reset.Add(time.Minute), 500),
	)
	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, today.Add(-7*24*time.Hour), false)
	if len(got) != 1 {
		t.Fatalf("estimates = %+v", got)
	}
	row := got[0]
	if row.CompleteWindows != 3 || row.SampleCount != 3 || row.StatisticalDays != 3 {
		t.Fatalf("sample counts = %+v", row)
	}
	if row.EstimatedTokens == nil || *row.EstimatedTokens != 400 {
		t.Fatalf("6/3/1 weighted estimate = %+v, want 400", row.EstimatedTokens)
	}
	if row.SampleMinTokens == nil || *row.SampleMinTokens != 100 || row.SampleMaxTokens == nil || *row.SampleMaxTokens != 500 || row.StatisticalTimezone != "UTC" || row.WeightRule != budgetRecentWeightRule {
		t.Fatalf("sample metadata = %+v", row)
	}
	if row.SampleFrom == nil || !row.SampleFrom.Equal(anchors[1].at) || row.SampleTo == nil || !row.SampleTo.Equal(anchors[3].at) {
		t.Fatalf("sample date range = %v..%v", row.SampleFrom, row.SampleTo)
	}
	if !row.Restricted || row.RestrictionSource != "upstream_6004_explicit_reset" || row.ResetAt == nil || !row.ResetAt.Equal(anchors[3].reset) || row.EstimatedRemaining != nil {
		t.Fatalf("active restriction must remain separate from unknown remaining = %+v", row)
	}
}

func TestWorkBuddyBudgetWeightNormalizesOnlyPopulatedDays(t *testing.T) {
	asOf := time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC)
	today := asOf.Truncate(24 * time.Hour)
	got, ok := weightedRecentBudgetMean([]budgetWindowSample{
		{at: today.Add(-48 * time.Hour), tokens: 100}, // weight 1
		{at: today.Add(12 * time.Hour), tokens: 500},  // weight 6
	}, asOf)
	if !ok || got != 443 { // round((100*1 + 500*6) / (1+6)); no absent-day weight
		t.Fatalf("normalized two-day estimate = %d, ok=%v; want 443", got, ok)
	}
}

func TestWorkBuddyBudgetReferenceRequiresMatchingKnownPlanAndPathAndBalancesAccounts(t *testing.T) {
	base := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	asOf := base.Add(20 * time.Hour)
	observations := appendBudgetSamples(nil, "account-a", "pro", "openai_chat_completions", base, []int64{100, 100, 100, 100})
	observations = append(observations, appendBudgetSamples(nil, "account-b", "pro", "openai_chat_completions", base, []int64{300})...)
	observations = append(observations, appendBudgetSamples(nil, "account-c", "pro", "openai_chat_completions", base, []int64{50})...)
	observations = append(observations, appendBudgetSamples(nil, "unknown-plan", "", "openai_chat_completions", base, []int64{900})...)
	observations = append(observations, appendBudgetSamples(nil, "other-path", "pro", "openai_responses", base, []int64{800})...)
	observations = append(observations, appendBudgetSamples(nil, "other-plan", "enterprise", "openai_chat_completions", base, []int64{700})...)
	observations = append(observations, appendBudgetSamples(nil, "unknown-path", "pro", "", base, []int64{600, 600})...)

	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, base.Add(-time.Minute), false)
	rows := make(map[string]WorkBuddyBudgetEstimate, len(got))
	for _, row := range got {
		rows[row.AccountID] = row
	}
	target := rows["account-c"]
	if target.Reference == nil || target.Reference.AccountCount != 2 || target.Reference.SampleCount != 5 || target.Reference.EstimatedTokens == nil || *target.Reference.EstimatedTokens != 200 {
		t.Fatalf("same-condition reference must average account means (100 and 300), not sample counts: %+v", target.Reference)
	}
	if target.Reference.SampleMinTokens == nil || *target.Reference.SampleMinTokens != 100 || target.Reference.SampleMaxTokens == nil || *target.Reference.SampleMaxTokens != 300 {
		t.Fatalf("reference window range = %+v", target.Reference)
	}
	if rows["unknown-plan"].Reference != nil {
		t.Fatalf("unknown plan must not join a cross-account cohort: %+v", rows["unknown-plan"])
	}
	unknownPath := rows["unknown-path"]
	if unknownPath.EstimatedTokens != nil || unknownPath.Reference != nil || unknownPath.Basis != "unknown_request_path" {
		t.Fatalf("unknown request path must not produce a numeric estimate: %+v", unknownPath)
	}
}

func TestWorkBuddyBudgetReferenceIsVisibleWithoutTarget6004(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	asOf := base.Add(5 * time.Hour)
	observations := appendBudgetSamples(nil, "donor", "pro", "openai_chat_completions", base, []int64{300})
	used := int64(42)
	observations = append(observations, ModelBudgetObservation{
		At: base.Add(4 * time.Hour), AccountID: "uncapped-target", Region: "cn", Model: "glm-5.3",
		Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusOK,
		UsageState: "actual", PromptTokens: &used, CompletionTokens: int64Pointer(0),
	})

	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, base.Add(-time.Hour), false)
	var target *WorkBuddyBudgetEstimate
	for index := range got {
		if got[index].AccountID == "uncapped-target" {
			target = &got[index]
			break
		}
	}
	if target == nil {
		t.Fatalf("uncapped account usage row must remain visible: %+v", got)
	}
	if target.Reference == nil || target.Reference.AccountCount != 1 || target.Reference.SampleCount != 1 || target.Reference.EstimatedTokens == nil || *target.Reference.EstimatedTokens != 300 {
		t.Fatalf("one complete donor window must be visible as a peer reference for an account without its own 6004: %+v", target.Reference)
	}
	if target.Reference.Confidence != "single_complete_window" {
		t.Fatalf("a single donor window must be clearly labeled: %+v", target.Reference)
	}
	if target.EstimatedTokens != nil || target.EstimatedRemaining != nil {
		t.Fatalf("peer reference must not become the target account's confirmed budget or remaining amount: %+v", target)
	}
}

func TestWorkBuddyUnknownPlanReferenceIsSeparateAndKeepsPathsApart(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	asOf := base.Add(8 * time.Hour)
	observations := appendBudgetSamples(nil, "unknown-plan-donor", "", "openai_chat_completions", base, []int64{300})
	observations = append(observations, appendBudgetSamples(nil, "other-path-donor", "", "openai_responses", base, []int64{700})...)
	observations = append(observations, appendBudgetSamples(nil, "known-plan-donor", "pro", "openai_chat_completions", base, []int64{900})...)
	used := int64(40)
	observations = append(observations, ModelBudgetObservation{
		At: base.Add(7 * time.Hour), AccountID: "unknown-plan-target", Region: "cn", Model: "glm-5.3",
		RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual",
		PromptTokens: &used, CompletionTokens: int64Pointer(0),
	})

	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, base.Add(-time.Hour), false)
	var target *WorkBuddyBudgetEstimate
	for index := range got {
		if got[index].AccountID == "unknown-plan-target" {
			target = &got[index]
			break
		}
	}
	if target == nil || target.Reference != nil || len(target.WeakReferences) != 1 {
		t.Fatalf("unknown plan must use only its separate weak reference tier: %+v", target)
	}
	weak := target.WeakReferences[0]
	if weak.Scope != "same_region_model_plan_unverified" || weak.Plan != "" || weak.RequestPath != "openai_chat_completions" || weak.EstimatedTokens == nil || *weak.EstimatedTokens != 300 || weak.AccountCount != 1 {
		t.Fatalf("weak reference mixed known plan or another request path: %+v", weak)
	}
}

func TestWorkBuddyFirstLimitUsesSeparateLowConfidence24hSample(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	limitAt := base.Add(25 * time.Hour)
	asOf := limitAt.Add(time.Hour)
	used := int64(123)
	reset := limitAt.Add(4 * time.Hour)
	observations := []ModelBudgetObservation{
		{At: limitAt.Add(-3 * time.Hour), AccountID: "first-limit", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &used, CompletionTokens: int64Pointer(0)},
		{At: limitAt, AccountID: "first-limit", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset},
	}
	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, base, false)
	if len(got) != 1 || got[0].FirstLimit24hSample == nil || got[0].FirstLimit24hSample.Tokens == nil || *got[0].FirstLimit24hSample.Tokens != 123 {
		t.Fatalf("first observed 6004 should expose only a separate 24h low-confidence sample: %+v", got)
	}
	row := got[0]
	if row.FirstLimit24hSample.Confidence != "low" || row.FirstLimit24hSample.Coverage != "complete" || row.EstimatedTokens != nil || row.EstimatedRemaining != nil {
		t.Fatalf("24h observation must not be promoted to a verified quota or remaining value: %+v", row)
	}
}

func TestWorkBuddyFirstLimitSampleIsVisibleToUncappedCatalogModelBySeparatePath(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	limitAt := base.Add(25 * time.Hour)
	asOf := limitAt.Add(time.Hour)
	resetA, resetB := limitAt.Add(4*time.Hour), limitAt.Add(5*time.Hour)
	chatUsage, otherUsage := int64(123), int64(900)
	observations := []ModelBudgetObservation{
		{At: limitAt.Add(-3 * time.Hour), AccountID: "first-limit-chat", Region: "cn", Model: "glm-5.3", RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &chatUsage, CompletionTokens: int64Pointer(0)},
		{At: limitAt, AccountID: "first-limit-chat", Region: "cn", Model: "glm-5.3", RequestPath: "openai_chat_completions", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &resetA},
		{At: limitAt.Add(-2 * time.Hour), AccountID: "first-limit-responses", Region: "cn", Model: "glm-5.3", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &otherUsage, CompletionTokens: int64Pointer(0)},
		{At: limitAt.Add(time.Minute), AccountID: "first-limit-responses", Region: "cn", Model: "glm-5.3", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &resetB},
	}
	targets := []WorkBuddyBudgetTarget{{AccountID: "catalog-uncapped", Region: "cn", Model: "glm-5.3"}}
	got := EstimateWorkBuddyBudgetsForTargetsInRange(observations, targets, asOf, base, false)
	var target *WorkBuddyBudgetEstimate
	for index := range got {
		if got[index].AccountID == "catalog-uncapped" {
			target = &got[index]
			break
		}
	}
	if target == nil || target.EstimatedTokens != nil || target.EstimatedRemaining != nil || len(target.LowConfidenceReferences) != 2 {
		t.Fatalf("catalog model should see first-limit 24h samples separately without promotion to budget: %+v", target)
	}
	if target.LowConfidenceReferences[0].Sample.Tokens == nil || target.LowConfidenceReferences[1].Sample.Tokens == nil || *target.LowConfidenceReferences[0].Sample.Tokens == *target.LowConfidenceReferences[1].Sample.Tokens {
		t.Fatalf("different path samples should remain separate values: %+v", target.LowConfidenceReferences)
	}
}

func TestWorkBuddyIncomplete24hUsageIsLowerBoundAndNeverProducesRemaining(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	asOf := base.Add(30 * time.Hour)
	used := int64(40)
	observations := []ModelBudgetObservation{
		{At: asOf.Add(-20 * time.Hour), AccountID: "partial-usage", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &used, CompletionTokens: int64Pointer(0)},
		{At: asOf.Add(-2 * time.Hour), AccountID: "partial-usage", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusError, UsageState: "unknown"},
	}
	got := EstimateWorkBuddyBudgetsInRange(observations, asOf, base, false)
	if len(got) != 1 {
		t.Fatalf("partial usage row missing: %+v", got)
	}
	row := got[0]
	if row.Observed24hTokens == nil || *row.Observed24hTokens != 40 || row.Observed24hCoverage != "missing_usage_lower_bound" || row.Observed24hMissing != 1 || row.Estimated24hRemaining != nil {
		t.Fatalf("partial 24h usage must be a lower bound without a precise remainder: %+v", row)
	}
}

func TestWorkBuddyCatalogTargetWithNoUsageKeepsUnknownsNull(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	targets := []WorkBuddyBudgetTarget{{AccountID: "catalog-only", Region: "cn", Model: "glm-5.3"}}
	got := EstimateWorkBuddyBudgetsForTargetsInRange(nil, targets, base.Add(time.Hour), base.Add(-24*time.Hour), false)
	if len(got) != 1 {
		t.Fatalf("catalog model must have a visible account row: %+v", got)
	}
	row := got[0]
	if row.Basis != "no_verified_reset_anchor" || row.EstimatedTokens != nil || row.EstimatedRemaining != nil || row.Observed24hTokens != nil || row.Estimated24hRemaining != nil || row.Reference != nil || len(row.WeakReferences) != 0 {
		t.Fatalf("catalog visibility must not fabricate usage or a budget: %+v", row)
	}
}

func TestWorkBuddyBudgetKeepsRestrictionVisibleWhenSamplesAreStaleOrTruncated(t *testing.T) {
	asOf := time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC)
	oldAt := asOf.Add(-4 * 24 * time.Hour)
	oldReset := oldAt.Add(time.Hour)
	oldLimit := oldAt.Add(3 * time.Hour)
	used := int64(90)
	staleItems := []ModelBudgetObservation{
		{At: oldAt, AccountID: "wb-stale", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &oldReset},
		{At: oldReset.Add(time.Minute), AccountID: "wb-stale", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &used, CompletionTokens: int64Pointer(0)},
		{At: oldLimit, AccountID: "wb-stale", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions", Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: timePointer(oldLimit.Add(time.Hour))},
	}
	stale := EstimateWorkBuddyBudgetsInRange(staleItems, asOf, asOf.Add(-7*24*time.Hour), false)
	if len(stale) != 1 || stale[0].SampleFreshness != "stale" || stale[0].SampleCount != 0 || stale[0].CompleteWindows != 1 || stale[0].EstimatedTokens != nil {
		t.Fatalf("stale sample must be shown but not treated as current estimate: %+v", stale)
	}

	reset := asOf.Add(time.Hour)
	limited := ModelBudgetObservation{At: asOf.Add(-time.Minute), AccountID: "wb-limited", Region: "cn", Model: "glm-5.3", Plan: "pro", RequestPath: "openai_chat_completions",
		Status: AttemptStatusFailover, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset}
	truncated := EstimateWorkBuddyBudgetsInRange([]ModelBudgetObservation{limited}, asOf, asOf.Add(-7*24*time.Hour), true)
	if len(truncated) != 1 || !truncated[0].Restricted || !truncated[0].ObservationTruncated || truncated[0].EstimatedTokens != nil || truncated[0].EstimatedRemaining != nil {
		t.Fatalf("truncated observations must retain a known restriction without a budget number: %+v", truncated)
	}
}

func appendBudgetSamples(dst []ModelBudgetObservation, account, plan, requestPath string, base time.Time, values []int64) []ModelBudgetObservation {
	for index := 0; index <= len(values); index++ {
		at := base.Add(time.Duration(index*3) * time.Hour)
		reset := at.Add(time.Hour)
		dst = append(dst, ModelBudgetObservation{At: at, AccountID: account, Region: "cn", Model: "glm-5.3", Plan: plan, RequestPath: requestPath,
			Status: AttemptStatusError, Kind: KindRateLimit, Code: workBuddyLimitCode, Scope: "model", UpstreamResetAt: &reset})
		if index < len(values) {
			used := values[index]
			dst = append(dst, ModelBudgetObservation{At: reset.Add(time.Minute), AccountID: account, Region: "cn", Model: "glm-5.3", Plan: plan, RequestPath: requestPath,
				Status: AttemptStatusOK, UsageState: "actual", PromptTokens: &used, CompletionTokens: int64Pointer(0)})
		}
	}
	return dst
}
