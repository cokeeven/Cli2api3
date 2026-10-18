package providers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestResolveOutputLimit(t *testing.T) {
	for _, test := range []struct {
		name    string
		req     translate.ChatRequest
		caps    ModelCapabilities
		max     bool
		want    *int
		source  string
		wantErr string
	}{
		{name: "legacy mapped", req: translate.ChatRequest{MaxTokens: json.RawMessage(`120`)}, caps: ModelCapabilities{MaxOutput: 256}, want: intPtr(120), source: "caller_max_tokens"},
		{name: "modern mapped to upstream max tokens", req: translate.ChatRequest{MaxCompletionTokens: json.RawMessage(`80`)}, caps: ModelCapabilities{MaxOutput: 256}, want: intPtr(80), source: "caller_max_completion_tokens_mapped_to_max_tokens"},
		{name: "missing leaves provider default unknown", req: translate.ChatRequest{}, caps: ModelCapabilities{MaxOutput: 256}, source: "upstream_default_unknown"},
		{name: "null is unset", req: translate.ChatRequest{MaxTokens: json.RawMessage(`null`)}, source: "upstream_default_unknown"},
		{name: "both rejected", req: translate.ChatRequest{MaxTokens: json.RawMessage(`10`), MaxCompletionTokens: json.RawMessage(`20`)}, wantErr: "cannot both be set"},
		{name: "both supplied with one null rejected consistently", req: translate.ChatRequest{MaxTokens: json.RawMessage(`null`), MaxCompletionTokens: json.RawMessage(`20`)}, wantErr: "cannot both be set"},
		{name: "zero rejected", req: translate.ChatRequest{MaxTokens: json.RawMessage(`0`)}, wantErr: "positive integer"},
		{name: "fraction rejected", req: translate.ChatRequest{MaxTokens: json.RawMessage(`1.5`)}, wantErr: "positive integer"},
		{name: "string rejected", req: translate.ChatRequest{MaxTokens: json.RawMessage(`"12"`)}, wantErr: "positive integer"},
		{name: "above known output cap rejected", req: translate.ChatRequest{MaxTokens: json.RawMessage(`300`)}, caps: ModelCapabilities{MaxOutput: 256}, wantErr: "catalog maximum"},
		{name: "max mode catalog cap", req: translate.ChatRequest{MaxTokens: json.RawMessage(`300`)}, caps: ModelCapabilities{MaxOutput: 256, MaxOutputMax: 512, MaxMode: true}, max: true, want: intPtr(300), source: "caller_max_tokens"},
		{name: "unknown model maximum does not invent a cap", req: translate.ChatRequest{MaxTokens: json.RawMessage(`128000`)}, want: intPtr(128000), source: "caller_max_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, source, err := ResolveOutputLimit(test.req, test.caps, test.max)
			if test.wantErr != "" {
				var providerErr *Error
				if !errors.As(err, &providerErr) || providerErr.Kind != "invalid_request" || !contains(providerErr.Message, test.wantErr) {
					t.Fatalf("error = %v, want invalid request containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if source != test.source {
				t.Fatalf("source = %q, want %q", source, test.source)
			}
			if test.want == nil {
				if got != nil {
					t.Fatalf("limit = %d, want unset", *got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("limit = %v, want %d", got, *test.want)
			}
		})
	}
}

func intPtr(value int) *int { return &value }

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return substring == ""
}
