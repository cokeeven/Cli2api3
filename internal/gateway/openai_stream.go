package gateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
)

const maxSSELineSize = 16 * 1024 * 1024

type StreamFlushWriter struct {
	W http.ResponseWriter
	F http.Flusher
}

func (w StreamFlushWriter) Write(data []byte) (int, error) {
	n, err := w.W.Write(data)
	if n > 0 {
		w.F.Flush()
	}
	return n, err
}

type StreamRelayStats struct {
	PromptTokens       *int
	CompletionTokens   *int
	CacheReadTokens    *int
	CacheWriteTokens   *int
	CachedTokens       *int
	UsageSource        string
	Credits            *float64
	ConsumedCredits    *float64
	Model              string
	FinishReason       string
	FinishReasonSource string
	FirstTokenAt       *time.Time
	SSEEventCount      int
	BytesRead          int64
	LastEvent          string
	SawDone            bool
	ContentChars       *int
	ReasoningChars     *int
	ToolEventCount     *int
	CharCountUnit      string
	promptSeen         bool
	completionSeen     bool
	cacheReadSeen      bool
	cacheWriteSeen     bool
	creditsSeen        bool
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

type StreamRelayWriteError struct {
	err error
}

func (e *StreamRelayWriteError) Error() string {
	if e == nil || e.err == nil {
		return "stream write error"
	}
	return "stream write error: " + e.err.Error()
}

func (e *StreamRelayWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func RelayOpenAIStream(w http.ResponseWriter, body io.Reader) (stats StreamRelayStats, returnErr error) {
	countedBody := &countingReader{reader: body}
	defer func() { stats.BytesRead = countedBody.bytes }()
	var writer io.Writer = w
	if flusher, ok := w.(http.Flusher); ok {
		writer = StreamFlushWriter{W: w, F: flusher}
	}

	scanner := bufio.NewScanner(countedBody)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineSize)
	sawDone := false
	var frame []string
	var streamErr error
	contentChars, reasoningChars, toolEventCount := 0, 0, 0
	defer func() {
		stats.ContentChars = &contentChars
		stats.ReasoningChars = &reasoningChars
		stats.ToolEventCount = &toolEventCount
		stats.CharCountUnit = "unicode_code_points"
	}()

	flushFrame := func() error {
		if len(frame) == 0 {
			return nil
		}
		eventName, data := parseSSEFrame(frame)
		stats.SSEEventCount++
		stats.LastEvent = eventName
		if classifiedErr := classifyStreamSSEError(eventName, data); classifiedErr != nil {
			if streamErr == nil {
				streamErr = classifiedErr
			}
			if err := writeStructuredStreamError(writer, classifiedErr); err != nil {
				return err
			}
			frame = nil
			return nil
		}
		for _, line := range frame {
			line = strings.TrimSuffix(line, "\r")
			contentDelta, reasoningDelta, toolDelta := streamDeltaMetrics(line)
			contentChars += contentDelta
			reasoningChars += reasoningDelta
			toolEventCount += toolDelta
			if stats.FirstTokenAt == nil && SSEDeltaHasToken(line) {
				now := time.Now()
				stats.FirstTokenAt = &now
			}
			if usage, ok := ParseStreamUsageLine(line); ok {
				stats = mergeStreamUsage(stats, usage)
			}
			if finish, ok := ParseStreamFinishReasonLine(line); ok {
				stats.FinishReason = finish
				stats.FinishReasonSource = "openai_choice_finish_reason"
			}
			if strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
				sawDone = true
			}
		}
		for i, line := range frame {
			frame[i] = normalizeOpenAIStreamUsageLine(line)
		}
		output := strings.Join(frame, "\n") + "\n\n"
		if _, err := io.WriteString(writer, output); err != nil {
			return &StreamRelayWriteError{err: err}
		}
		if sawDone {
			stats.SawDone = true
		}
		frame = nil
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flushFrame(); err != nil {
				return stats, err
			}
			continue
		}
		frame = append(frame, line)
	}
	if err := scanner.Err(); err != nil {
		if streamErr != nil {
			return stats, streamErr
		}
		streamErr := executor.StreamReadError(err)
		if writeErr := writeStructuredStreamError(writer, streamErr); writeErr != nil {
			return stats, writeErr
		}
		return stats, streamErr
	}
	if err := flushFrame(); err != nil {
		return stats, err
	}
	if streamErr != nil {
		return stats, streamErr
	}
	if !sawDone {
		streamErr := executor.StreamIncompleteError()
		if writeErr := writeStructuredStreamError(writer, streamErr); writeErr != nil {
			return stats, writeErr
		}
		return stats, streamErr
	}
	return stats, nil
}

func writeStructuredStreamError(writer io.Writer, err error) error {
	if err == nil {
		return nil
	}
	classified := ClassifyAPIError(err)
	status := classified.Status
	if status == 0 {
		status = http.StatusBadGateway
	}
	code := firstNonEmpty(classified.Code, "upstream_error")
	typ := firstNonEmpty(classified.Type, "api_error")
	message := firstNonEmpty(classified.Message, code)
	errorPayload := map[string]any{
		"message":  message,
		"type":     typ,
		"code":     code,
		"kind":     firstNonEmpty(classified.Kind, accounts.KindUnavailable),
		"status":   status,
		"failover": classified.Failover,
	}
	if classified.RetryAfter > 0 {
		seconds := int(classified.RetryAfter / time.Second)
		if classified.RetryAfter%time.Second != 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}
		errorPayload["retry_after"] = seconds
	}
	payload, marshalErr := json.Marshal(map[string]any{"error": errorPayload})
	if marshalErr != nil {
		return marshalErr
	}
	if _, writeErr := io.WriteString(writer, "data: "+string(payload)+"\n\n"); writeErr != nil {
		return &StreamRelayWriteError{err: writeErr}
	}
	return nil
}

func parseSSEFrame(lines []string) (eventName, data string) {
	eventName = "message"
	dataLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if eventName == "" {
				eventName = "message"
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			dataLines = append(dataLines, value)
		}
	}
	return eventName, strings.Join(dataLines, "\n")
}

func classifyStreamSSEError(eventName, data string) error {
	force := strings.EqualFold(strings.TrimSpace(eventName), "error")
	if !force && !streamJSONLooksLikeError(data) {
		return nil
	}
	status := streamErrorStatus(data)
	body := strings.TrimSpace(data)
	if inner := streamErrorBody(data); inner != "" {
		body = inner
	}
	return executor.ClassifyUpstreamBody(status, body)
}

func streamJSONLooksLikeError(raw string) bool {
	var value any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &value) != nil {
		return false
	}
	return streamValueLooksLikeError(value)
}

func streamValueLooksLikeError(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		if nested, ok := value.(string); ok {
			var parsed any
			return json.Unmarshal([]byte(strings.TrimSpace(nested)), &parsed) == nil && streamValueLooksLikeError(parsed)
		}
		return false
	}
	if _, ok := object["choices"]; ok {
		return false
	}
	if _, ok := object["error"]; ok {
		return true
	}
	if nested, ok := object["body"]; ok {
		if streamValueLooksLikeError(nested) {
			return true
		}
	}
	if status, ok := object["status"].(float64); ok && status >= 400 {
		return true
	}
	for _, key := range []string{"code", "msgCode", "kind"} {
		if _, ok := object[key]; ok {
			return true
		}
	}
	if message, ok := object["message"].(string); ok && strings.TrimSpace(message) != "" {
		return true
	}
	return false
}

func streamErrorBody(raw string) string {
	var value any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &value) != nil {
		return ""
	}
	for {
		switch current := value.(type) {
		case string:
			var nested any
			if json.Unmarshal([]byte(strings.TrimSpace(current)), &nested) != nil {
				return current
			}
			value = nested
		case map[string]any:
			nested, ok := current["body"]
			if !ok {
				encoded, err := json.Marshal(current)
				if err != nil {
					return raw
				}
				return string(encoded)
			}
			value = nested
		default:
			return raw
		}
	}
}

func streamErrorStatus(raw string) int {
	var value any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &value) != nil {
		return 0
	}
	for {
		switch current := value.(type) {
		case map[string]any:
			for _, key := range []string{"status", "statusCode", "statusCodeValue"} {
				if status, ok := current[key].(float64); ok && int(status) >= 400 {
					return int(status)
				}
			}
			if nested, ok := current["body"]; ok {
				value = nested
				continue
			}
			if nested, ok := current["error"]; ok {
				value = nested
				continue
			}
			return 0
		case string:
			var nested any
			if json.Unmarshal([]byte(strings.TrimSpace(current)), &nested) != nil {
				return 0
			}
			value = nested
		default:
			return 0
		}
	}
}

func IsStreamClientDisconnect(err error) bool {
	var writeErr *StreamRelayWriteError
	return errors.As(err, &writeErr)
}

func SSEDeltaHasToken(line string) bool {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return false
	}
	var parsed struct {
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(payload), &parsed) != nil || len(parsed.Choices) == 0 {
		return false
	}
	delta := parsed.Choices[0].Delta
	return jsonHasText(delta.Content) || jsonHasText(delta.ReasoningContent) || jsonHasArray(delta.ToolCalls)
}

func jsonHasText(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var parts []any
	if json.Unmarshal(raw, &parts) == nil {
		return len(parts) > 0
	}
	return true
}

func jsonHasArray(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var parts []any
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	return len(parts) > 0
}

func ParseStreamUsageLine(line string) (StreamRelayStats, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return StreamRelayStats{}, false
	}
	var parsed struct {
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil || len(parsed.Usage) == 0 || string(parsed.Usage) == "null" {
		return StreamRelayStats{}, false
	}
	var usage struct {
		PromptTokens             json.RawMessage `json:"prompt_tokens"`
		CompletionTokens         json.RawMessage `json:"completion_tokens"`
		CacheReadTokens          json.RawMessage `json:"cache_read_tokens"`
		CacheWriteTokens         json.RawMessage `json:"cache_write_tokens"`
		CacheHitTokens           json.RawMessage `json:"prompt_cache_hit_tokens"`
		CacheReadInputTokens     json.RawMessage `json:"cache_read_input_tokens"`
		CacheCreationInputTokens json.RawMessage `json:"cache_creation_input_tokens"`
		Source                   string          `json:"source"`
		Credits                  json.RawMessage `json:"credits"`
		Credit                   json.RawMessage `json:"credit"`
		PromptDetails            json.RawMessage `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(parsed.Usage, &usage); err != nil {
		return StreamRelayStats{}, false
	}
	var details struct {
		CachedTokens json.RawMessage `json:"cached_tokens"`
	}
	if len(usage.PromptDetails) > 0 && string(usage.PromptDetails) != "null" {
		_ = json.Unmarshal(usage.PromptDetails, &details)
	}
	promptTokens, promptSeen := parseUsageInt(usage.PromptTokens)
	completionTokens, completionSeen := parseUsageInt(usage.CompletionTokens)
	cacheRead, cacheReadSeen := parseUsageInt(usage.CacheReadTokens)
	cacheDetails, detailsSeen := parseUsageInt(details.CachedTokens)
	cacheHit, cacheHitSeen := parseUsageInt(usage.CacheHitTokens)
	cacheInput, inputSeen := parseUsageInt(usage.CacheReadInputTokens)
	if cacheRead == nil {
		cacheRead = cacheDetails
	}
	if cacheRead == nil {
		cacheRead = cacheHit
	}
	if cacheRead == nil {
		cacheRead = cacheInput
	}
	cacheReadSeen = cacheReadSeen || detailsSeen || cacheHitSeen || inputSeen
	cacheWrite, cacheWriteSeen := parseUsageInt(usage.CacheWriteTokens)
	cacheCreation, creationSeen := parseUsageInt(usage.CacheCreationInputTokens)
	if cacheWrite == nil {
		cacheWrite = cacheCreation
	}
	cacheWriteSeen = cacheWriteSeen || creationSeen
	credits, creditsSeen := parseUsageFloat(usage.Credits)
	if credits == nil {
		credits, creditsSeen = parseUsageFloat(usage.Credit)
	}
	return StreamRelayStats{
		PromptTokens: promptTokens, CompletionTokens: completionTokens,
		CacheReadTokens: cacheRead, CachedTokens: cacheRead, CacheWriteTokens: cacheWrite,
		UsageSource: firstNonEmpty(usage.Source, "upstream"), Credits: credits, Model: parsed.Model,
		promptSeen: promptSeen, completionSeen: completionSeen,
		cacheReadSeen: cacheReadSeen, cacheWriteSeen: cacheWriteSeen, creditsSeen: creditsSeen,
	}, true
}

func parseUsageInt(raw json.RawMessage) (*int, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil, true
	}
	return &value, true
}

func parseUsageFloat(raw json.RawMessage) (*float64, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil, true
	}
	return &value, true
}

// mergeStreamUsage treats each usage object as the latest cumulative snapshot.
// Fields omitted from a later snapshot retain the previous known value; explicit
// null clears it, and explicit zero replaces it. Values are never added across
// snapshots.
func mergeStreamUsage(current, next StreamRelayStats) StreamRelayStats {
	if next.promptSeen {
		current.PromptTokens, current.promptSeen = next.PromptTokens, true
	}
	if next.completionSeen {
		current.CompletionTokens, current.completionSeen = next.CompletionTokens, true
	}
	if next.cacheReadSeen {
		current.CacheReadTokens, current.CachedTokens, current.cacheReadSeen = next.CacheReadTokens, next.CachedTokens, true
	}
	if next.cacheWriteSeen {
		current.CacheWriteTokens, current.cacheWriteSeen = next.CacheWriteTokens, true
	}
	if next.creditsSeen {
		current.Credits, current.creditsSeen = next.Credits, true
	}
	if next.UsageSource != "" {
		current.UsageSource = next.UsageSource
	}
	if next.Model != "" {
		current.Model = next.Model
	}
	return current
}

func ParseStreamFinishReasonLine(line string) (string, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	var parsed struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if payload == "" || payload == "[DONE]" || json.Unmarshal([]byte(payload), &parsed) != nil {
		return "", false
	}
	for _, choice := range parsed.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return *choice.FinishReason, true
		}
	}
	return "", false
}

func streamDeltaMetrics(line string) (contentChars, reasoningChars, toolEvents int) {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	var parsed struct {
		Choices []struct {
			Delta struct {
				Content          json.RawMessage   `json:"content"`
				ReasoningContent json.RawMessage   `json:"reasoning_content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if payload == "" || payload == "[DONE]" || json.Unmarshal([]byte(payload), &parsed) != nil {
		return 0, 0, 0
	}
	for _, choice := range parsed.Choices {
		contentChars += jsonTextRuneCount(choice.Delta.Content)
		reasoningChars += jsonTextRuneCount(choice.Delta.ReasoningContent)
		toolEvents += len(choice.Delta.ToolCalls)
	}
	return
}

func jsonTextRuneCount(raw json.RawMessage) int {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return utf8.RuneCountInString(text)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		count := 0
		for _, part := range parts {
			count += utf8.RuneCountInString(part.Text)
		}
		return count
	}
	return 0
}

func normalizeOpenAIStreamUsageLine(line string) string {
	dataIndex := strings.Index(line, "data:")
	if dataIndex < 0 {
		return line
	}
	prefix := line[:dataIndex+len("data:")]
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	var root map[string]any
	if payload == "" || payload == "[DONE]" || json.Unmarshal([]byte(payload), &root) != nil {
		return line
	}
	usage, ok := root["usage"].(map[string]any)
	if !ok {
		return line
	}
	stats, ok := ParseStreamUsageLine(line)
	if !ok || (stats.CacheReadTokens == nil && stats.CacheWriteTokens == nil) {
		return line
	}
	if stats.CacheReadTokens != nil {
		usage["cache_read_tokens"] = *stats.CacheReadTokens
		details, _ := usage["prompt_tokens_details"].(map[string]any)
		if details == nil {
			details = map[string]any{}
			usage["prompt_tokens_details"] = details
		}
		details["cached_tokens"] = *stats.CacheReadTokens
	}
	if stats.CacheWriteTokens != nil {
		usage["cache_write_tokens"] = *stats.CacheWriteTokens
		details, _ := usage["prompt_tokens_details"].(map[string]any)
		if details == nil {
			details = map[string]any{}
			usage["prompt_tokens_details"] = details
		}
		// Mirror the top-level compatibility field at the nested location
		// consumed by the currently installed DSH OpenAI Completions parser.
		details["cache_write_tokens"] = *stats.CacheWriteTokens
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return line
	}
	return prefix + " " + string(encoded)
}
