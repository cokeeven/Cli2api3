package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestExplicitThinkingCannotBeSilentlyDiscarded(t *testing.T) {
	falseValue := false
	for _, req := range []translate.ChatRequest{
		{Thinking: json.RawMessage(`{"type":"enabled","budget_tokens":1024}`)},
		{Thinking: json.RawMessage(`{"type":"unrecognized"}`)},
		{ReasoningBudgetTokens: json.RawMessage(`1024`)},
		{EnableThinking: &falseValue},
	} {
		if err := ValidateReasoningSignals(req, "high"); err == nil {
			t.Errorf("invalid intent accepted: %+v", req)
		}
	}
	if level, present, err := ThinkingIntent(json.RawMessage(`{"type":"disabled"}`)); err != nil || !present || level != "none" {
		t.Fatalf("%q %v %v", level, present, err)
	}
}
func TestCheckinTraceHandlesNetworkAndUnknownBusinessCodes(t *testing.T) {
	ctx, tr := WithCheckinTrace(context.Background())
	RecordCheckinResponse(ctx, "status", 0, nil, &net.OpError{Op: "dial", Err: errors.New("secret-marker")})
	result, err := tr.Failure(errors.New("secret-marker"), false)
	if err == nil || !strings.Contains(result.Message, "outcome=network_error") || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, tr = WithCheckinTrace(context.Background())
	RecordCheckinResponse(ctx, "claim", 200, []byte(`{"code":null}`), nil)
	if tr.BusinessCode != nil {
		t.Fatalf("null code became %v", *tr.BusinessCode)
	}
}
