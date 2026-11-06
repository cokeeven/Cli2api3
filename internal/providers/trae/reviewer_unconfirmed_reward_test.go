package trae

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReviewerUnconfirmedRewardDoesNotExposeConfirmedAmount(t *testing.T) {
	calls := 0
	c, s := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathCheckinStatus {
			calls++
			if calls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"checked_in":false,"credits":100,"extra_credits":100,"enable":true}`))
			} else {
				_, _ = w.Write([]byte(`{"code":0,"checked_in":true,"credits":100,"extra_credits":100}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	seedCheckinCredential(t, s)
	result, err := c.Checkin(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), `"reward_credits"`) {
		t.Fatalf("status rule values presented as confirmed actual reward: %s", raw)
	}
}
