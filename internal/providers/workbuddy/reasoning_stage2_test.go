package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestReasoningUsesAttemptAccountCatalog(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected upstream call") }))
	c.rememberCatalogFor("one", []providers.ModelInfo{{NativeModel: "m", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"low", "medium"}, ReasoningDefault: "low"}}})
	c.rememberCatalogFor("two", []providers.ModelInfo{{NativeModel: "m", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"high"}, ReasoningDefault: "high"}}})
	for _, tc := range []struct{ id, want string }{{"one", "medium"}, {"two", "high"}} {
		h, res, err := c.chatRequest(context.Background(), tc.id, Credential{}, translate.ChatRequest{Model: "m", ReasoningEffort: json.RawMessage(`"high"`)})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = json.NewDecoder(h.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		obj, _ := body["reasoning"].(map[string]any)
		if res.ReasoningLevel != tc.want || obj["effort"] != tc.want {
			t.Fatalf("%s: %+v", tc.id, body)
		}
	}
}

func TestCatalogDoesNotCrossCredentialGeneration(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected upstream call") }))
	c.rememberCatalogForEvidence("a", []providers.ModelInfo{{NativeModel: "m", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"high"}}}}, providers.AttemptCredentialEvidence{Valid: true, Generation: 1})
	ctx := providers.WithCredentialEvidence(context.Background(), "a")
	providers.RecordCredentialEvidence(ctx, "a", providers.AttemptCredentialEvidence{Observed: true, Valid: true, Generation: 2})
	if _, ok := c.accountCatalogModel(ctx, "a", "m"); ok {
		t.Fatal("stale generation catalog used")
	}
	providers.RecordCredentialEvidence(ctx, "a", providers.AttemptCredentialEvidence{Observed: true, Valid: true, Generation: 1})
	if _, ok := c.accountCatalogModel(ctx, "a", "m"); !ok {
		t.Fatal("matching generation missing")
	}
}
