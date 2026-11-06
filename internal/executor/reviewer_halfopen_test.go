package executor

import (
	"testing"
	"time"
)

func TestReviewerExpiredModelCooldownAdmitsOneProbe(t *testing.T) {
	p := NewPool(nil, nil)
	ready := true
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"m", "other"}, MaxInFlight: 4, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
	q := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}
	_, release, ok := p.acquireRoute(q, routingIdentity{})
	if !ok {
		t.Fatal("first probe not admitted")
	}
	defer release()
	second, secondRelease, ok := p.acquireRoute(q, routingIdentity{})
	if ok {
		if secondRelease != nil {
			defer secondRelease()
		}
		if p.RetryAfter(second, "m") <= 0 {
			t.Fatal("second probe admitted while first in flight")
		}
	}
}
