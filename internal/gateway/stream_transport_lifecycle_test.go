package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

type observedTransport struct {
	io.ReadCloser
	once   sync.Once
	closed chan struct{}
}

func (b *observedTransport) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.ReadCloser.Close()
}

type blockedTerminalWriter struct {
	*httptest.ResponseRecorder
	blocked chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (w *blockedTerminalWriter) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "event: error") {
		w.once.Do(func() { close(w.blocked) })
		<-w.resume
	}
	return w.ResponseRecorder.Write(data)
}

func TestCompatibilityStreamClosesUpstreamBeforeBlockingErrorOutput(t *testing.T) {
	for _, protocol := range []struct {
		path, body string
		serve      func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"/v1/messages", `{"model":"workbuddy/lifecycle-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":true}`, (*Handler).HandleAnthropicMessages},
		{"/v1/responses", `{"model":"workbuddy/lifecycle-model","input":"hello","max_output_tokens":32,"stream":true}`, (*Handler).HandleResponses},
	} {
		t.Run(protocol.path, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			transport := &observedTransport{ReadCloser: reader, closed: make(chan struct{})}
			defer transport.Close()
			pool := executor.NewPool(nil, nil)
			pool.Upsert(executor.Item{ID: "account", Provider: "workbuddy", Region: "cn", Runtime: "inprocess", MaxInFlight: 1, Models: []string{"lifecycle-model"}})
			e := executor.NewChatExecutor(pool, "")
			e.Providers = providers.NewRegistry()
			e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: lifecycleChat{body: transport}})
			budgets := &lifecycleBudget{}
			e.Budgets = budgets
			h := &Handler{Executor: e, Pool: pool}
			ctx := auth.WithIdentity(context.Background(), auth.Identity{Kind: auth.KindKey, KeyID: "key", AllowedProviders: []string{"workbuddy:cn"}})
			r := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.body)).WithContext(ctx)
			output := &blockedTerminalWriter{ResponseRecorder: httptest.NewRecorder(), blocked: make(chan struct{}), resume: make(chan struct{})}
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(output.resume) }) }
			defer resume()
			done := make(chan struct{})
			go func() { defer close(done); protocol.serve(h, output, r) }()
			written := make(chan error, 1)
			go func() {
				_, err := io.WriteString(writer, "event: error\ndata: {\"error\":{\"message\":\"invalid request\",\"code\":\"invalid_request\",\"status\":400}}\n\n")
				written <- err
			}()
			select {
			case <-output.blocked:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal error write did not block")
			}
			// The relay returned on the error frame while the source pipe was
			// deliberately left open. A slow client must not keep that source
			// generating after its concurrency slot has already been released.
			select {
			case <-transport.closed:
			default:
				t.Error("upstream transport is still open after terminal finalization")
			}
			item, _ := pool.ByID("account")
			if item.LiveInFlight != 0 {
				t.Errorf("terminal admission not released: %d", item.LiveInFlight)
			}
			budgets.mu.Lock()
			n := len(budgets.settled)
			budgets.mu.Unlock()
			if n != 1 {
				t.Errorf("terminal settlements=%d", n)
			}
			resume()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not exit")
			}
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("source writer did not exit")
			}
		})
	}
}
