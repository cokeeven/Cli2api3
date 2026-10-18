package executor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestCooldownEvidenceIsScopedAndSafeForRouteDiagnostics(t *testing.T) {
	p := NewPool(nil, nil)
	ready := true
	p.Upsert(Item{ID: "synthetic-a", Provider: "workbuddy", Region: "cn", Models: []string{"model-a"}, Ready: &ready})
	reset := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)
	p.MarkClassified("synthetic-a", Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_LIMIT",
		EvidenceSource: "provider_adapter_error", UpstreamResetAt: &reset, Cooldown: time.Hour,
		Message: "synthetic private upstream detail must not be exposed",
	})
	p.MarkClassified("synthetic-a", Classified{
		Kind: accounts.KindUnavailable, Scope: "account", Code: "SYNTHETIC_ACCOUNT_FAILURE",
		EvidenceSource: "local_runtime", Cooldown: 2 * time.Hour,
		Message: "synthetic account-level detail",
	})

	item, ok := p.ByID("synthetic-a")
	if !ok {
		t.Fatal("missing synthetic account")
	}
	modelEvidence := item.CooldownEvidence["model-a"]
	accountEvidence := item.CooldownEvidence[""]
	if modelEvidence.Message != "synthetic private upstream detail must not be exposed" || modelEvidence.Code != "SYNTHETIC_LIMIT" || modelEvidence.Scope != "model" {
		t.Fatalf("model evidence crossed scope: %+v", modelEvidence)
	}
	if accountEvidence.Message != "synthetic account-level detail" || accountEvidence.Code != "SYNTHETIC_ACCOUNT_FAILURE" || accountEvidence.Scope != "account" {
		t.Fatalf("account evidence crossed scope: %+v", accountEvidence)
	}

	candidates := p.InspectRoute(RouteQuery{PublicModel: "model-a", ProviderFilter: "workbuddy", RegionFilter: "cn"})
	if len(candidates) != 1 || candidates[0].AccountCooldownEvidence == nil || candidates[0].ModelCooldownEvidence == nil {
		t.Fatalf("expected both active scope details in route diagnostics: %+v", candidates)
	}
	encoded, err := json.Marshal(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	if !strings.Contains(serialized, "SYNTHETIC_LIMIT") || !strings.Contains(serialized, "SYNTHETIC_ACCOUNT_FAILURE") {
		t.Fatalf("route diagnostics omitted bounded codes: %s", serialized)
	}
	if strings.Contains(serialized, "synthetic private upstream detail") || strings.Contains(serialized, "synthetic account-level detail") {
		t.Fatalf("route diagnostics exposed stored error message: %s", serialized)
	}
}

func TestCooldownEvidenceObservationAndClearingStayScoped(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "synthetic-a", Provider: "workbuddy", Region: "cn", Models: []string{"model-a", "model-b"}})
	staleVersion, _ := p.ByID("synthetic-a")
	reset := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	p.MarkClassified("synthetic-a", Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_A_FIRST",
		EvidenceSource: "provider_adapter_error", UpstreamResetAt: &reset, Cooldown: time.Hour, Message: "a-first",
	})
	first, _ := p.ByID("synthetic-a")
	firstObserved := first.CooldownEvidence["model-a"].FirstObservedAt
	time.Sleep(2 * time.Millisecond)
	p.MarkClassified("synthetic-a", Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_A_LATEST",
		EvidenceSource: "provider_adapter_error", Cooldown: time.Hour, Message: "a-latest",
	})
	p.MarkClassified("synthetic-a", Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-b", Code: "SYNTHETIC_B",
		EvidenceSource: "provider_adapter_error", Cooldown: 2 * time.Hour, Message: "b-no-reset",
	})
	state, _ := p.ByID("synthetic-a")
	a := state.CooldownEvidence["model-a"]
	b := state.CooldownEvidence["model-b"]
	if !a.FirstObservedAt.Equal(firstObserved) || !a.LastObservedAt.After(firstObserved) || a.Code != "SYNTHETIC_A_LATEST" || a.Message != "a-latest" {
		t.Fatalf("model-a observation window/latest fact mismatch: first=%+v latest=%+v", firstObserved, a)
	}
	if a.UpstreamResetAt != nil {
		t.Fatalf("missing latest reset was copied from a prior event: %+v", a.UpstreamResetAt)
	}
	if b.Message != "b-no-reset" || b.UpstreamResetAt != nil || b.Scope != "model" {
		t.Fatalf("model-b evidence mismatch: %+v", b)
	}
	if !a.LocalBackoffUntil.Equal(state.ModelDownUntil["model-a"]) || !b.LocalBackoffUntil.Equal(state.ModelDownUntil["model-b"]) {
		t.Fatalf("local backoff deadline did not match the pool gate: a=%+v b=%+v state=%+v", a, b, state.ModelDownUntil)
	}

	// A successful response from another model and an ordinary quota refresh
	// must leave model-a's restriction evidence intact.
	p.MergeQuota("synthetic-a", &QuotaSnapshot{FetchedAt: time.Now().UTC().Format(time.RFC3339)})
	p.MarkOK("synthetic-a", "model-b")
	state, _ = p.ByID("synthetic-a")
	if got := state.CooldownEvidence["model-a"]; got.Code != "SYNTHETIC_A_LATEST" || got.Message != "a-latest" {
		t.Fatalf("another-model success or quota refresh cleared model-a evidence: %+v", got)
	}
	if _, ok := state.CooldownEvidence["model-b"]; ok {
		t.Fatal("same-model success did not clear its own evidence")
	}

	// An older admitted success cannot clear a restriction learned later.
	p.MarkOKVersion("synthetic-a", "model-a", staleVersion.StateVersion)
	state, _ = p.ByID("synthetic-a")
	if got := state.CooldownEvidence["model-a"]; got.Code != "SYNTHETIC_A_LATEST" || state.ModelDownUntil["model-a"].IsZero() {
		t.Fatalf("late success cleared newer model restriction: %+v", state)
	}
}

func TestCooldownEvidenceDoesNotBackfillLegacyFirstObservation(t *testing.T) {
	p := NewPool(nil, nil)
	until := time.Now().UTC().Add(time.Hour)
	p.Upsert(Item{
		ID: "synthetic-legacy", ModelDownUntil: map[string]time.Time{"model-a": until},
		CooldownEvidence: map[string]accounts.CooldownEvidence{"model-a": {Message: "legacy-message-only"}},
	})
	p.MarkClassified("synthetic-legacy", Classified{
		Kind: accounts.KindRateLimit, Scope: "model", Model: "model-a", Code: "SYNTHETIC_NEW_OBSERVATION",
		EvidenceSource: "provider_adapter_error", Cooldown: time.Minute, Message: "new-observation",
	})
	item, _ := p.ByID("synthetic-legacy")
	evidence := item.CooldownEvidence["model-a"]
	if !evidence.FirstObservedAt.IsZero() {
		t.Fatalf("existing active legacy cooldown gained a synthetic historical start time: %+v", evidence)
	}
	if evidence.LastObservedAt.IsZero() || evidence.Code != "SYNTHETIC_NEW_OBSERVATION" {
		t.Fatalf("new observation was not recorded without backfilling history: %+v", evidence)
	}
}
