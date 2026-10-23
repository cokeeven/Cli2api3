package qoder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestWalkUpstreamStopsAtDone(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"before"}}]}`,
		`data: [DONE]`,
		`data: {"choices":[{"delta":{"content":"after"}}]}`,
		``,
	}, "\n")
	var content []string
	err := walkUpstream(strings.NewReader(stream), func(body map[string]any) error {
		choices, _ := body["choices"].([]any)
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if value, _ := delta["content"].(string); value != "" {
			content = append(content, value)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0] != "before" {
		t.Fatalf("events consumed after [DONE]: %v", content)
	}
}

type trackedBlockingBody struct {
	started    chan struct{}
	closed     chan struct{}
	readDone   chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
	readOnce   sync.Once
	closeCalls atomic.Int32
}

func newTrackedBlockingBody() *trackedBlockingBody {
	return &trackedBlockingBody{started: make(chan struct{}), closed: make(chan struct{}), readDone: make(chan struct{})}
}

func (b *trackedBlockingBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.closed
	b.readOnce.Do(func() { close(b.readDone) })
	return 0, io.ErrClosedPipe
}

func (b *trackedBlockingBody) Close() error {
	b.closeCalls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestConvertedBodyCloseUnblocksAndClosesUpstreamOnce(t *testing.T) {
	upstreamBody := newTrackedBlockingBody()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: upstreamBody}
	converted := rewriteUpstreamSSE(resp, "model")
	select {
	case <-upstreamBody.started:
	case <-time.After(time.Second):
		t.Fatal("converter did not begin reading upstream")
	}

	if err := converted.Body.Close(); err != nil {
		t.Fatal(err)
	}
	closedOnReturn := upstreamBody.closeCalls.Load()
	readStoppedOnReturn := false
	select {
	case <-upstreamBody.readDone:
		readStoppedOnReturn = true
	case <-time.After(100 * time.Millisecond):
	}
	// Keep a failing baseline test from leaking its deliberately blocked reader.
	if !readStoppedOnReturn {
		_ = upstreamBody.Close()
		select {
		case <-upstreamBody.readDone:
		case <-time.After(time.Second):
			t.Fatal("test cleanup could not unblock upstream reader")
		}
	}
	if closedOnReturn != 1 || !readStoppedOnReturn {
		t.Fatalf("downstream close returned with upstream close calls=%d, read stopped=%v", closedOnReturn, readStoppedOnReturn)
	}
	if got := upstreamBody.closeCalls.Load(); got != 1 {
		t.Fatalf("upstream body closed %d times, want exactly once", got)
	}
}

func TestDirectChatStreamBoundsWaitForFirstMeaningfulEvent(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	direct := newDirectFor(t, upstream)
	ctx := providers.WithChatTimeouts(context.Background(), providers.ChatTimeouts{
		Headers: time.Second, FirstEvent: 40 * time.Millisecond, Idle: time.Second,
	})
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()

	begin := time.Now()
	_, _, err := direct.ChatStream(ctx, "acc", translate.ChatRequest{Model: "auto"})
	elapsed := time.Since(begin)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream handler did not start")
	}
	var upstreamErr *providers.Error
	if elapsed > 400*time.Millisecond || !errorsAsProvider(err, &upstreamErr) || upstreamErr.Code != "upstream_empty" {
		t.Fatalf("first-event wait elapsed=%s error=%#v, want bounded upstream_empty", elapsed, err)
	}
}

func errorsAsProvider(err error, target **providers.Error) bool {
	if err == nil {
		return false
	}
	return errors.As(err, target)
}

func TestWriteOpenAIChunksUsesUniqueStableCompletionIDPerStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	var first, second bytes.Buffer
	if err := writeOpenAIChunks(&first, strings.NewReader(stream), "model"); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenAIChunks(&second, strings.NewReader(stream), "model"); err != nil {
		t.Fatal(err)
	}
	firstIDs := completionChunkIDs(t, first.String())
	secondIDs := completionChunkIDs(t, second.String())
	if len(firstIDs) < 2 || len(secondIDs) < 2 {
		t.Fatalf("expected content and terminal chunks, got first=%v second=%v", firstIDs, secondIDs)
	}
	for _, id := range firstIDs {
		if id != firstIDs[0] {
			t.Fatalf("first stream changed completion ID: %v", firstIDs)
		}
	}
	for _, id := range secondIDs {
		if id != secondIDs[0] {
			t.Fatalf("second stream changed completion ID: %v", secondIDs)
		}
	}
	if firstIDs[0] == secondIDs[0] || !strings.HasPrefix(firstIDs[0], "chatcmpl-") || !strings.HasPrefix(secondIDs[0], "chatcmpl-") {
		t.Fatalf("completion IDs are not per-request unique: first=%v second=%v", firstIDs, secondIDs)
	}
}

func completionChunkIDs(t *testing.T, stream string) []string {
	t.Helper()
	var ids []string
	scanner := bufio.NewScanner(strings.NewReader(stream))
	for scanner.Scan() {
		line, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "data:")
		if !ok || strings.TrimSpace(line) == "[DONE]" {
			continue
		}
		var chunk struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &chunk); err != nil {
			t.Fatalf("invalid chunk JSON %q: %v", line, err)
		}
		ids = append(ids, chunk.ID)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestBuildPlainChatBodyPreservesAssistantReasoningHistory(t *testing.T) {
	body := BuildPlainChatBody(translate.ChatRequest{
		Model: "auto",
		Messages: []translate.ChatMessage{
			{Role: "user", Content: "keep the rule from earlier"},
			{Role: "assistant", Content: "", ReasoningContent: "reasoning history stays separate"},
			{Role: "user", Content: "continue"},
		},
	}, time.Unix(1_700_000_000, 0))
	messages, ok := body["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("messages type = %T", body["messages"])
	}
	if len(messages) != 3 || messages[1]["role"] != "assistant" || messages[1]["reasoning_content"] != "reasoning history stays separate" {
		t.Fatalf("assistant reasoning history was lost or moved into content: %#v", messages)
	}
	if messages[1]["content"] != "" {
		t.Fatalf("reasoning was copied into user-visible content: %#v", messages[1])
	}
}
