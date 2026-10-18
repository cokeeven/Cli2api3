package gateway

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type requestPathCapture struct{ started []accounts.RequestLog }

func (c *requestPathCapture) Start(entry accounts.RequestLog) { c.started = append(c.started, entry) }

func TestNormalizedRequestPathUsesAllowlistedPublicRoutes(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/v1/chat/completions", want: "openai_chat_completions"},
		{path: "/v1/responses", want: "openai_responses"},
		{path: "/v1/messages", want: "anthropic_messages"},
		{path: "/v1/chat/completions/private", want: ""},
		{path: "/v1/chat/completions?key=secret", want: ""},
	}
	for _, test := range tests {
		if got := normalizedRequestPath(test.path); got != test.want {
			t.Errorf("normalizedRequestPath(%q)=%q, want %q", test.path, got, test.want)
		}
	}
}

func TestPrepareChatExecutionPersistsAllowlistedRequestPath(t *testing.T) {
	logs := &requestPathCapture{}
	handler := &Handler{Executor: executor.NewChatExecutor(executor.NewPool(nil, nil), ""), Logs: logs}
	req := httptest.NewRequest("POST", "/v1/chat/completions?ignored=query", nil)
	identity := auth.KeyIdentity(accounts.APIKey{ID: "synthetic-key", Name: "synthetic", Providers: []string{"workbuddy"}, Enabled: true})
	req = req.WithContext(auth.WithIdentity(context.Background(), identity))
	if _, err := handler.PrepareChatExecution(req, translate.ChatRequest{Model: "workbuddy/glm-5.3", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}}); err != nil {
		t.Fatal(err)
	}
	if len(logs.started) != 1 || logs.started[0].RequestPath != "openai_chat_completions" {
		t.Fatalf("gateway-to-request-log path = %+v", logs.started)
	}
}
