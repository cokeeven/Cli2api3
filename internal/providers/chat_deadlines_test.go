package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatDeadlinesSeparateHeadersFirstEventIdleAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, phase     string
		initial, repeat string
		headerDelay     bool
		cancel          bool
	}{
		{name: "headers", phase: "upstream_headers", headerDelay: true},
		{name: "heartbeat_is_not_first_output", phase: "first_event", repeat: ": heartbeat\n\n"},
		{name: "heartbeat_does_not_extend_idle", phase: "stream_idle", initial: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n", repeat: ": heartbeat\n\n"},
		{name: "client_cancellation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.headerDelay {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if tc.initial != "" {
					_, _ = io.WriteString(w, tc.initial)
					w.(http.Flusher).Flush()
				}
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						if tc.repeat != "" {
							_, _ = io.WriteString(w, tc.repeat)
							w.(http.Flusher).Flush()
						}
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(WithChatTimeouts(context.Background(), ChatTimeouts{Headers: 80 * time.Millisecond, FirstEvent: 80 * time.Millisecond, Idle: 80 * time.Millisecond}))
			defer cancel()
			if tc.cancel {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
			resp, err := DoChatRequest(server.Client(), req)
			if err == nil {
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			if tc.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
				var phase *ChatTimeoutError
				if errors.As(err, &phase) {
					t.Fatal("client cancellation became server timeout")
				}
				return
			}
			var phase *ChatTimeoutError
			if !errors.As(err, &phase) || phase.Phase != tc.phase {
				t.Fatalf("error=%v, want phase %s", err, tc.phase)
			}
		})
	}
}

func TestChatDeadlinesAllowContinuousReasoningAndRecordFirstContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"x\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(15 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	ctx := WithChatTimeouts(context.Background(), ChatTimeouts{Headers: time.Second, FirstEvent: 70 * time.Millisecond, Idle: 70 * time.Millisecond})
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
	resp, err := DoChatRequest(server.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	obs := resp.Body.(ChatStreamObserver).ChatStreamObservation()
	if obs.FirstEventAt.IsZero() || obs.FirstContentAt.IsZero() || !obs.FirstContentAt.After(obs.FirstEventAt) || obs.TimeoutPhase != "" {
		t.Fatalf("observation=%+v", obs)
	}
}

func TestChatTotalDeadlineKeepsItsCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeoutCause(context.Background(), 30*time.Millisecond, &ChatTimeoutError{Phase: "total"})
	defer cancel()
	req, _ := http.NewRequestWithContext(WithChatTimeouts(ctx, ChatTimeouts{Headers: time.Second, FirstEvent: time.Second, Idle: time.Second}), "POST", server.URL, nil)
	resp, err := DoChatRequest(server.Client(), req)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	var phase *ChatTimeoutError
	if !errors.As(err, &phase) || phase.Phase != "total" {
		t.Fatalf("lost total cause: %v", err)
	}
}
