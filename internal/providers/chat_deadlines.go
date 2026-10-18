package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ChatTimeouts supplements the executor's total resource bound. Only the
// WorkBuddy and Trae chat adapters opt into these SSE-specific deadlines.
type ChatTimeouts struct{ Headers, FirstEvent, Idle time.Duration }

func (p ChatTimeouts) Normalized() ChatTimeouts {
	if p.Headers <= 0 {
		p.Headers = 45 * time.Second
	}
	if p.FirstEvent <= 0 {
		p.FirstEvent = 120 * time.Second
	}
	if p.Idle <= 0 {
		p.Idle = 90 * time.Second
	}
	return p
}

type chatTimeoutsKey struct{}

func WithChatTimeouts(ctx context.Context, p ChatTimeouts) context.Context {
	return context.WithValue(ctx, chatTimeoutsKey{}, p.Normalized())
}

type ChatTimeoutError struct{ Phase string }

func (e *ChatTimeoutError) Error() string        { return "chat time limit exceeded during " + e.Phase }
func (e *ChatTimeoutError) Unwrap() error        { return context.DeadlineExceeded }
func (e *ChatTimeoutError) TimeoutPhase() string { return e.Phase }
func (e *ChatTimeoutError) Timeout() bool        { return true }

// ChatContextError preserves the gateway's own deadline cause across net/http,
// pipes and transport Close calls, without turning a user cancellation into it.
func ChatContextError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var phase *ChatTimeoutError
	if errors.As(err, &phase) {
		return err
	}
	if errors.As(context.Cause(ctx), &phase) {
		return phase
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type ChatStreamObservation struct {
	HeadersAt, FirstEventAt, FirstContentAt time.Time
	TimeoutPhase                            string
}
type ChatStreamObserver interface{ ChatStreamObservation() ChatStreamObservation }

// DoChatRequest uses a cancelable header deadline rather than http.Client.Timeout,
// which would also terminate a healthy, continuously producing SSE stream.
func DoChatRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	policy, _ := req.Context().Value(chatTimeoutsKey{}).(ChatTimeouts)
	policy = policy.Normalized()
	ctx, cancel := context.WithCancelCause(req.Context())
	var phase atomic.Value
	phase.Store("upstream_connect")
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { phase.Store("upstream_headers") }}
	ctx = httptrace.WithClientTrace(ctx, trace)
	timer := time.AfterFunc(policy.Headers, func() { cancel(&ChatTimeoutError{Phase: phase.Load().(string)}) })
	copyClient := *client
	copyClient.Timeout = 0
	resp, err := copyClient.Do(req.Clone(ctx))
	timer.Stop()
	if err != nil {
		err = ChatContextError(ctx, err)
		var timeout net.Error
		if ctx.Err() == nil && errors.As(err, &timeout) && timeout.Timeout() {
			err = &ChatTimeoutError{Phase: phase.Load().(string)}
		}
		cancel(nil)
		return nil, err
	}
	now := time.Now()
	rawBody := resp.Body
	body := &chatDeadlineBody{ReadCloser: rawBody, ctx: ctx, cancel: cancel, policy: policy, observation: ChatStreamObservation{HeadersAt: now}, phase: "first_event", due: now.Add(policy.FirstEvent)}
	body.timer = time.AfterFunc(policy.FirstEvent, body.expire)
	body.stopContext = context.AfterFunc(ctx, func() { _ = rawBody.Close() })
	resp.Body = body
	return resp, nil
}

type chatDeadlineBody struct {
	io.ReadCloser
	ctx         context.Context
	cancel      context.CancelCauseFunc
	policy      ChatTimeouts
	mu          sync.Mutex
	timer       *time.Timer
	stopContext func() bool
	once        sync.Once
	phase       string
	due         time.Time
	terminal    bool
	pending     []byte
	event       string
	data        []string
	dataBytes   int
	observation ChatStreamObservation
}

func (b *chatDeadlineBody) expire() {
	b.mu.Lock()
	if b.terminal {
		b.mu.Unlock()
		return
	}
	if time.Now().Before(b.due) {
		b.timer.Reset(time.Until(b.due))
		b.mu.Unlock()
		return
	}
	phase := b.phase
	b.observation.TimeoutPhase = phase
	b.mu.Unlock()
	b.cancel(&ChatTimeoutError{Phase: phase})
}
func (b *chatDeadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if n > 0 {
		b.observe(p[:n])
	}
	if err != nil {
		if len(b.pending) > 0 {
			b.line(string(b.pending))
			b.pending = nil
			b.frame()
		}
		b.timer.Stop()
	}
	b.mu.Unlock()
	return n, ChatContextError(b.ctx, err)
}
func (b *chatDeadlineBody) Close() error {
	var err error
	b.once.Do(func() {
		b.mu.Lock()
		b.terminal = true
		b.timer.Stop()
		b.mu.Unlock()
		b.stopContext()
		b.cancel(nil)
		err = b.ReadCloser.Close()
	})
	return err
}
func (b *chatDeadlineBody) ChatStreamObservation() ChatStreamObservation {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observation
}
func (b *chatDeadlineBody) observe(data []byte) {
	for _, ch := range data {
		if ch == '\n' {
			b.line(strings.TrimSuffix(string(b.pending), "\r"))
			b.pending = b.pending[:0]
		} else if len(b.pending) < 16<<20 {
			b.pending = append(b.pending, ch)
		}
	}
}
func (b *chatDeadlineBody) line(line string) {
	if line == "" {
		b.frame()
		return
	}
	if strings.HasPrefix(line, "event:") {
		b.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
	}
	if strings.HasPrefix(line, "data:") {
		value := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if b.dataBytes+len(value) <= 16<<20 {
			b.data = append(b.data, value)
			b.dataBytes += len(value)
		}
	}
}
func (b *chatDeadlineBody) frame() {
	data := strings.Join(b.data, "\n")
	event := b.event
	b.data = nil
	b.dataBytes = 0
	b.event = ""
	if b.terminal {
		return
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(data), &obj)
	terminal := data == "[DONE]" || event == "done" || event == "error" || len(obj["error"]) > 0
	content, effective := chatFrameOutput(obj)
	if !terminal && !effective {
		return
	}
	now := time.Now()
	if b.observation.FirstEventAt.IsZero() {
		b.observation.FirstEventAt = now
	}
	if content && b.observation.FirstContentAt.IsZero() {
		b.observation.FirstContentAt = now
	}
	if terminal {
		b.terminal = true
		b.timer.Stop()
		return
	}
	b.phase = "stream_idle"
	b.due = now.Add(b.policy.Idle)
	b.timer.Reset(b.policy.Idle)
}
func chatFrameOutput(obj map[string]json.RawMessage) (content, effective bool) {
	hasString := func(raw json.RawMessage) bool {
		var value string
		return json.Unmarshal(raw, &value) == nil && value != ""
	}
	hasCalls := func(raw json.RawMessage) bool {
		var value []json.RawMessage
		return json.Unmarshal(raw, &value) == nil && len(value) > 0
	}
	check := func(value map[string]json.RawMessage) {
		content = content || hasString(value["content"]) || hasString(value["response"])
		effective = effective || content || hasString(value["reasoning_content"]) || hasCalls(value["tool_calls"]) || hasString(value["finish_reason"])
	}
	check(obj)
	var choices []map[string]json.RawMessage
	_ = json.Unmarshal(obj["choices"], &choices)
	for _, choice := range choices {
		check(choice)
		for _, key := range []string{"delta", "message"} {
			var value map[string]json.RawMessage
			_ = json.Unmarshal(choice[key], &value)
			check(value)
		}
	}
	return
}

// CarryChatObservation preserves raw-upstream timing through an SSE rewrite.
func CarryChatObservation(body io.ReadCloser, source io.ReadCloser, retryAfter ...string) io.ReadCloser {
	header := ""
	if len(retryAfter) > 0 {
		header = retryAfter[0]
	}
	return &chatObservedBody{ReadCloser: body, source: source, retryAfter: header}
}

type chatObservedBody struct {
	io.ReadCloser
	source     io.ReadCloser
	retryAfter string
}

func (b *chatObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	return n, WithRetryAfterHeader(err, b.retryAfter)
}

func (b *chatObservedBody) ChatStreamObservation() ChatStreamObservation {
	if observer, ok := b.source.(ChatStreamObserver); ok {
		return observer.ChatStreamObservation()
	}
	return ChatStreamObservation{}
}
func (b *chatObservedBody) SoloStreamFinishReason() (string, string, string, string) {
	if observer, ok := b.ReadCloser.(interface {
		SoloStreamFinishReason() (string, string, string, string)
	}); ok {
		return observer.SoloStreamFinishReason()
	}
	return "", "", "", ""
}
