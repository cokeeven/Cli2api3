package executor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

// ExecutionError is the executor's final classified result. HTTP layers format
// it; they must not re-run Classify or invent failover/cooldown policy.
type ExecutionError struct {
	Classified Classified
	Err        error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Classified.Message != "" {
		return e.Classified.Message
	}
	return e.Classified.Code
}

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewExecutionError(classified Classified, err error) *ExecutionError {
	if classified.RetryAfter <= 0 {
		classified.RetryAfter = classified.Cooldown
	}
	return &ExecutionError{Classified: classified, Err: err}
}

// ClassifyUpstreamBody classifies an upstream HTTP/SSE error body once.
func ClassifyUpstreamBody(status int, body string) error {
	classified := Classify(status, body, "", "", "")
	classified.EvidenceSource = "upstream_error_body"
	return NewExecutionError(classified, nil)
}

// StreamIncompleteError is a mid-body stream that ended without [DONE].
func StreamIncompleteError() error {
	return newUnavailableStreamError("upstream_stream_incomplete", "stream ended before [DONE]", 502, nil)
}

// StreamReadError wraps a mid-body stream read failure.
func StreamReadError(err error) error {
	if err == nil {
		return StreamIncompleteError()
	}
	var execErr *ExecutionError
	if errors.As(err, &execErr) && execErr != nil {
		return err
	}
	var providerErr *providers.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &providerErr) && providerErr != nil) {
		return NewExecutionError(ClassifyError(err), err)
	}
	return newUnavailableStreamError("upstream_stream_interrupted", "stream read error: "+err.Error(), 502, err)
}

func newUnavailableStreamError(code, message string, status int, cause error) error {
	classified := Classify(status, message, "", accounts.KindUnavailable, "")
	classified.Code = code
	classified.Message = message
	classified.Status = status
	classified.Type = "api_error"
	classified.EvidenceSource = "local_stream_guard"
	return NewExecutionError(classified, cause)
}

// ClassifyError is the single adapter-error policy used by execution and HTTP reporting.
func ClassifyError(err error) Classified {
	if err == nil {
		return Classify(0, "", "", accounts.KindUnavailable, "")
	}
	var execErr *ExecutionError
	if errors.As(err, &execErr) && execErr != nil {
		return execErr.Classified
	}
	var phase interface{ TimeoutPhase() string }
	if errors.As(err, &phase) {
		classified := Classified{
			Kind: accounts.KindCanceled, Status: 504, Failover: false, Scope: "request",
			Code: "request_timeout_" + phase.TimeoutPhase(), Type: "timeout_error", Message: err.Error(),
			ErrorPhase: phase.TimeoutPhase(), ErrorSource: "gateway_chat_deadline",
		}
		copyProviderErrorEvidence(&classified, err)
		return classified
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		classified := Classified{
			Kind: accounts.KindCanceled, Status: 499, Failover: false,
			Code: "request_canceled", Message: err.Error(),
		}
		copyProviderErrorEvidence(&classified, err)
		return classified
	}
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) || providerErr == nil {
		return Classify(0, err.Error(), "", "", "")
	}
	message := strings.TrimSpace(providerErr.Message)
	if message == "" {
		message = providerErr.Error()
	}
	classified := Classify(providerErr.Status, message, "", providerErr.Kind, "")
	classified.Scope = providerErr.Scope
	classified.RetrySafe = providerErr.RetrySafe
	if classified.Kind == accounts.KindQuota && providerErr.RetrySafe {
		classified.Failover = true
	}
	if providerErr.Code != "" {
		classified.Code = providerErr.Code
	}
	classified.EvidenceSource = "provider_adapter_error"
	copyProviderErrorEvidence(&classified, err)
	if providerErr.UpstreamResetAt != nil {
		reset := providerErr.UpstreamResetAt.UTC()
		classified.UpstreamResetAt = &reset
	}
	if providerErr.Type != "" {
		classified.Type = providerErr.Type
	}
	if providerErr.Message != "" {
		classified.Message = providerErr.Message
	}
	if providerErr.Code == "upstream_empty" {
		// A heartbeat-only upstream stream says nothing about the account's
		// health: the credential, quota, and rate-limit state are all fine, and
		// the same account may succeed on the very next request. Fail over so
		// another account serves the turn, but never cool this one down.
		classified.Failover = true
		classified.Cooldown = 0
	}
	switch classified.Kind {
	case accounts.KindRateLimit:
		switch {
		case providerErr.RetryAfterHeader != "":
			classified.Cooldown = ParseRetryAfterHint(providerErr.Message, providerErr.RetryAfterHeader, classified.Cooldown)
			if classified.Cooldown < minRateLimitCooldown {
				classified.Cooldown = minRateLimitCooldown
			}
		case providerErr.Code == "4011" && providerErr.RetryAfter <= 0:
			classified.Cooldown = 5 * time.Minute
		case providerErr.RetryAfter > 0:
			classified.Cooldown = providerErr.RetryAfter
			if classified.Cooldown < 30*time.Second {
				classified.Cooldown = 30 * time.Second
			}
		}
	case accounts.KindQuota:
		// Use only a reset actually provided by the upstream.
		if providerErr.RetryAfterHeader != "" {
			classified.Cooldown = ParseRetryAfterHint(providerErr.Message, providerErr.RetryAfterHeader, classified.Cooldown)
		} else if providerErr.RetryAfter > 0 {
			classified.Cooldown = providerErr.RetryAfter
		}
	}
	classified.RetryAfter = classified.Cooldown
	return classified
}

func copyProviderErrorEvidence(classified *Classified, err error) {
	if classified == nil || err == nil {
		return
	}
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) || providerErr == nil {
		return
	}
	classified.ErrorPhase = providerErr.Phase
	classified.ErrorSource = providerErr.Source
	if providerErr.HTTPStatus != nil {
		status := *providerErr.HTTPStatus
		classified.HTTPStatus = &status
	}
}
