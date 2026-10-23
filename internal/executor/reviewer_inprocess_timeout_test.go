package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type reworkWaitChat struct{}

func (reworkWaitChat) ChatNonStream(ctx context.Context, _ string, _ translate.ChatRequest) (providers.ChatOutcome, error) {
	<-ctx.Done()
	return providers.ChatOutcome{}, ctx.Err()
}

func (reworkWaitChat) ChatStream(ctx context.Context, _ string, _ translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	<-ctx.Done()
	return nil, providers.ResolvedChat{}, ctx.Err()
}

func TestReworkInProcessTerminationEvidenceStreamAndNonStream(t *testing.T) {
	for _, provider := range []string{"workbuddy", "trae"} {
		for _, streamed := range []bool{false, true} {
			for _, callerCancel := range []bool{false, true} {
				name := provider + "/"
				if streamed {
					name += "stream/"
				} else {
					name += "nonstream/"
				}
				if callerCancel {
					name += "caller_cancel"
				} else {
					name += "total_timeout"
				}
				t.Run(name, func(t *testing.T) {
					pool := NewPool(nil, nil)
					ready := true
					pool.Upsert(Item{ID: "synthetic", Provider: provider, Region: "cn", Runtime: "in_process", Ready: &ready, Hot: &ready, Models: []string{"synthetic-model"}})
					executor := NewChatExecutor(pool, "")
					executor.Providers = providers.NewRegistry()
					executor.Providers.Register(providers.Adapter{ID: provider, Chat: reworkWaitChat{}})
					if callerCancel {
						executor.RequestTimeout = time.Second
					} else {
						executor.RequestTimeout = 60 * time.Millisecond
					}
					var attempts []accounts.RequestAttempt
					executor.OnAttempt = func(attempt accounts.RequestAttempt) { attempts = append(attempts, attempt) }
					ctx, cancel := context.WithCancel(WithRequestID(context.Background(), "synthetic-rework"))
					defer cancel()
					if callerCancel {
						time.AfterFunc(10*time.Millisecond, cancel)
					}
					request := translate.ChatRequest{Model: "synthetic-model"}
					var err error
					if streamed {
						_, err = executor.ChatStreamProxy(ctx, request, "synthetic", provider)
					} else {
						_, err = executor.ChatNonStream(ctx, request, "synthetic", provider)
					}
					if err == nil {
						t.Fatal("expected request termination")
					}
					classified := ClassifyError(err)
					wantCode, wantStatus := "request_timeout_total", 504
					wantPhase, wantSource := "total", "gateway_chat_deadline"
					if callerCancel {
						wantCode, wantStatus, wantPhase, wantSource = "request_canceled", 499, "", ""
					}
					if classified.Code != wantCode || classified.Status != wantStatus || classified.ErrorPhase != wantPhase || classified.ErrorSource != wantSource {
						t.Fatalf("returned classification=%+v, want code/status/phase/source %q/%d/%q/%q", classified, wantCode, wantStatus, wantPhase, wantSource)
					}
					if len(attempts) != 1 {
						t.Fatalf("attempt count=%d, want exactly one", len(attempts))
					}
					attempt := attempts[0]
					if attempt.ErrorCode != wantCode || attempt.ErrorPhase != wantPhase || attempt.ErrorSource != wantSource || attempt.Status != accounts.AttemptStatusCanceled {
						t.Fatalf("recorded attempt=%+v, want matching terminal evidence", attempt)
					}
					for _, item := range pool.Items() {
						if item.LiveInFlight != 0 {
							t.Fatalf("request termination leaked a slot: %+v", item)
						}
					}
				})
			}
		}
	}
}
