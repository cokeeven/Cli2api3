package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

// maxChatRequestBytes bounds an inbound /v1/chat/completions or /v1/messages
// body. Chat requests carry the full message history, so this stays generous
// (64 MiB, same as translate.MaxNativeRequestBytes) — it exists to stop a
// pathological body from pinning unbounded memory, not to constrain prompts.
const maxChatRequestBytes = 64 << 20

type requestLogMetadata struct {
	OutputLimit          *int
	OutputLimitSource    string
	OutputBudgetEvidence *providers.OutputBudgetEvidence
	PromptStripped       *bool
	FinishReason         string
	FinishReasonSource   string
}

type streamDiagnosticTiming struct {
	PreparedAt              time.Time
	UpstreamStartedAt       time.Time
	UpstreamResponseAt      time.Time
	OutputBudgetEnforcement string
}

func (h *Handler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	r = withGatewayRequestStart(r)
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	var req translate.ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatRequestBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "messages required")
		return
	}
	if err := translate.ValidateChatRequest(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	execution, err := h.PrepareChatExecution(r, req)
	if err != nil {
		writeChatHTTPError(w, err)
		return
	}
	req = execution.Request
	publicModel := execution.PublicModel
	prefer := execution.Prefer
	providerFilter := execution.ProviderFilter
	requestID := execution.RequestID
	started := execution.Started
	ctx := execution.Context
	preparedAt := time.Now()
	w.Header().Set("X-Request-Id", requestID)

	if req.Stream {
		upstreamStartedAt := time.Now()
		upstream, err := h.Executor.ChatStreamProxy(ctx, req, prefer, providerFilter)
		upstreamResponseAt := time.Now()
		if err != nil {
			h.finishRequestLog(requestID, started, req, publicModel, upstream.AccountID, firstNonEmpty(upstream.Provider, providerFilter), upstream.Routing, accounts.RequestStatusError, upstream.TTFBMs, nil, err, upstream.AttemptCount, upstream.ReasoningLevel, requestLogMetadata{
				OutputLimit: upstream.OutputLimit, OutputLimitSource: upstream.OutputLimitSource, OutputBudgetEvidence: upstream.OutputBudgetEvidence, PromptStripped: upstream.PromptStripped,
			})
			WriteClassifiedErr(w, err)
			return
		}
		defer upstream.Response.Body.Close()
		h.finishRequestLog(requestID, started, req, publicModel, upstream.AccountID, firstNonEmpty(upstream.Provider, providerFilter), upstream.Routing, accounts.RequestStatusStreaming, upstream.TTFBMs, nil, nil, upstream.AttemptCount, upstream.ReasoningLevel, requestLogMetadata{
			OutputLimit: upstream.OutputLimit, OutputLimitSource: upstream.OutputLimitSource, OutputBudgetEvidence: upstream.OutputBudgetEvidence, PromptStripped: upstream.PromptStripped,
		})
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		if upstream.AccountID != "" {
			w.Header().Set("X-Qoder-Account", upstream.AccountID)
			w.Header().Set("X-CLI2API-Account", upstream.AccountID)
		}
		if provider := firstNonEmpty(upstream.Provider, providerFilter); provider != "" {
			w.Header().Set("X-CLI2API-Provider", provider)
		}
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		stats, relayErr := RelayOpenAIStream(w, upstream.Response.Body)
		status := streamLogStatus(relayErr, stats.SawDone, r.Context().Err() != nil)
		budgetState := outputBudgetEnforcement(firstNonEmpty(upstream.Provider, providerFilter), upstream.OutputBudgetEvidence, upstream.OutputLimit, stats.CompletionTokens, stats.UsageSource)
		h.recordStreamDiagnostic(requestID, upstream.Response, started, stats, relayErr, r.Context().Err(), streamDiagnosticTiming{
			PreparedAt: preparedAt, UpstreamStartedAt: upstreamStartedAt, UpstreamResponseAt: upstreamResponseAt, OutputBudgetEnforcement: budgetState,
		})
		ttfb := upstream.TTFBMs
		if stats.FirstTokenAt != nil {
			ttfb = int(stats.FirstTokenAt.Sub(started).Milliseconds())
			if ttfb < 1 {
				ttfb = 1
			}
		}
		logErr := relayErr
		switch status {
		case accounts.RequestStatusCanceled:
			if logErr == nil || IsStreamClientDisconnect(logErr) {
				logErr = context.Canceled
			}
		case accounts.RequestStatusOK:
			logErr = nil
		}
		h.finishRequestLog(requestID, started, req, publicModel, upstream.AccountID, firstNonEmpty(upstream.Provider, providerFilter), upstream.Routing, status, ttfb, &stats, logErr, upstream.AttemptCount, upstream.ReasoningLevel, requestLogMetadata{
			OutputLimit: upstream.OutputLimit, OutputLimitSource: upstream.OutputLimitSource, OutputBudgetEvidence: upstream.OutputBudgetEvidence, PromptStripped: upstream.PromptStripped,
			FinishReason:       firstNonEmpty(responseUpstreamFinishReason(upstream.Response), stats.FinishReason),
			FinishReasonSource: firstNonEmpty(responseUpstreamFinishReasonSource(upstream.Response), stats.FinishReasonSource),
		})
		finalizeStream(upstream, stats, status, logErr)
		if relayErr != nil {
			// The upstream answered 200 and failed inside the stream, so the
			// executor's attempt loop never saw it. Feed the classified state
			// back into the pool so the next request can route around a
			// quota-exhausted account. Use req.Model (prefix-stripped by
			// resolveProviderFilter) so the cooldown key matches the key
			// PickRoute uses; publicModel may still carry "qoder/" and would
			// write a cooldown that routing never hits.
			if upstream.Finalize == nil && r.Context().Err() == nil && !errors.Is(relayErr, context.Canceled) && !errors.Is(relayErr, context.DeadlineExceeded) && !IsStreamClientDisconnect(relayErr) {
				h.Executor.ObserveStreamFailure(upstream.AccountID, relayErr, req.Model)
			}
			panic(http.ErrAbortHandler)
		}
		return
	}

	res, err := h.Executor.ChatNonStream(ctx, req, prefer, providerFilter)
	if err != nil {
		h.finishRequestLog(requestID, started, req, publicModel, res.AccountID, firstNonEmpty(res.Provider, providerFilter), res.Routing, accounts.RequestStatusError, 0, nonstreamFailureUsage(res), err, res.AttemptCount, res.ReasoningLevel, requestLogMetadata{
			OutputLimit: res.OutputLimit, OutputLimitSource: res.OutputLimitSource, OutputBudgetEvidence: res.OutputBudgetEvidence, PromptStripped: res.PromptStripped,
		})
		WriteClassifiedErr(w, err)
		return
	}
	if publicModel != "" {
		res.Model = publicModel
	}
	prompt, completion := res.UsageTokenEvidence()
	h.finishRequestLog(requestID, started, req, publicModel, res.AccountID, firstNonEmpty(res.Provider, providerFilter), res.Routing, accounts.RequestStatusOK, 0, &StreamRelayStats{
		PromptTokens: prompt, CompletionTokens: completion,
		CacheReadTokens: res.CacheReadTokens, CacheWriteTokens: res.CacheWriteTokens,
		CachedTokens: res.CachedTokens, UsageSource: res.UsageSource, Credits: res.Credits,
		ConsumedCredits: res.ConsumedCredits, Model: res.Model,
	}, nil, res.AttemptCount, res.ReasoningLevel, requestLogMetadata{
		OutputLimit: res.OutputLimit, OutputLimitSource: res.OutputLimitSource, OutputBudgetEvidence: res.OutputBudgetEvidence, PromptStripped: res.PromptStripped,
		FinishReason: res.FinishReason, FinishReasonSource: res.FinishReasonSource,
	})
	message := map[string]any{
		"role":    "assistant",
		"content": res.Content,
	}
	if res.Reasoning != "" {
		message["reasoning_content"] = res.Reasoning
	}
	if len(res.ToolCalls) > 0 && string(res.ToolCalls) != "null" {
		message["tool_calls"] = json.RawMessage(res.ToolCalls)
		if res.Content == "" {
			message["content"] = nil
		}
	}
	finishReason := res.FinishReason
	finishReasonSource := res.FinishReasonSource
	if finishReason == "" {
		if len(res.ToolCalls) > 0 && string(res.ToolCalls) != "null" {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
		finishReasonSource = "gateway_fallback"
	}
	w.Header().Set("X-CLI2API-Finish-Reason", finishReason)
	w.Header().Set("X-CLI2API-Finish-Reason-Source", firstNonEmpty(finishReasonSource, "unknown"))
	if res.AccountID != "" {
		w.Header().Set("X-Qoder-Account", res.AccountID)
		w.Header().Set("X-CLI2API-Account", res.AccountID)
	}
	if provider := firstNonEmpty(res.Provider, providerFilter); provider != "" {
		w.Header().Set("X-CLI2API-Provider", provider)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   res.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": BuildChatUsage(res),
	})
}

func (h *Handler) finishRequestLog(requestID string, started time.Time, req translate.ChatRequest, publicModel, accountID, provider, routing, status string, ttfb int, stats *StreamRelayStats, err error, attemptCount int, resolvedReasoning string, metadata ...requestLogMetadata) {
	if h.Recorder == nil || requestID == "" {
		return
	}
	entry := accounts.RequestLog{
		ID:                 requestID,
		CreatedAt:          started,
		Stream:             req.Stream,
		Status:             status,
		RequestedModel:     firstNonEmpty(publicModel, req.Model),
		RequestedReasoning: executor.RequestedReasoningLevel(req),
		ResolvedReasoning:  resolvedReasoning,
		AccountID:          accountID,
		Provider:           provider,
		Routing:            routing,
		AttemptCount:       attemptCount,
	}
	if len(metadata) > 0 {
		entry.OutputLimit = metadata[0].OutputLimit
		entry.OutputLimitSource = metadata[0].OutputLimitSource
		entry.OutputBudgetEnforcement = outputBudgetEnforcement(provider, metadata[0].OutputBudgetEvidence, entry.OutputLimit, nil, "unknown")
		entry.PromptStripped = metadata[0].PromptStripped
		entry.FinishReason = metadata[0].FinishReason
		entry.FinishReasonSource = metadata[0].FinishReasonSource
	}
	if status != accounts.RequestStatusStarted && status != accounts.RequestStatusStreaming {
		finished := time.Now().UTC()
		latency := int(finished.Sub(started).Milliseconds())
		entry.FinishedAt = &finished
		entry.LatencyMs = &latency
	}
	if ttfb > 0 {
		entry.TTFBMs = &ttfb
	} else if !req.Stream && entry.LatencyMs != nil {
		entry.TTFBMs = entry.LatencyMs
	}
	if stats != nil {
		entry.PromptTokens = stats.PromptTokens
		entry.CompletionTokens = stats.CompletionTokens
		entry.CacheReadTokens = stats.CacheReadTokens
		entry.CacheWriteTokens = stats.CacheWriteTokens
		entry.UsageSource = stats.UsageSource
		consumed := stats.ConsumedCredits
		if consumed == nil {
			consumed = stats.Credits
		}
		entry.Credits = consumed
		if stats.Model != "" {
			entry.MappedModel = stats.Model
		}
		if len(metadata) > 0 {
			entry.OutputBudgetEnforcement = outputBudgetEnforcement(provider, metadata[0].OutputBudgetEvidence, entry.OutputLimit, stats.CompletionTokens, stats.UsageSource)
		}
	}
	if err != nil {
		classified := ClassifyAPIError(err)
		if classified.Kind == accounts.KindCanceled {
			entry.Status = accounts.RequestStatusCanceled
		}
		entry.ErrorKind = classified.Kind
		entry.ErrorCode = classified.Code
		entry.ErrorMessage = classified.Message
	}
	h.Recorder.Finish(entry)
	if stats != nil && entry.Credits != nil {
		h.Recorder.UsageDetail(accounts.RequestUsageDetail{
			RequestID: requestID,
			CreatedAt: started,
			Provider:  provider,
			Credit:    entry.Credits,
			Unit:      "credits",
		})
	}
}

func (h *Handler) recordStreamDiagnostic(requestID string, response *http.Response, started time.Time, stats StreamRelayStats, relayErr, contextErr error, timings ...streamDiagnosticTiming) {
	if h.Recorder == nil || requestID == "" {
		return
	}
	finished := time.Now().UTC()
	diagnostic := accounts.RequestStreamDiagnostic{
		RequestID:     requestID,
		CreatedAt:     started,
		FinishedAt:    &finished,
		SSEEventCount: stats.SSEEventCount,
		BytesRead:     stats.BytesRead,
		LastEvent:     stats.LastEvent,
		SawDone:       stats.SawDone,
	}
	if response != nil {
		status := response.StatusCode
		diagnostic.UpstreamStatus = &status
		if response.ContentLength >= 0 {
			contentLength := int(response.ContentLength)
			diagnostic.ContentLength = &contentLength
		}
		diagnostic.UpstreamRequestID = firstNonEmpty(
			response.Header.Get("X-Request-ID"),
			response.Header.Get("X-Request-Id"),
			response.Header.Get("X-Upstream-Request-ID"),
		)
	}
	if len(timings) > 0 {
		timing := timings[0]
		diagnostic.OutputBudgetEnforcement = timing.OutputBudgetEnforcement
		if !timing.PreparedAt.IsZero() {
			diagnostic.PrepareDurationMs = elapsedMilliseconds(started, timing.PreparedAt)
		}
		if !timing.UpstreamStartedAt.IsZero() && !timing.UpstreamResponseAt.IsZero() {
			diagnostic.UpstreamResponseDurationMs = elapsedMilliseconds(timing.UpstreamStartedAt, timing.UpstreamResponseAt)
		}
		if !timing.UpstreamResponseAt.IsZero() && stats.FirstTokenAt != nil {
			diagnostic.FirstEventDurationMs = elapsedMilliseconds(timing.UpstreamResponseAt, *stats.FirstTokenAt)
		}
	}
	diagnostic.TotalDurationMs = elapsedMilliseconds(started, finished)
	if response != nil && response.Body != nil {
		if observer, ok := response.Body.(providers.ChatStreamObserver); ok {
			observed := observer.ChatStreamObservation()
			if len(timings) > 0 && !observed.HeadersAt.IsZero() {
				diagnostic.UpstreamResponseDurationMs = elapsedMilliseconds(timings[0].UpstreamStartedAt, observed.HeadersAt)
			}
			if !observed.HeadersAt.IsZero() && !observed.FirstEventAt.IsZero() {
				diagnostic.FirstEventDurationMs = elapsedMilliseconds(observed.HeadersAt, observed.FirstEventAt)
			}
			if !observed.HeadersAt.IsZero() && !observed.FirstContentAt.IsZero() {
				diagnostic.FirstContentDurationMs = elapsedMilliseconds(observed.HeadersAt, observed.FirstContentAt)
			}
			diagnostic.TimeoutPhase = observed.TimeoutPhase
		}
	}
	var timeout interface{ TimeoutPhase() string }
	if errors.As(relayErr, &timeout) {
		diagnostic.TimeoutPhase = timeout.TimeoutPhase()
	}
	diagnostic.ContentChars = stats.ContentChars
	diagnostic.ReasoningChars = stats.ReasoningChars
	diagnostic.ToolEventCount = stats.ToolEventCount
	diagnostic.CharCountUnit = stats.CharCountUnit
	if metadata, ok := responseBodyFinishMetadata(response); ok {
		diagnostic.UpstreamFinishReason = metadata.upstreamReason
		diagnostic.UpstreamFinishReasonSource = metadata.upstreamSource
		diagnostic.ExternalFinishReason = metadata.externalReason
		diagnostic.ExternalFinishReasonSource = metadata.externalSource
	}
	if diagnostic.ExternalFinishReason == "" && stats.FinishReason != "" {
		diagnostic.ExternalFinishReason = stats.FinishReason
		diagnostic.ExternalFinishReasonSource = firstNonEmpty(stats.FinishReasonSource, "openai_stream_finish_reason")
	}
	if contextErr != nil {
		diagnostic.ContextErr = contextErr.Error()
	}
	if relayErr != nil {
		diagnostic.RelayError = relayErr.Error()
	}
	switch {
	case diagnostic.TimeoutPhase != "":
		diagnostic.CancellationSource = "request_timeout"
	case IsStreamClientDisconnect(relayErr):
		diagnostic.CancellationSource = "client_disconnect"
	case contextErr != nil:
		diagnostic.CancellationSource = "request_context_canceled"
	case errors.Is(relayErr, context.DeadlineExceeded):
		diagnostic.CancellationSource = "request_timeout"
	case errors.Is(relayErr, context.Canceled):
		diagnostic.CancellationSource = "upstream_context_canceled"
	case relayErr != nil:
		diagnostic.CancellationSource = "upstream_stream_error"
	default:
		diagnostic.CancellationSource = "completed"
	}
	if IsStreamClientDisconnect(relayErr) || contextErr != nil || errors.Is(relayErr, context.Canceled) || errors.Is(relayErr, context.DeadlineExceeded) {
		switch {
		case stats.SawDone:
			diagnostic.CancellationStage = "after_completion"
		case stats.FirstTokenAt != nil:
			diagnostic.CancellationStage = "streaming"
		case response != nil:
			diagnostic.CancellationStage = "awaiting_first_event"
		default:
			diagnostic.CancellationStage = "unknown"
		}
	}
	h.Recorder.StreamDiagnostic(diagnostic)
}

type finishMetadata struct {
	upstreamReason, upstreamSource string
	externalReason, externalSource string
}

func responseBodyFinishMetadata(response *http.Response) (finishMetadata, bool) {
	if response == nil || response.Body == nil {
		return finishMetadata{}, false
	}
	metadata, ok := response.Body.(interface {
		SoloStreamFinishReason() (string, string, string, string)
	})
	if !ok {
		return finishMetadata{}, false
	}
	upstream, upstreamSource, external, externalSource := metadata.SoloStreamFinishReason()
	return finishMetadata{upstream, upstreamSource, external, externalSource}, true
}

func responseUpstreamFinishReason(response *http.Response) string {
	metadata, _ := responseBodyFinishMetadata(response)
	return metadata.upstreamReason
}

func responseUpstreamFinishReasonSource(response *http.Response) string {
	metadata, _ := responseBodyFinishMetadata(response)
	return metadata.upstreamSource
}

func elapsedMilliseconds(started, finished time.Time) *int {
	if started.IsZero() || finished.IsZero() || finished.Before(started) {
		return nil
	}
	value := int(finished.Sub(started).Milliseconds())
	return &value
}

func ptrInt(value int) *int { return &value }
