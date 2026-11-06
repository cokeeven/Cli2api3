package console

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/endpoint"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/gateway"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type strictTestProvider struct{ seen *[]string }

func (p strictTestProvider) ChatNonStream(_ context.Context, id string, _ translate.ChatRequest) (providers.ChatOutcome, error) {
	*p.seen = append(*p.seen, id)
	if id == "a" {
		return providers.ChatOutcome{}, &providers.Error{Kind: executor.KindAuth, Status: 401, Scope: "account", Message: "synthetic rejection"}
	}
	return providers.ChatOutcome{Content: "ok", FinishReason: "stop"}, nil
}
func (p strictTestProvider) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, providers.ResolvedChat{}, nil
}

func TestConsoleChatStrictAndPublicChatFallback(t *testing.T) {
	for _, console := range []bool{true, false} {
		seen := []string{}
		pool := executor.NewPool(nil, nil)
		for _, id := range []string{"a", "b"} {
			pool.Upsert(executor.Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
		}
		e := executor.NewChatExecutor(pool, "")
		e.Providers = providers.NewRegistry()
		e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: strictTestProvider{seen: &seen}})
		gw := &gateway.Handler{Executor: e, Pool: pool, RequestedAccount: endpoint.RequestedAccount}
		h := &Handler{Chat: gw.HandleChatCompletions, RequestedAccount: endpoint.RequestedAccount}
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"workbuddy/m","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("X-Qoder-Account", "a")
		w := httptest.NewRecorder()
		if console {
			h.HandleChat(w, req)
		} else {
			gw.HandleChatCompletions(w, req)
		}
		if w.Header().Get("X-Request-Id") == "" {
			t.Fatalf("request ID missing: status=%d body=%s", w.Code, w.Body.String())
		}
		if console {
			if w.Code == http.StatusOK || len(seen) != 1 || seen[0] != "a" || w.Header().Get("X-CLI2API-Account") != "a" {
				t.Fatalf("strict status=%d seen=%v account=%q", w.Code, seen, w.Header().Get("X-CLI2API-Account"))
			}
		} else if w.Code != http.StatusOK || len(seen) != 2 || seen[0] != "a" || seen[1] != "b" || w.Header().Get("X-CLI2API-Account") != "b" {
			t.Fatalf("ordinary status=%d seen=%v account=%q", w.Code, seen, w.Header().Get("X-CLI2API-Account"))
		}
	}
}
