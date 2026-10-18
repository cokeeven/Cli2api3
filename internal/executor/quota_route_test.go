package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestQuotaRestrictionEvidenceSurvivesRefreshWithoutBecomingCooldown(t *testing.T) {
	p := NewPool(nil, nil)
	ready := true
	p.Upsert(Item{ID: "synthetic-quota", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"model-a"}})

	classified := Classified{
		Kind: KindQuota, Scope: "account", Code: "14018", EvidenceSource: "provider_adapter_error",
		Message: "synthetic quota restriction",
	}
	p.MarkClassified("synthetic-quota", classified)
	first, ok := p.ByID("synthetic-quota")
	if !ok || first.Quota == nil || first.Quota.RestrictionEvidence == nil || !first.Quota.Exceeded {
		t.Fatalf("missing synthetic quota state: %+v", first.Quota)
	}
	firstObservedAt := first.Quota.RestrictionEvidence.FirstObservedAt
	if firstObservedAt == nil || first.Quota.RestrictionEvidence.UpstreamResetAt != nil || !first.DownUntil.IsZero() {
		t.Fatalf("no-reset quota created a timer or lost its unknown reset: %+v", first)
	}

	refreshedAt := time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	p.MergeQuota("synthetic-quota", &QuotaSnapshot{Remaining: 17, Unit: "credits", FetchedAt: refreshedAt})
	refreshed, ok := p.ByID("synthetic-quota")
	if !ok || refreshed.Quota == nil || refreshed.Quota.RestrictionEvidence == nil || refreshed.Quota.RestrictionEvidence.Code != "14018" {
		t.Fatalf("ordinary provider quota refresh erased local restriction evidence: %+v", refreshed.Quota)
	}
	if refreshed.Quota.Remaining != 17 || refreshed.Quota.FetchedAt != refreshedAt {
		t.Fatalf("refresh data was not applied with preserved evidence: %+v", refreshed.Quota)
	}
	if refreshed.Quota.RestrictionEvidence.FirstObservedAt == nil || !refreshed.Quota.RestrictionEvidence.FirstObservedAt.Equal(*firstObservedAt) {
		t.Fatalf("refresh replaced the evidence observation window: %+v", refreshed.Quota.RestrictionEvidence)
	}

	// Upsert is also used when a runtime re-registers accounts. A quota value
	// without the local evidence field must not discard an existing observation.
	p.Upsert(Item{ID: "synthetic-quota", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"model-a"}, Quota: &QuotaSnapshot{Remaining: 20, FetchedAt: refreshedAt}})
	restarted, ok := p.ByID("synthetic-quota")
	if !ok || restarted.Quota == nil || restarted.Quota.RestrictionEvidence == nil || restarted.Quota.RestrictionEvidence.Code != "14018" {
		t.Fatalf("runtime upsert erased local quota evidence: %+v", restarted.Quota)
	}
}

func TestQuotaEvidenceDoesNotLeakBetweenAccounts(t *testing.T) {
	p := NewPool(nil, nil)
	ready := true
	for _, id := range []string{"synthetic-a", "synthetic-b"} {
		p.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"model-a"}})
	}
	p.MarkClassified("synthetic-a", Classified{Kind: KindQuota, Scope: "account", Code: "14018", EvidenceSource: "provider_adapter_error"})
	items := p.Items()
	for _, item := range items {
		if item.ID == "synthetic-a" && (item.Quota == nil || item.Quota.RestrictionEvidence == nil || item.Quota.RestrictionEvidence.Code != "14018") {
			t.Fatalf("account A lost its own quota evidence: %+v", item.Quota)
		}
		if item.ID == "synthetic-b" && item.Quota != nil && item.Quota.RestrictionEvidence != nil {
			t.Fatalf("account A quota evidence leaked into account B: %+v", item.Quota.RestrictionEvidence)
		}
	}
}

func TestWorkBuddyExhaustedSnapshotDoesNotMaskHealthyAccount(t *testing.T) {
	var healthyHits, exhaustedHits atomic.Int32
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"content": "healthy"}}},
			"usage":   map[string]any{"source": "upstream"},
		})
	}))
	defer healthy.Close()
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exhaustedHits.Add(1)
		http.Error(w, "must not be selected", http.StatusTooManyRequests)
	}))
	defer exhausted.Close()

	pool := NewPool(nil, nil)
	pool.Upsert(Item{
		ID: "a-exhausted", URL: exhausted.URL, Provider: "workbuddy", Region: "cn", Runtime: "child_process",
		Models: []string{"glm-5.3"}, Quota: &QuotaSnapshot{Exceeded: true},
	})
	pool.Upsert(Item{
		ID: "b-healthy", URL: healthy.URL, Provider: "workbuddy", Region: "cn", Runtime: "child_process",
		Models: []string{"glm-5.3"},
	})

	executor := NewChatExecutor(pool, "")
	executor.HTTPClient = healthy.Client()
	result, err := executor.ChatNonStream(context.Background(), translate.ChatRequest{
		Model: "glm-5.3", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}},
	}, "", "workbuddy")
	if err != nil {
		t.Fatalf("usable WorkBuddy account was masked by an exhausted snapshot: %v", err)
	}
	if result.AccountID != "b-healthy" || result.Content != "healthy" || healthyHits.Load() != 1 || exhaustedHits.Load() != 0 {
		t.Fatalf("account=%q content=%q healthy=%d exhausted=%d", result.AccountID, result.Content, healthyHits.Load(), exhaustedHits.Load())
	}
}
