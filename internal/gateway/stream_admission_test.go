package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type heldStreamChat struct {
	opened chan struct{}
	reader *io.PipeReader
}

func (s heldStreamChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	return providers.ChatOutcome{}, fmt.Errorf("unexpected non-stream request")
}
func (s heldStreamChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	close(s.opened)
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: s.reader}, providers.ResolvedChat{}, nil
}

func TestPublicStreamHoldsAdmissionUntilRelayAndRecordsTerminalAttempt(t *testing.T) {
	for _, protocol := range []struct {
		path, body string
		serve      func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"/v1/chat/completions", `{"model":"workbuddy/guard-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":true}`, (*Handler).HandleChatCompletions},
		{"/v1/messages", `{"model":"workbuddy/guard-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":true}`, (*Handler).HandleAnthropicMessages},
		{"/v1/responses", `{"model":"workbuddy/guard-model","input":"hello","max_output_tokens":32,"stream":true}`, (*Handler).HandleResponses},
	} {
		t.Run(protocol.path, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			opened := make(chan struct{})
			pool := executor.NewPool(nil, nil)
			pool.Upsert(executor.Item{ID: "account", Provider: "workbuddy", Region: "cn", Runtime: "inprocess", MaxInFlight: 1, Models: []string{"guard-model"}})
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: heldStreamChat{opened: opened, reader: reader}})
			var mu sync.Mutex
			var attempts []accounts.RequestAttempt
			e.OnAttempt = func(a accounts.RequestAttempt) { mu.Lock(); defer mu.Unlock(); attempts = append(attempts, a) }
			h := &Handler{Executor: e, Pool: pool}
			identity := auth.Identity{Kind: auth.KindKey, KeyID: "test-key", AllowedProviders: []string{"workbuddy:cn"}, RoutingPolicy: accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", MaxInFlight: 1}}}}
			request := func() *http.Request {
				r := httptest.NewRequest("POST", protocol.path, strings.NewReader(protocol.body))
				return r.WithContext(auth.WithIdentity(r.Context(), identity))
			}
			done := make(chan struct{})
			first := httptest.NewRecorder()
			go func() { defer close(done); protocol.serve(h, first, request()) }()
			select {
			case <-opened:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not open")
			}
			item, _ := pool.ByID("account")
			if item.LiveInFlight != 1 {
				t.Fatalf("live count at headers=%d", item.LiveInFlight)
			}
			mu.Lock()
			for _, a := range attempts {
				if a.Status == accounts.AttemptStatusOK {
					t.Error("attempt marked successful at headers")
				}
			}
			mu.Unlock()
			second := httptest.NewRecorder()
			protocol.serve(h, second, request())
			if second.Code != 429 {
				t.Fatalf("concurrent request status=%d body=%s", second.Code, second.Body.String())
			}
			_, err := io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n")
			if err != nil {
				t.Fatal(err)
			}
			writer.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not finish")
			}
			if first.Code != 200 {
				t.Fatalf("first status=%d", first.Code)
			}
			item, _ = pool.ByID("account")
			if item.LiveInFlight != 0 || len(pool.PlatformInFlight()) != 0 {
				t.Fatalf("admission leak: %+v", pool.PlatformInFlight())
			}
			mu.Lock()
			defer mu.Unlock()
			successes := 0
			for _, a := range attempts {
				if a.Status == accounts.AttemptStatusOK {
					successes++
					if a.FinishedAt == nil || a.PromptTokens == nil || *a.PromptTokens != 3 {
						t.Errorf("incomplete terminal attempt: %+v", a)
					}
				}
			}
			if successes != 1 {
				t.Fatalf("terminal successes=%d", successes)
			}
		})
	}
}
