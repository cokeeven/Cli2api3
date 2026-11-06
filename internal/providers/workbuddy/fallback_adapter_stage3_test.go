package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"net/http"
	"testing"
)

func TestAlternativeAdapterRequiresSelectedAccountEvidence(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected network") }))
	info := providers.ModelInfo{NativeModel: "m", PublicModel: "m", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"high"}}, FallbackEvidence: &providers.FallbackCapabilities{ContextWindow: 8192, MaxOutput: 100, Tools: false}}
	c.rememberCatalogFor("a", []providers.ModelInfo{info})
	ctx := providers.WithFallbackValidation(context.Background())
	req := translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("20"), Tools: json.RawMessage(`[{"type":"function","function":{"name":"f"}}]`)}
	_, _, err := c.chatRequest(ctx, "a", Credential{}, req)
	var classified *providers.Error
	if !errors.As(err, &classified) || classified.Code != "fallback_capability_unknown" {
		t.Fatalf("unknown tools must reject before outbound: %v", err)
	}
	info.FallbackEvidence.Tools = true
	c.rememberCatalogFor("a", []providers.ModelInfo{info})
	_, _, err = c.chatRequest(ctx, "a", Credential{}, req)
	if err != nil {
		t.Fatalf("explicitly supported tools rejected: %v", err)
	}
}
