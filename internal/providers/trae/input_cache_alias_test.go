package trae

import "testing"

func TestObservedInputCacheAliasesKeepZeroAndDoNotUseTotal(t *testing.T) {
	for _, value := range []int{0, 9} {
		out, err := outcomeFromAggregate(map[string]any{"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "cache_read_input_tokens": value, "cache_creation_input_tokens": 4, "cache_read_input_tokens_total": 999}}, "m")
		if err != nil || out.CacheReadTokens == nil || *out.CacheReadTokens != value || out.CacheWriteTokens == nil || *out.CacheWriteTokens != 4 || out.PromptTokens != 100 || out.CompletionTokens != 10 {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
}
