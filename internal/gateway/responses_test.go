package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestRelayOpenAIStreamDoneSurvivesUsageReplacement(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		"",
		`data: {"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	stats, err := RelayOpenAIStream(recorder, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !stats.SawDone || !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("stats=%+v body=%s", stats, recorder.Body.String())
	}
}

func TestStreamLogStatusKeepsCompletedDisconnectSuccessful(t *testing.T) {
	disconnect := &StreamRelayWriteError{err: errors.New("client closed")}
	if got := streamLogStatus(disconnect, true, true); got != accounts.RequestStatusOK {
		t.Fatalf("completed disconnect status=%s", got)
	}
	if got := streamLogStatus(context.Canceled, true, false); got != accounts.RequestStatusOK {
		t.Fatalf("completed context cancel status=%s", got)
	}
	if got := streamLogStatus(context.Canceled, false, true); got != accounts.RequestStatusCanceled {
		t.Fatalf("mid-stream cancel status=%s", got)
	}
	if got := streamLogStatus(errors.New("upstream reset"), true, false); got != accounts.RequestStatusError {
		t.Fatalf("upstream failure status=%s", got)
	}
	if got := streamLogStatus(nil, false, false); got != accounts.RequestStatusOK {
		t.Fatalf("clean stream status=%s", got)
	}
}

func TestResponsesResponseMapsLengthToIncomplete(t *testing.T) {
	response := responsesResponse("req", "model", "", "reasoning", nil, 10, 32, "length", nil, nil)
	if response["status"] != "incomplete" {
		t.Fatalf("status=%v", response["status"])
	}
	details := response["incomplete_details"].(map[string]any)
	if details["reason"] != "max_output_tokens" {
		t.Fatalf("details=%#v", details)
	}
	if responsesRequestStatus("length") != "incomplete" || responsesRequestStatus("stop") != "ok" {
		t.Fatal("request status mapping changed")
	}
}

func TestRelayResponsesStreamEmitsIncomplete(t *testing.T) {
	upstream := strings.NewReader("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":32}}\n\n" +
		"data: [DONE]\n\n")
	var output strings.Builder
	stats, err := RelayResponsesStream(&output, upstream, "req", "model", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FinishReason != "length" {
		t.Fatalf("finish reason=%q", stats.FinishReason)
	}
	body := output.String()
	if !strings.Contains(body, "event: response.incomplete") || strings.Contains(body, "event: response.completed") {
		t.Fatalf("wrong terminal event:\n%s", body)
	}
	var terminal map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event["type"] == "response.incomplete" {
			terminal = event
		}
	}
	if terminal == nil {
		t.Fatal("missing response.incomplete payload")
	}
	response := terminal["response"].(map[string]any)
	if response["status"] != "incomplete" || response["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("response=%#v", response)
	}
}
func TestResponsesUsagePreservesCachedInputTokens(t *testing.T) {
	zero := 0
	read := 64
	cached := 48

	withoutCache := responsesUsage(100, 20, nil, nil)
	if _, ok := withoutCache["input_tokens_details"]; ok {
		t.Fatalf("missing cache usage must not be fabricated: %#v", withoutCache)
	}

	withZero := responsesUsage(100, 20, &zero, nil)
	zeroDetails := withZero["input_tokens_details"].(map[string]any)
	if zeroDetails["cached_tokens"] != 0 {
		t.Fatalf("explicit zero cache usage was lost: %#v", withZero)
	}

	withFallback := responsesUsage(100, 20, nil, &cached)
	fallbackDetails := withFallback["input_tokens_details"].(map[string]any)
	if fallbackDetails["cached_tokens"] != 48 {
		t.Fatalf("cached token fallback mismatch: %#v", withFallback)
	}

	withTopLevel := responsesUsage(100, 20, &read, &cached)
	topLevelDetails := withTopLevel["input_tokens_details"].(map[string]any)
	if topLevelDetails["cached_tokens"] != 64 {
		t.Fatalf("cache_read_tokens must win: %#v", withTopLevel)
	}
	if withTopLevel["total_tokens"] != 120 {
		t.Fatalf("cached input must not be added twice: %#v", withTopLevel)
	}
}

func TestParseStreamUsageLineCacheReadFallback(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage string
		want  *int
	}{
		{name: "detail only", usage: `"prompt_tokens_details":{"cached_tokens":2176}`, want: ptrInt(2176)},
		{name: "explicit zero", usage: `"prompt_tokens_details":{"cached_tokens":0}`, want: ptrInt(0)},
		{name: "top-level wins", usage: `"cache_read_tokens":12,"prompt_tokens_details":{"cached_tokens":2176}`, want: ptrInt(12)},
		{name: "prompt cache hit alias", usage: `"prompt_cache_hit_tokens":9`, want: ptrInt(9)},
		{name: "explicit null falls through to standard detail", usage: `"cache_read_tokens":null,"prompt_tokens_details":{"cached_tokens":0}`, want: ptrInt(0)},
		{name: "unknown stays absent", usage: `"prompt_tokens":16`, want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			stats, ok := ParseStreamUsageLine(`data: {"usage":{` + test.usage + `}}`)
			if !ok {
				t.Fatal("usage not parsed")
			}
			if test.want == nil {
				if stats.CacheReadTokens != nil {
					t.Fatalf("fabricated cache read: %v", *stats.CacheReadTokens)
				}
				return
			}
			if stats.CacheReadTokens == nil || *stats.CacheReadTokens != *test.want {
				t.Fatalf("cache read = %v, want %d", stats.CacheReadTokens, *test.want)
			}
		})
	}
}

func TestRelayOpenAIStreamMergesFinalUsageAndNormalizesCacheAliases(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"推理😀"}}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"答案"}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"lookup","arguments":"{"}},{"index":1,"id":"call_b","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
		"",
		`data: {"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":12,"vendor_extension":{"kept":true}}}`,
		"",
		`data: {"choices":[{"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_read_tokens":null,"prompt_tokens_details":{"cached_tokens":12},"prompt_cache_hit_tokens":11,"vendor_extension":{"final":true}}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	stats, err := RelayOpenAIStream(recorder, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !stats.SawDone || stats.PromptTokens == nil || *stats.PromptTokens != 100 || stats.CompletionTokens == nil || *stats.CompletionTokens != 10 {
		t.Fatalf("latest cumulative usage snapshot not retained: %+v", stats)
	}
	if stats.CacheReadTokens == nil || *stats.CacheReadTokens != 12 || stats.CachedTokens == nil || *stats.CachedTokens != 12 {
		t.Fatalf("cache read alias priority mismatch: %+v", stats)
	}
	if stats.FinishReason != "tool_calls" || stats.FinishReasonSource != "openai_choice_finish_reason" {
		t.Fatalf("finish reason/source=%q/%q", stats.FinishReason, stats.FinishReasonSource)
	}
	if stats.ContentChars == nil || *stats.ContentChars != 2 || stats.ReasoningChars == nil || *stats.ReasoningChars != 3 ||
		stats.ToolEventCount == nil || *stats.ToolEventCount != 2 || stats.CharCountUnit != "unicode_code_points" {
		t.Fatalf("stream character/tool metrics=%+v", stats)
	}
	var usageFrames []map[string]any
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var frame map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil {
			if usage, ok := frame["usage"].(map[string]any); ok {
				usageFrames = append(usageFrames, usage)
			}
		}
	}
	if len(usageFrames) != 2 {
		t.Fatalf("usage frame count=%d body=%s", len(usageFrames), recorder.Body.String())
	}
	for i, usage := range usageFrames {
		if usage["cache_read_tokens"] != float64(12) || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(12) {
			t.Fatalf("usage frame %d lacks normalized standard cache count: %#v", i, usage)
		}
	}
	if usageFrames[0]["vendor_extension"].(map[string]any)["kept"] != true || usageFrames[1]["vendor_extension"].(map[string]any)["final"] != true {
		t.Fatalf("upstream usage extensions were not preserved: %#v", usageFrames)
	}
}

func TestDecodeOpenAIToolCallsDropsInvalidArguments(t *testing.T) {
	calls := decodeOpenAIToolCalls(json.RawMessage(`[
		{"id":"call_bad","function":{"name":"mcp__fastctx__read","arguments":"{\"path\":\"x\",\"error_retry:: 240}"}},
		{"id":"call_good","function":{"name":"mcp__fastctx__grep","arguments":"{\"pattern\":\"x\"}"}}
	]`))
	if len(calls) != 1 || calls[0].ID != "call_good" {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestRelayResponsesStreamRejectsInvalidToolArgumentsBeforeFinalize(t *testing.T) {
	upstream := strings.NewReader(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_bad","function":{"name":"mcp__fastctx__read","arguments":"{\"path\":\"x\",\"error_retry:: 240}"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"finish_reason":"tool_calls"}]}` + "\n\n" +
			"data: [DONE]\n\n",
	)
	var output strings.Builder
	_, err := RelayResponsesStream(&output, upstream, "req", "model", nil)
	if err == nil || !strings.Contains(err.Error(), "arguments are invalid JSON") {
		t.Fatalf("error=%v", err)
	}
	body := output.String()
	for _, event := range []string{
		"event: response.function_call_arguments.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if strings.Contains(body, event) {
			t.Fatalf("invalid tool call was finalized with %q:\n%s", event, body)
		}
	}
}
