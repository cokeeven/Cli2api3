package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	applogs "github.com/caigee-cmd/cli2api/internal/logs"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestFidelityStreamDoesNotCompleteEmptyToolArguments(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_empty\",\"function\":{\"name\":\"count_letters\",\"arguments\":\"\"}}]}}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	var output strings.Builder
	_, err := RelayResponsesStream(&output, strings.NewReader(stream), "req_synthetic", "m", nil)
	if err == nil {
		t.Fatal("empty arguments were converted into an executable completed call")
	}
	if strings.Contains(output.String(), "response.completed") || strings.Contains(output.String(), "response.function_call_arguments.done") {
		t.Fatal("invalid arguments finalized as successful")
	}
}

type invalidToolOutputChat struct{ calls int }

func (d *invalidToolOutputChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	d.calls++
	return providers.ChatOutcome{ToolCalls: json.RawMessage(`[{"id":"call_empty","type":"function","function":{"name":"count_letters","arguments":""}}]`), FinishReason: "tool_calls", PromptTokens: 3, CompletionTokens: 2, UsageSource: "upstream"}, nil
}
func (*invalidToolOutputChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, providers.ErrUnsupported
}

func TestFidelityNonstreamInvalidToolIsNotManufacturedOrReplayed(t *testing.T) {
	for _, protocol := range []string{"responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "fidelity.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			recorder := applogs.NewRequestRecorder(db)
			pool := executor.NewPool(nil, nil)
			for _, id := range []string{"a", "b"} {
				pool.Upsert(executor.Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "inprocess", Models: []string{"m"}, MaxInFlight: 1})
			}
			adapter := &invalidToolOutputChat{}
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: adapter})
			e.OnAttempt = recorder.Attempt
			budget := &lifecycleBudget{}
			e.Budgets = budget
			h := &Handler{Executor: e, Pool: pool, Recorder: recorder}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "synthetic", AllowedProviders: []string{"workbuddy:cn"}})
			body := `{"model":"workbuddy/m","input":"synthetic","max_output_tokens":32}`
			if protocol == "anthropic" {
				body = `{"model":"workbuddy/m","messages":[{"role":"user","content":"synthetic"}],"max_tokens":32}`
			}
			r := httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(body)).WithContext(ctx)
			w := httptest.NewRecorder()
			if protocol == "responses" {
				h.HandleResponses(w, r)
			} else {
				h.HandleAnthropicMessages(w, r)
			}
			recorder.Close()
			got, err := db.GetRequestLog(context.Background(), w.Header().Get("X-Request-ID"))
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), `"arguments":"{}"`) {
				t.Fatalf("invalid call finalized: status=%d body=%s", w.Code, w.Body.String())
			}
			if adapter.calls != 1 || len(got.Attempts) != 1 || got.Status != "error" || got.CompletionTokens == nil || *got.CompletionTokens != 2 {
				t.Fatalf("replay or usage loss: calls=%d log=%+v", adapter.calls, got)
			}
			// The upstream attempt completed; protocol conversion failed afterwards.
			if got.Attempts[0].Status != "ok" {
				t.Fatalf("upstream attempt=%s", got.Attempts[0].Status)
			}
			if len(budget.settled) != 1 || budget.settled[0].ActualTokens == nil || *budget.settled[0].ActualTokens != 5 {
				t.Fatalf("settlement=%+v", budget.settled)
			}
		})
	}
}
