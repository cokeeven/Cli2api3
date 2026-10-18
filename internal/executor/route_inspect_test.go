package executor

import (
	"testing"
	"time"
)

func TestInspectRouteExplainsCandidateStateWithoutChangingRotation(t *testing.T) {
	ready := true
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "available", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	p.Upsert(Item{ID: "cooldown", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}, ModelDownUntil: map[string]time.Time{"glm-5.3": time.Now().Add(time.Hour)}})
	p.Upsert(Item{ID: "quota", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}, Quota: &QuotaSnapshot{Exceeded: true}})
	p.Upsert(Item{ID: "unsupported", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.2"}})
	before := len(p.lastPicked)
	rows := p.InspectRoute(RouteQuery{PublicModel: "glm-5.3", ProviderFilter: "workbuddy", RegionFilter: "cn"})
	got := map[string]string{}
	for _, row := range rows {
		got[row.ID] = row.Reason
	}
	if got["available"] != "available" || got["cooldown"] != "model_cooldown" || got["quota"] != "quota_exhausted" || got["unsupported"] != "model_catalog" {
		t.Fatalf("candidate reasons = %+v", got)
	}
	if len(p.lastPicked) != before {
		t.Fatalf("inspection changed rotation state: before=%d after=%d", before, len(p.lastPicked))
	}
}
