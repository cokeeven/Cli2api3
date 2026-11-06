package executor

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestReviewerTransportErrorURLContainsCredentialWord(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://service.invalid/api/credential-check", Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection timed out")}}
	got := ClassifyError(err)
	if got.Kind == KindAuth {
		t.Fatalf("transport error with no HTTP response was classified auth: %+v", got)
	}
}

func TestTransportURLTextDoesNotOverrideTypedEvidence(t *testing.T) {
	for _, path := range []string{"credential", "access-token/401", "refresh-token/403"} {
		err := &url.Error{Op: "Post", URL: "https://service.invalid/" + path, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
		if got := ClassifyError(err); got.Kind != KindUnavailable {
			t.Fatalf("%s classified %+v", path, got)
		}
	}
	for _, status := range []int{401, 403} {
		httpStatus := status
		err := &providers.Error{Kind: KindAuth, Status: status, HTTPStatus: &httpStatus, Scope: "account", Cause: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("network")}}
		if got := ClassifyError(err); got.Kind != KindAuth || got.Status != status || got.HTTPStatus == nil || *got.HTTPStatus != status {
			t.Fatalf("typed auth %d: %+v", status, got)
		}
	}
	wrapped := &providers.Error{Message: "credential endpoint connection failed", Scope: "model", RetrySafe: true, Cause: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}}
	if got := ClassifyError(wrapped); got.Kind != KindUnavailable || got.Scope != "model" || !got.RetrySafe {
		t.Fatalf("wrapped transport classified %+v", got)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := ClassifyError(err); got.Kind != KindCanceled {
			t.Fatalf("context %v: %+v", err, got)
		}
	}
}
