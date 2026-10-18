package gateway

import (
	"fmt"
	"net/http"
	"time"

	"github.com/caigee-cmd/cli2api/internal/executor"
)

func BuildChatUsage(res executor.ChatResult) map[string]any {
	out := map[string]any{
		"prompt_tokens":     res.PromptTokens,
		"completion_tokens": res.CompletionTokens,
		"total_tokens":      res.PromptTokens + res.CompletionTokens,
		"source":            firstNonEmpty(res.UsageSource, "estimate"),
	}
	for key, value := range res.UsageExtensions {
		if _, exists := out[key]; !exists {
			out[key] = value
		}
	}
	if res.ReportedPromptTokens != nil || res.ReportedCompletionTokens != nil {
		if res.ReportedPromptTokens == nil {
			delete(out, "prompt_tokens")
			delete(out, "total_tokens")
		}
		if res.ReportedCompletionTokens == nil {
			delete(out, "completion_tokens")
			delete(out, "total_tokens")
		}
	}
	if res.CacheReadTokens != nil {
		out["cache_read_tokens"] = *res.CacheReadTokens
	}
	if res.CacheWriteTokens != nil {
		out["cache_write_tokens"] = *res.CacheWriteTokens
	}
	details := map[string]any{}
	if existing, ok := out["prompt_tokens_details"].(map[string]any); ok {
		for key, value := range existing {
			details[key] = value
		}
	}
	cachedTokens := res.CachedTokens
	if cachedTokens == nil {
		cachedTokens = res.CacheReadTokens
	}
	if cachedTokens != nil {
		details["cached_tokens"] = *cachedTokens
	}
	if res.CacheWriteTokens != nil {
		// Current DSH's OpenAI Completions parser reads the cache-write
		// extension from prompt_tokens_details, while other clients may use
		// the existing top-level compatibility field above.
		details["cache_write_tokens"] = *res.CacheWriteTokens
	}
	if len(details) > 0 {
		out["prompt_tokens_details"] = details
	}
	if res.Credits != nil {
		out["credits"] = *res.Credits
	}
	return out
}

func ClassifyAPIError(err error) executor.Classified { return executor.ClassifyError(err) }

func WriteClassifiedErr(w http.ResponseWriter, err error) {
	if err == nil {
		writeErr(w, http.StatusServiceUnavailable, "upstream_not_ready", "upstream not ready")
		return
	}
	classified := ClassifyAPIError(err)
	if classified.RetryAfter > 0 {
		seconds := int(classified.RetryAfter / time.Second)
		if classified.RetryAfter%time.Second != 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	}
	w.Header().Set("X-Qoder-Error-Kind", classified.Kind)
	if classified.Failover {
		w.Header().Set("X-Qoder-Failover", "1")
	} else {
		w.Header().Set("X-Qoder-Failover", "0")
	}
	writeErr(w, classified.Status, classified.Code, classified.Message)
}
