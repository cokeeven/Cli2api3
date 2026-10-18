package trae

import (
	"context"
	"errors"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"net/http"
	"testing"
)

func TestTrae3004RateLimitDoesNotDependOnMessageLanguage(t *testing.T) {
	for _, body := range []string{`{"code":3004,"message":"requests have exceeded the rate limit"}`, `{"code":3004,"message":"请求频繁"}`, `{"code":"3004","message":""}`} {
		got := Classify(0, body)
		if got.Kind != accounts.KindRateLimit || got.Status != 429 {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestChatHTTPRateLimitPreservesRetryAfterHeader(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "125")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"code":3004,"message":"localized"}`))
	}))
	credential, _ := (Credential{AccessToken: "synthetic", RefreshToken: "synthetic", UID: "synthetic", Domain: DomainCN, ExpiresAt: 4102444800, MachineID: "synthetic", DeviceID: "synthetic"}).Encode()
	store.items = map[string][]byte{"a": credential}
	for _, stream := range []bool{false, true} {
		req := translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}}
		var err error
		if stream {
			_, _, err = client.ChatStream(context.Background(), "a", req)
		} else {
			_, err = client.ChatNonStream(context.Background(), "a", req)
		}
		var upstream *providers.Error
		if !errors.As(err, &upstream) || upstream.Kind != accounts.KindRateLimit || upstream.RetryAfterHeader != "125" {
			t.Fatalf("error=%v", err)
		}
	}
}
