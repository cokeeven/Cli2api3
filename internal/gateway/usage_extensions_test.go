package gateway

import (
	"github.com/caigee-cmd/cli2api/internal/executor"
	"testing"
)

func TestNonstreamUsagePreservesDetailsWithoutDoubleCounting(t *testing.T) {
	read, prompt := 3, 10
	result := executor.ChatResult{PromptTokens: 10, CompletionTokens: 7, UsageSource: "upstream", CacheReadTokens: &read, UsageExtensions: map[string]any{"prompt_tokens_details": map[string]any{"cached_tokens": 2, "audio_tokens": 1}, "completion_tokens_details": map[string]any{"reasoning_tokens": 6}, "total_tokens": 999}}
	usage := BuildChatUsage(result)
	if usage["total_tokens"] != 17 || usage["prompt_tokens"] != 10 || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != 3 || usage["completion_tokens_details"].(map[string]any)["reasoning_tokens"] != 6 {
		t.Fatalf("usage=%+v", usage)
	}
	if result.UsageExtensions["prompt_tokens_details"].(map[string]any)["cached_tokens"] != 2 {
		t.Fatal("mutated source extensions")
	}
	result.ReportedPromptTokens = &prompt
	usage = BuildChatUsage(result)
	if _, exists := usage["completion_tokens"]; exists {
		t.Fatal("invented missing output usage")
	}
	if _, exists := usage["total_tokens"]; exists {
		t.Fatal("invented partial total")
	}
}

func TestNonstreamCacheWriteIsVisibleToOpenAICompatibleDSHParser(t *testing.T) {
	write := 4
	result := executor.ChatResult{
		PromptTokens: 100, CompletionTokens: 10, CacheWriteTokens: &write,
		UsageExtensions: map[string]any{"prompt_tokens_details": map[string]any{"cache_write_tokens": 2, "audio_tokens": 1}},
	}
	usage := BuildChatUsage(result)
	details, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok || details["cache_write_tokens"] != 4 {
		t.Fatalf("DSH OpenAI parser reads prompt_tokens_details.cache_write_tokens: usage=%+v", usage)
	}
	if usage["cache_write_tokens"] != 4 {
		t.Fatalf("top-level compatibility field lost: usage=%+v", usage)
	}
	if result.UsageExtensions["prompt_tokens_details"].(map[string]any)["cache_write_tokens"] != 2 {
		t.Fatal("mutated provider usage extensions")
	}
}
