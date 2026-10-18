package gateway

import (
	"context"
	"errors"
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

type partialFailureChat struct {
	calls int
	err   error
}

func (d *partialFailureChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	d.calls++
	return providers.ChatOutcome{Content: "partial", PromptTokens: 3, CompletionTokens: 2, UsageSource: "upstream"}, d.err
}
func (*partialFailureChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, providers.ErrUnsupported
}
func TestNonstreamFailurePreservesConfirmedUsageWithoutReplay(t *testing.T) {
	for _, failure := range []error{errors.New("synthetic read failure"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "partial.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			recorder := applogs.NewRequestRecorder(db)
			pool := executor.NewPool(nil, nil)
			for _, id := range []string{"a", "b"} {
				pool.Upsert(executor.Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "inprocess", Models: []string{"m"}, MaxInFlight: 1})
			}
			adapter := &partialFailureChat{err: failure}
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: adapter})
			e.OnAttempt = recorder.Attempt
			budget := &lifecycleBudget{}
			e.Budgets = budget
			h := &Handler{Executor: e, Pool: pool, Recorder: recorder}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "synthetic", AllowedProviders: []string{"workbuddy:cn"}})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"workbuddy/m","messages":[{"role":"user","content":"synthetic"}],"max_tokens":32}`)).WithContext(ctx)
			w := httptest.NewRecorder()
			h.HandleChatCompletions(w, r)
			recorder.Close()
			got, err := db.GetRequestLog(context.Background(), w.Header().Get("X-Request-ID"))
			if err != nil {
				t.Fatal(err)
			}
			if adapter.calls != 1 || len(got.Attempts) != 1 || got.PromptTokens == nil || *got.PromptTokens != 3 || got.CompletionTokens == nil || *got.CompletionTokens != 2 {
				t.Fatalf("partial usage/replay: calls=%d log=%+v", adapter.calls, got)
			}
			if got.Attempts[0].PromptTokens == nil || *got.Attempts[0].PromptTokens != 3 {
				t.Fatal("attempt usage missing")
			}
			if len(budget.settled) != 1 || budget.settled[0].ActualTokens == nil || *budget.settled[0].ActualTokens != 5 {
				t.Fatalf("settlement=%+v", budget.settled)
			}
		})
	}
}
