package executor

import (
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestModelHalfOpenIsSinglePerAccountModelAndReleased(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m", "other"}, MaxInFlight: 100, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
	q := RouteQuery{PublicModel: "m", ProviderFilter: "workbuddy", RegionFilter: "cn"}
	item, release, ok := p.acquireRoute(q, routingIdentity{})
	if !ok || !item.modelProbe {
		t.Fatalf("first %+v %v", item, ok)
	}
	var wg sync.WaitGroup
	successes := make(chan bool, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, r, ok := p.acquireRoute(q, routingIdentity{})
			if ok {
				r()
			}
			successes <- ok
		}()
	}
	wg.Wait()
	close(successes)
	for ok := range successes {
		if ok {
			t.Fatal("concurrent probe admitted")
		}
	}
	_, otherRelease, ok := p.acquireRoute(RouteQuery{PublicModel: "other", ProviderFilter: "workbuddy", RegionFilter: "cn"}, routingIdentity{})
	if !ok {
		t.Fatal("other model blocked")
	}
	otherRelease()
	release()
	release()
	_, again, ok := p.acquireRoute(q, routingIdentity{})
	if !ok {
		t.Fatal("released probe not available")
	}
	again()
}
func TestStructured11102HasLocalModelBackoff(t *testing.T) {
	c := ClassifyError(&providers.Error{Kind: accounts.KindUnavailable, Status: 503, Code: "11102", Scope: "model", RetrySafe: true})
	if c.Cooldown != 2*time.Minute || c.UpstreamResetAt != nil {
		t.Fatalf("%+v", c)
	}
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Models: []string{"m", "other"}})
	c.Model = "m"
	p.MarkClassified("a", c)
	item, _ := p.ByID("a")
	if !item.DownUntil.IsZero() || item.ModelDownUntil["m"].IsZero() || !item.ModelDownUntil["other"].IsZero() || item.CooldownEvidence["m"].UpstreamResetAt != nil {
		t.Fatalf("%+v", item)
	}
}
