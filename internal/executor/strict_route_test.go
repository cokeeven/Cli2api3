package executor

import (
	"context"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestStrictAccountRouteOverridesPreferenceAndNeverFallsBack(t *testing.T) {
	ready := true
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "preferred", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	p.Upsert(Item{ID: "selected", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	e := NewChatExecutor(p, "")
	ctx := WithStrictAccount(context.Background(), "selected")
	loop := e.newRouteLoop(ctx, "preferred", "", translate.ChatRequest{Model: "glm-5.3"})
	if loop.prefer != "selected" || loop.hardProvider != "workbuddy" || loop.regionFilter != "cn" || loop.strictAccount != "selected" {
		t.Fatalf("strict route = %+v", loop)
	}
	q := e.routeQuery(loop.prefer, loop.hardProvider, loop.regionFilter, "glm-5.3", loop.allowed, nil, loop.routeEligibility())
	item, ok := p.PickRoute(q)
	if !ok || item.ID != "selected" {
		t.Fatalf("pick = %+v, ok=%v", item, ok)
	}

	p.Upsert(Item{ID: "selected", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}, ModelDownUntil: map[string]time.Time{"glm-5.3": time.Now().Add(time.Hour)}})
	item, ok = p.PickRoute(q)
	if !ok || item.ID != "selected" || p.RetryAfter(item, "glm-5.3") <= 0 {
		t.Fatalf("cooldown route fell back: %+v, ok=%v", item, ok)
	}
}
