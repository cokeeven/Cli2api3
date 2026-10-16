package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

// platformCase is one account family this round is responsible for: Qoder
// global/CN, WorkBuddy global/CN, and Trae CN.
type platformCase struct {
	name     string
	provider string
	region   string
	model    string
}

func platformMatrix() []platformCase {
	return []platformCase{
		{"qoder-global", "qoder", "global", "qwen3.7-max"},
		{"qoder-cn", "qoder", "cn", "qwen3.7-max"},
		{"workbuddy-global", "workbuddy", "global", "glm-5.2"},
		{"workbuddy-cn", "workbuddy", "cn", "glm-5.2"},
		{"trae-cn", "trae", "cn", "claude-sonnet-4"},
	}
}

// recordingChat is a simulated upstream for one platform. It records the
// request bodies it saw, so the tests can assert what actually left the
// executor for each family.
type recordingChat struct {
	requests []translate.ChatRequest
	outcome  providers.ChatOutcome
	err      error
}

func (c *recordingChat) ChatNonStream(_ context.Context, _ string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return providers.ChatOutcome{}, c.err
	}
	out := c.outcome
	if out.Content == "" {
		out = providers.ChatOutcome{Content: "ok", UsageSource: "upstream", PromptTokens: 2, CompletionTokens: 1}
	}
	return out, nil
}

func (c *recordingChat) ChatStream(_ context.Context, _ string, req translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return nil, providers.ResolvedChat{}, c.err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, providers.ResolvedChat{}, nil
}

func platformExecutor(tc platformCase, chat providers.Adapter) (ChatExecutor, *Pool) {
	pool := NewPool(nil, nil)
	pool.Upsert(Item{
		ID: tc.name, Provider: tc.provider, Region: tc.region, Runtime: "in_process",
		MaxInFlight: 2, Models: []string{tc.model},
	})
	registry := providers.NewRegistry()
	if chat.ID == "" {
		chat.ID = tc.provider
	}
	registry.Register(chat)
	e := NewChatExecutor(pool, "")
	e.Providers = registry
	return e, pool
}

// Every platform in scope must serve a plain non-stream chat and a stream
// through the same executor path.
func TestPlatformMatrixChatAndStream(t *testing.T) {
	for _, tc := range platformMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			chat := &recordingChat{}
			e, _ := platformExecutor(tc, providers.Adapter{Chat: chat})
			result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{
				Model: tc.model, Messages: []translate.ChatMessage{{Role: "user", Content: "hi"}},
			}, "", tc.provider)
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			if result.Content != "ok" || result.AccountID != tc.name {
				t.Fatalf("result = %+v", result)
			}
			stream, err := e.ChatStreamProxy(context.Background(), translate.ChatRequest{
				Model: tc.model, Stream: true, Messages: []translate.ChatMessage{{Role: "user", Content: "hi"}},
			}, "", tc.provider)
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			if _, err := io.ReadAll(stream.Response.Body); err != nil {
				t.Fatal(err)
			}
			stream.Finalize(StreamCompletion{Completed: true, PromptTokens: ptrInt(2), CompletionTokens: ptrInt(1), UsageSource: "upstream"})
			if len(chat.requests) != 2 {
				t.Fatalf("simulated upstream saw %d requests", len(chat.requests))
			}
		})
	}
}

// Tool results in a multi-turn conversation must reach every in-scope
// platform unchanged: the executor may translate, but it must not drop the
// assistant tool call or the tool result that answers it.
func TestPlatformMatrixToolCallMultiTurnReachesUpstream(t *testing.T) {
	for _, tc := range platformMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			chat := &recordingChat{outcome: providers.ChatOutcome{
				Content: "done", ToolCalls: []byte(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]`),
				FinishReason: "tool_calls", UsageSource: "upstream",
			}}
			e, _ := platformExecutor(tc, providers.Adapter{Chat: chat})
			_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{
				Model: tc.model, Messages: []translate.ChatMessage{
					{Role: "user", Content: "weather?"},
					{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]`)},
					{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
					{Role: "user", Content: "thanks"},
				},
			}, "", tc.provider)
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			if len(chat.requests) != 1 {
				t.Fatalf("requests = %d", len(chat.requests))
			}
			roles := translate.MessageRoles(chat.requests[0].Messages)
			want := []string{"user", "assistant", "tool", "user"}
			if strings.Join(roles, ",") != strings.Join(want, ",") {
				t.Fatalf("roles = %v, want %v", roles, want)
			}
			if chat.requests[0].Messages[2].ToolCallID != "call_1" {
				t.Fatalf("tool result lost its call id: %+v", chat.requests[0].Messages[2])
			}
		})
	}
}

// A rate-limited platform must be cooled and, when another account on the
// same platform exists, the request must fail over instead of surfacing the
// upstream error.
func TestPlatformMatrixRateLimitCoolsAndFailsOver(t *testing.T) {
	for _, tc := range platformMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			limited := &firstAccountRateLimitedChat{}
			pool := NewPool(nil, nil)
			pool.Upsert(Item{ID: tc.name + "-1", Provider: tc.provider, Region: tc.region, Runtime: "in_process", MaxInFlight: 2, Models: []string{tc.model}})
			pool.Upsert(Item{ID: tc.name + "-2", Provider: tc.provider, Region: tc.region, Runtime: "in_process", MaxInFlight: 2, Models: []string{tc.model}})
			registry := providers.NewRegistry()
			registry.Register(providers.Adapter{ID: tc.provider, Chat: limited})
			e := NewChatExecutor(pool, "")
			e.Providers = registry

			result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: tc.model}, "", tc.provider)
			if err != nil {
				t.Fatalf("expected failover, got %v", err)
			}
			if result.AttemptCount < 2 {
				t.Fatalf("attempts = %d, want failover", result.AttemptCount)
			}
			// A rate limit is recorded as a cooldown: account-wide, or
			// model-scoped when the upstream did not declare a wider scope.
			cooled := false
			for _, item := range pool.Items() {
				if !item.DownUntil.IsZero() || !item.ModelDownUntil[tc.model].IsZero() {
					cooled = true
				}
			}
			if !cooled {
				t.Fatal("rate-limited account was not cooled")
			}
		})
	}
}

// firstAccountRateLimitedChat simulates an upstream that rate-limits one
// account of the platform and serves the other.
type firstAccountRateLimitedChat struct {
	mu      sync.Mutex
	limited map[string]bool
}

func (f *firstAccountRateLimitedChat) ChatNonStream(_ context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	f.mu.Lock()
	if f.limited == nil {
		f.limited = map[string]bool{}
	}
	first := f.limited[accountID]
	if !first && len(f.limited) == 0 {
		f.limited[accountID] = true
		first = true
	}
	f.mu.Unlock()
	if first {
		return providers.ChatOutcome{}, &providers.Error{Kind: accounts.KindRateLimit, Status: 429, Message: "soft_rate"}
	}
	return providers.ChatOutcome{Model: req.Model, Content: "OK-" + accountID, FinishReason: "stop"}, nil
}

func (f *firstAccountRateLimitedChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, errors.New("stream unsupported in fake")
}

// Interrupting a stream (client disconnect) must release the account slot,
// must not cool a healthy account, and must never settle the attempt as a
// success.
func TestPlatformMatrixStreamInterruptReleasesAndDoesNotSucceed(t *testing.T) {
	for _, tc := range platformMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewPool(nil, nil)
			pool.Upsert(Item{ID: tc.name, Provider: tc.provider, Region: tc.region, Runtime: "in_process", MaxInFlight: 1, Models: []string{tc.model}})
			registry := providers.NewRegistry()
			registry.Register(providers.Adapter{ID: tc.provider, Chat: admissionChat{}})
			e := NewChatExecutor(pool, "")
			e.Providers = registry
			var attempts []accounts.RequestAttempt
			e.OnAttempt = func(a accounts.RequestAttempt) { attempts = append(attempts, a) }

			ctx, cancel := context.WithCancel(context.Background())
			stream, err := e.ChatStreamProxy(ctx, translate.ChatRequest{Model: tc.model, Stream: true}, "", tc.provider)
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			// The client goes away mid-stream.
			cancel()
			_ = stream.Response.Body.Close()
			stream.Finalize(StreamCompletion{Err: context.Canceled})

			deadline := time.Now().Add(2 * time.Second)
			for {
				item, _ := pool.ByID(tc.name)
				if item.LiveInFlight == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("interrupt leaked capacity: %+v", item)
				}
				time.Sleep(time.Millisecond)
			}
			item, _ := pool.ByID(tc.name)
			if !item.DownUntil.IsZero() {
				t.Fatal("client interrupt cooled a healthy account")
			}
			for _, attempt := range attempts {
				if attempt.Status == accounts.AttemptStatusOK {
					t.Fatalf("interrupted stream was recorded as success: %+v", attempt)
				}
			}
		})
	}
}

// A platform error that the provider classifies as a hard rejection must not
// be retried on another account of the same platform.
func TestPlatformMatrixHardRejectionDoesNotFailover(t *testing.T) {
	for _, tc := range platformMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			reject := &recordingChat{err: &providers.Error{Kind: accounts.KindInvalidRequest, Status: 400, Message: "sensitive content rejected"}}
			pool := NewPool(nil, nil)
			pool.Upsert(Item{ID: tc.name + "-1", Provider: tc.provider, Region: tc.region, Runtime: "in_process", MaxInFlight: 2, Models: []string{tc.model}})
			pool.Upsert(Item{ID: tc.name + "-2", Provider: tc.provider, Region: tc.region, Runtime: "in_process", MaxInFlight: 2, Models: []string{tc.model}})
			registry := providers.NewRegistry()
			registry.Register(providers.Adapter{ID: tc.provider, Chat: reject})
			e := NewChatExecutor(pool, "")
			e.Providers = registry

			_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: tc.model}, "", tc.provider)
			if err == nil {
				t.Fatal("expected the rejection to surface")
			}
			if len(reject.requests) != 1 {
				t.Fatalf("hard rejection retried on another account: %d attempts", len(reject.requests))
			}
		})
	}
}
