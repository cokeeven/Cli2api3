package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type reasoningCaptureChat struct{ effort string }

func (c *reasoningCaptureChat) ChatNonStream(_ context.Context, _ string, r translate.ChatRequest) (providers.ChatOutcome, error) {
	c.effort = string(r.ReasoningEffort)
	return providers.ChatOutcome{Content: "ok", FinishReason: "stop", ReasoningLevel: "medium"}, nil
}
func (*reasoningCaptureChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	panic("unexpected stream")
}
func TestPublicProtocolsPreserveExplicitReasoning(t *testing.T) {
	cases := []struct {
		path, body string
		serve      func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"/v1/chat/completions", `{"model":"workbuddy/m","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`, (*Handler).HandleChatCompletions},
		{"/v1/messages", `{"model":"workbuddy/m","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"output_config":{"effort":"high"}}`, (*Handler).HandleAnthropicMessages},
		{"/v1/responses", `{"model":"workbuddy/m","input":"hello","reasoning":{"effort":"high"}}`, (*Handler).HandleResponses},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			pool := executor.NewPool(nil, nil)
			pool.Upsert(executor.Item{ID: "a", Provider: "workbuddy", Region: "cn", Runtime: "inprocess", Models: []string{"m"}, MaxInFlight: 1})
			capture := &reasoningCaptureChat{}
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: capture})
			h := &Handler{Executor: e, Pool: pool}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "synthetic", AllowedProviders: []string{"workbuddy:cn"}})
			rec := httptest.NewRecorder()
			tc.serve(h, rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)).WithContext(ctx))
			if rec.Code != 200 || capture.effort != `"high"` {
				t.Fatalf("code=%d effort=%q body=%s", rec.Code, capture.effort, rec.Body.String())
			}
		})
	}
}
