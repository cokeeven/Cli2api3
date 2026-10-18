package executor

import (
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/providers/trae"
)

func TestTrae3004UsesExistingScopedBackoffAndHeaderPolicy(t *testing.T) {
	upstream := trae.Classify(0, `{"code":3004,"message":"localized"}`)
	err := &providers.Error{Kind: upstream.Kind, Status: upstream.Status, Code: "3004", Message: upstream.Message, RetryAfterHeader: "120"}
	classified := ClassifyError(err)
	if classified.Kind != KindRateLimit || classified.Cooldown != 2*time.Minute || !classified.Failover {
		t.Fatalf("classified=%+v", classified)
	}
	pool := NewPool(nil, nil)
	pool.Upsert(Item{ID: "trae", Provider: "trae", Region: "cn", Models: []string{"m", "other"}})
	pool.Upsert(Item{ID: "wb", Provider: "workbuddy", Region: "cn", Models: []string{"m"}})
	e := NewChatExecutor(pool, "")
	e.ObserveStreamFailure("trae", err, "m")
	first, _ := pool.ByID("trae")
	e.ObserveStreamFailure("trae", err, "m")
	second, _ := pool.ByID("trae")
	if !second.ModelDownUntil["m"].After(first.ModelDownUntil["m"].Add(time.Minute)) || !second.DownUntil.IsZero() || len(second.ModelDownUntil) != 1 {
		t.Fatal("scoped exponential backoff changed")
	}
	wb, _ := pool.ByID("wb")
	if !wb.DownUntil.IsZero() || len(wb.ModelDownUntil) != 0 {
		t.Fatal("cross-platform penalty")
	}
	if got := ClassifyError(&providers.Error{Kind: KindRateLimit, Status: 429, RetryAfterHeader: "999999999"}); got.Cooldown != maxRetryAfter {
		t.Fatalf("header ceiling=%s", got.Cooldown)
	}
}
