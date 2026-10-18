package trae

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type terminalErrorReader struct {
	data []byte
	err  error
}

func (r *terminalErrorReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestRewriteSoloStreamPreservesOutputFinishReason(t *testing.T) {
	for _, test := range []struct {
		name       string
		output     string
		done       string
		wantReason string
	}{
		{
			name:       "output reason survives done without reason",
			output:     `{"response":"thinking","finish_reason":"length"}`,
			done:       `{}`,
			wantReason: "length",
		},
		{
			name:       "explicit done reason wins",
			output:     `{"response":"thinking","finish_reason":"length"}`,
			done:       `{"finish_reason":"stop"}`,
			wantReason: "stop",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := "event: output\ndata: " + test.output + "\n\n" +
				"event: done\ndata: " + test.done + "\n\n"
			resp, err := rewriteSoloStream(io.NopCloser(strings.NewReader(stream)), "test")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"finish_reason":"`+test.wantReason+`"`) {
				t.Fatalf("finish reason %q missing from %s", test.wantReason, body)
			}
		})
	}
}

func TestRewriteSoloStreamDoesNotSynthesizeSuccessOnEarlyEOF(t *testing.T) {
	resp, err := rewriteSoloStream(io.NopCloser(strings.NewReader(
		"event: output\ndata: {\"response\":\"partial\"}\n\n",
	)), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatal("stream ended successfully without an upstream done event")
	}
	if strings.Contains(string(body), "[DONE]") || strings.Contains(string(body), `"finish_reason":"stop"`) {
		t.Fatalf("early EOF was presented as a successful completion: %s", body)
	}
}

func TestRewriteSoloStreamDoesNotSynthesizeSuccessOnReadFailure(t *testing.T) {
	reader := &terminalErrorReader{
		data: []byte("event: output\ndata: {\"response\":\"partial\"}\n\n"),
		err:  context.Canceled,
	}
	resp, err := rewriteSoloStream(io.NopCloser(reader), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if !errors.Is(readErr, context.Canceled) {
		t.Fatalf("read error = %v, want context cancellation", readErr)
	}
	if strings.Contains(string(body), "[DONE]") || strings.Contains(string(body), `"finish_reason":"stop"`) {
		t.Fatalf("read failure was presented as a successful completion: %s", body)
	}
}
