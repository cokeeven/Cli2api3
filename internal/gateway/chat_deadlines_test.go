package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	applogs "github.com/caigee-cmd/cli2api/internal/logs"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type deadlineChat struct{ url string }

func (d deadlineChat) ChatNonStream(context.Context, string, translate.ChatRequest) (providers.ChatOutcome, error) {
	return providers.ChatOutcome{}, providers.ErrUnsupported
}
func (d deadlineChat) ChatStream(ctx context.Context, _ string, _ translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	resp, err := providers.DoChatRequest(http.DefaultClient, r)
	return resp, providers.ResolvedChat{OutputLimit: ptrInt(32), OutputLimitSource: "caller_max_tokens", ReasoningLevel: "high"}, err
}

func TestChatDeadlineRequestAttemptDiagnosticsAndSettlementAgree(t *testing.T) {
	for _, phase := range []string{"upstream_headers", "first_event", "stream_idle", "total"} {
		t.Run(phase, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "upstream_headers" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				if phase == "stream_idle" {
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "deadlines.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			recorder := applogs.NewRequestRecorder(db)
			pool := executor.NewPool(nil, nil)
			for _, id := range []string{"a", "b"} {
				pool.Upsert(executor.Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "inprocess", MaxInFlight: 1, Models: []string{"m"}})
			}
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: deadlineChat{upstream.URL}})
			// Only the targeted phase has a short deadline. Unrelated phases
			// must not win because the full test suite briefly saturates the host.
			e.ChatTimeouts = providers.ChatTimeouts{Headers: 5 * time.Second, FirstEvent: 5 * time.Second, Idle: 5 * time.Second}
			switch phase {
			case "upstream_headers":
				e.ChatTimeouts.Headers = 50 * time.Millisecond
			case "first_event":
				e.ChatTimeouts.FirstEvent = 50 * time.Millisecond
			case "stream_idle":
				e.ChatTimeouts.Idle = 50 * time.Millisecond
			}
			if phase == "total" {
				e.RequestTimeout = 20 * time.Millisecond
			}
			e.OnAttempt = recorder.Attempt
			budget := &lifecycleBudget{}
			e.Budgets = budget
			h := &Handler{Executor: e, Pool: pool, Recorder: recorder}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "key", AllowedProviders: []string{"workbuddy:cn"}})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"workbuddy/m","messages":[{"role":"user","content":"synthetic"}],"stream":true,"max_tokens":32}`)).WithContext(ctx)
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if recovered := recover(); recovered != nil && recovered != http.ErrAbortHandler {
						panic(recovered)
					}
				}()
				h.HandleChatCompletions(w, r)
			}()
			recorder.Close()
			id := w.Header().Get("X-Request-ID")
			got, err := db.GetRequestLog(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != accounts.RequestStatusCanceled || got.ErrorCode != "request_timeout_"+phase || len(got.Attempts) != 1 || got.Attempts[0].Status != accounts.AttemptStatusCanceled {
				data, _ := json.Marshal(got)
				t.Fatalf("inconsistent outcome: %s response=%s", data, w.Body.String())
			}
			if got.OutputLimit == nil || *got.OutputLimit != 32 || got.OutputLimitSource != "caller_max_tokens" || got.ResolvedReasoning != "high" {
				t.Fatal("timeout lost resolved outbound settings")
			}
			if len(budget.settled) != 1 || len(budget.reserved) != 1 {
				t.Fatalf("settlement/reservation=%d/%d", len(budget.settled), len(budget.reserved))
			}
			if phase == "stream_idle" {
				if budget.settled[0].ActualTokens == nil || *budget.settled[0].ActualTokens != 4 {
					t.Fatal("known usage was lost")
				}
				if got.StreamDiagnostic == nil || got.StreamDiagnostic.TimeoutPhase != phase || got.StreamDiagnostic.CancellationSource != "request_timeout" {
					t.Fatalf("diagnostics=%+v", got.StreamDiagnostic)
				}
			}
			for _, item := range pool.Items() {
				if item.LiveInFlight != 0 || !item.DownUntil.IsZero() || len(item.ModelDownUntil) > 0 {
					t.Fatalf("slot/health not preserved: %+v", item)
				}
			}
		})
	}
}
