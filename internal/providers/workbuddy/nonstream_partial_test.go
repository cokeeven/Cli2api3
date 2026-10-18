package workbuddy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestNonstreamHTTPFailureKeepsObservedUsageAndContent(t *testing.T) {
	for _, suffix := range []string{"", "data: {\"error\":{\"message\":\"synthetic business failure\"}}\n\n"} {
		client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"+suffix)
		}))
		credential, _ := (Credential{AccessToken: "synthetic", UID: "synthetic", Domain: "codebuddy.cn", ExpiresAt: 4102444800}).Encode()
		store.items = map[string][]byte{"a": credential}
		out, err := client.ChatNonStream(context.Background(), "a", translate.ChatRequest{Model: "m", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic"}}})
		if err == nil || out.Content != "partial" || out.PromptTokens != 3 || out.CompletionTokens != 2 || out.UsageSource != "upstream" || out.UsageExtensions["completion_tokens_details"] == nil {
			t.Fatalf("partial=%+v error=%v", out, err)
		}
	}
}

func TestStreamSanitizerReportsBusinessError(t *testing.T) {
	var output strings.Builder
	if err := copySanitizedSSE(strings.NewReader("data: {\"error\":{\"message\":\"synthetic\"}}\n\n"), &output); err == nil {
		t.Fatal("business error erased", err)
	}
}
