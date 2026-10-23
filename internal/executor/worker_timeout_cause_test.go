package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestWorkerTimeoutAndClientCancellationKeepDistinctCauses(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		for _, clientCancel := range []bool{false, true} {
			name := "total_timeout"
			if clientCancel {
				name = "client_cancel"
			}
			if streamed {
				name += "_stream"
			} else {
				name += "_nonstream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					time.Sleep(200 * time.Millisecond)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(func() {
					server.CloseClientConnections()
					server.Close()
				})

				pool := NewPool(nil, nil)
				ready := true
				pool.Upsert(Item{
					ID: "worker", Provider: "qoder", Region: "cn", Runtime: "child_process",
					URL: server.URL, Ready: &ready, Hot: &ready, Models: []string{"synthetic-model"},
				})
				ex := NewChatExecutor(pool, "")
				ex.HTTPClient = server.Client()
				if clientCancel {
					ex.RequestTimeout = time.Second
				} else {
					ex.RequestTimeout = 100 * time.Millisecond
				}

				var mu sync.Mutex
				var attempts []accounts.RequestAttempt
				ex.OnAttempt = func(attempt accounts.RequestAttempt) {
					mu.Lock()
					attempts = append(attempts, attempt)
					mu.Unlock()
				}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = WithRequestID(ctx, "synthetic-request")
				if clientCancel {
					time.AfterFunc(25*time.Millisecond, cancel)
				}

				var err error
				if streamed {
					_, err = ex.ChatStreamProxy(ctx, translate.ChatRequest{Model: "synthetic-model"}, "worker", "qoder")
				} else {
					_, err = ex.ChatNonStream(ctx, translate.ChatRequest{Model: "synthetic-model"}, "worker", "qoder")
				}
				if err == nil {
					t.Fatal("expected request termination")
				}

				classified := ClassifyError(err)
				if clientCancel {
					if classified.Status != 499 || classified.Code != "request_canceled" {
						t.Fatalf("caller cancellation classified as %+v; err=%v", classified, err)
					}
				} else if classified.Status != 504 || classified.Code != "request_timeout_total" {
					t.Fatalf("total timeout classified as %+v; err=%v", classified, err)
				}

				mu.Lock()
				gotAttempts := append([]accounts.RequestAttempt(nil), attempts...)
				mu.Unlock()
				if len(gotAttempts) != 1 {
					t.Fatalf("attempts=%d, want exactly 1", len(gotAttempts))
				}
				attempt := gotAttempts[0]
				if attempt.Status != accounts.AttemptStatusCanceled || attempt.ErrorKind != accounts.KindCanceled {
					t.Fatalf("attempt termination facts=%+v", attempt)
				}
				if clientCancel {
					if attempt.ErrorCode != "request_canceled" {
						t.Fatalf("cancel attempt code=%q", attempt.ErrorCode)
					}
				} else if attempt.ErrorCode != "request_timeout_total" || attempt.ErrorPhase != "total" {
					t.Fatalf("timeout attempt lost phase: %+v", attempt)
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

func TestClassifyErrorDoesNotTreatArbitraryDeadlineAsGatewayTotalTimeout(t *testing.T) {
	if got := ClassifyError(context.DeadlineExceeded); got.Status != 499 || got.Code != "request_canceled" {
		t.Fatalf("unscoped deadline classification changed: %+v", got)
	}
	var timeout interface{ TimeoutPhase() string }
	if errors.As(context.DeadlineExceeded, &timeout) {
		t.Fatal("plain context deadline unexpectedly has timeout phase")
	}
}
