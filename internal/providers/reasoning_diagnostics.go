package providers

import (
	"context"
	"log"
)

type diagnosticRequestIDKey struct{}

func WithDiagnosticRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, diagnosticRequestIDKey{}, id)
}

// LogReasoningDecision records normalized levels and their source without
// recording prompts, credentials, tools, or arbitrary caller-provided text.
func LogReasoningDecision(ctx context.Context, provider, account, model, caller, configured, actual string) {
	if actual == "" {
		return
	}
	original, reason := caller, "caller_exact"
	if original == "" {
		original = NormalizeReasoningLevel(configured)
		reason = "console_default_exact"
		if original == "" {
			reason = "catalog_default"
		}
	}
	if original != "" && original != actual {
		if caller != "" {
			reason = "caller_catalog_match"
		} else {
			reason = "console_default_catalog_match"
		}
	}
	id, _ := ctx.Value(diagnosticRequestIDKey{}).(string)
	log.Printf("reasoning_decision request_id=%q provider=%q account=%q model=%q requested=%q actual=%q reason=%q", id, provider, account, model, original, actual, reason)
}
