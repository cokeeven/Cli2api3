package executor

import (
	"testing"
	"time"
)

func TestPoolReadSnapshotsDoNotAliasMutableState(t *testing.T) {
	for _, read := range []string{"by_id", "first", "pick", "admission"} {
		t.Run(read, func(t *testing.T) {
			p := NewPool(nil, nil)
			p.Upsert(Item{ID: "a", Provider: "trae", Region: "cn", Models: []string{"m"}, NativeModels: map[string]string{"m": "native"}, ModelDownUntil: map[string]time.Time{"other": time.Now()}})
			var snapshot Item
			switch read {
			case "by_id":
				snapshot, _ = p.ByID("a")
			case "first":
				snapshot, _ = p.First()
			case "pick":
				snapshot, _ = p.PickRoute(RouteQuery{PublicModel: "m"})
			case "admission":
				var release func()
				snapshot, release, _ = p.acquireRoute(RouteQuery{PublicModel: "m"}, routingIdentity{})
				defer release()
			}
			snapshot.NativeModels["m"] = "mutated"
			snapshot.Models[0] = "mutated"
			delete(snapshot.ModelDownUntil, "other")
			live := p.Items()[0]
			if live.NativeModels["m"] != "native" || live.Models[0] != "m" || len(live.ModelDownUntil) != 1 {
				t.Fatal("read snapshot mutated the live pool")
			}
		})
	}
}
