package trae

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestNonstreamHTTPFailureKeepsObservedUsageAndContent(t *testing.T) {
	for _, suffix := range []string{"", "event: error\ndata: {\"code\":3004,\"message\":\"synthetic\"}\n\n"} {
		client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "event: output\ndata: {\"response\":\"partial\"}\n\nevent: token_usage\ndata: {\"prompt_tokens\":3,\"completion_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":1}}\n\n"+suffix)
		}))
		credential, _ := (Credential{AccessToken: "synthetic", RefreshToken: "synthetic", UID: "synthetic", Domain: DomainCN, ExpiresAt: 4102444800, MachineID: "synthetic", DeviceID: "synthetic"}).Encode()
		store.items = map[string][]byte{"a": credential}
		out, err := client.ChatNonStream(context.Background(), "a", translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}})
		if err == nil || out.Content != "partial" || out.PromptTokens != 3 || out.CompletionTokens != 2 || out.UsageSource != "upstream" || out.UsageExtensions["completion_tokens_details"] == nil {
			t.Fatalf("partial=%+v error=%v", out, err)
		}
	}
}
