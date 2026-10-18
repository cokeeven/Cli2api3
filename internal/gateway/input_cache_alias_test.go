package gateway

import (
	"encoding/json"
	"testing"
)

func TestInputCacheAliasesPreserveExplicitCountsAndIgnoreTotalAliases(t *testing.T) {
	for _, value := range []string{"0", "9"} {
		line := `data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_read_input_tokens":` + value + `,"cache_creation_input_tokens":4,"cache_read_input_tokens_total":999}}`
		got, ok := ParseStreamUsageLine(line)
		if !ok || got.CacheReadTokens == nil || got.CacheWriteTokens == nil || *got.CacheWriteTokens != 4 {
			t.Fatalf("missing explicit cache aliases: %+v", got)
		}
		want := 0
		if value == "9" {
			want = 9
		}
		if *got.CacheReadTokens != want || *got.PromptTokens != 100 || *got.CompletionTokens != 10 {
			t.Fatal("aliases inflated usage")
		}
	}
	got, _ := ParseStreamUsageLine(`data: {"usage":{"cache_read_input_tokens_total":999,"cache_creation_input_tokens_total":888}}`)
	if got.CacheReadTokens != nil || got.CacheWriteTokens != nil {
		t.Fatal("unverified total aliases were interpreted")
	}
	got, _ = ParseStreamUsageLine(`data: {"usage":{"cache_read_tokens":0,"prompt_tokens_details":{"cached_tokens":5},"prompt_cache_hit_tokens":7,"cache_read_input_tokens":9,"cache_write_tokens":0,"cache_creation_input_tokens":4}}`)
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 0 || got.CacheWriteTokens == nil || *got.CacheWriteTokens != 0 {
		t.Fatal("legacy aliases overrode explicit canonical zero")
	}
	got, _ = ParseStreamUsageLine(`data: {"usage":{"cache_read_tokens":null,"prompt_tokens_details":{"cached_tokens":null},"prompt_cache_hit_tokens":null,"cache_read_input_tokens":9,"cache_write_tokens":null,"cache_creation_input_tokens":4}}`)
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 9 || got.CacheWriteTokens == nil || *got.CacheWriteTokens != 4 {
		t.Fatal("null prevented valid alias fallback")
	}
}

func TestInputCacheWriteAliasIsNormalizedWithoutInventingRead(t *testing.T) {
	line := normalizeOpenAIStreamUsageLine(`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_creation_input_tokens":4}}`)
	var root struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal([]byte(line[6:]), &root); err != nil {
		t.Fatal(err)
	}
	if root.Usage["cache_write_tokens"] != float64(4) {
		t.Fatalf("write alias not normalized: %s", line)
	}
	details, ok := root.Usage["prompt_tokens_details"].(map[string]any)
	if !ok || details["cache_write_tokens"] != float64(4) {
		t.Fatalf("write alias not normalized where DSH reads it: %s", line)
	}
	if _, exists := root.Usage["cache_read_tokens"]; exists {
		t.Fatal("invented cache read")
	}
}
