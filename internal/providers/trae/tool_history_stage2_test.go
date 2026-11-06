package trae

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestToolHistoryDiagnosticIsLocalAndDoesNotLeakContent(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected upstream") }))
	_, _, err := c.chatRequest(context.Background(), Credential{}, translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"private-id","type":"function","function":{"name":"private-tool","arguments":"{}"}}]`)}, {Role: "tool", ToolCallID: "different-id", Content: "private-body"}}}, "")
	var e *providers.Error
	if !errors.As(err, &e) || e.Code != "invalid_tool_history" || e.Phase != "request_prepare" || e.Source != "trae_tool_history_validation" || !strings.Contains(e.Message, "matching pending call") || strings.Contains(e.Message, "private-") || strings.Contains(e.Message, "different-id") {
		t.Fatalf("diagnostic=%+v", e)
	}
}
