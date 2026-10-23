package workbuddy

import (
	"errors"
	"net/http"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestSessionDeadResponsesRemainAccountScoped(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "HTTP 401", status: http.StatusUnauthorized, body: "{\"message\":\"session expired\"}"},
		{name: "structured 12153", status: http.StatusBadRequest, body: "{\"code\":12153,\"message\":\"session dead\"}"},
		{name: "known plain session rejection", status: http.StatusBadGateway, body: "Offline user session not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			classified := Classify(tc.status, tc.body)
			if classified.Kind != accounts.KindAuth || classified.Scope != "account" {
				t.Fatalf("session rejection must remain account-scoped auth: %+v", classified)
			}
		})
	}
}

func TestCredentialAndRefreshFormatErrorsRemainAccountScoped(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "credential read", err: workBuddyCredentialReadError(errors.New("synthetic read failure"), "test")},
		{name: "credential format", err: workBuddyCredentialPayloadError(errors.New("synthetic malformed payload"))},
		{name: "refresh format", err: workBuddyRefreshResponseError(http.StatusBadGateway)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var providerErr *providers.Error
			if !errors.As(tc.err, &providerErr) || providerErr.Scope != "account" {
				t.Fatalf("credential boundary must retain account scope: %#v", tc.err)
			}
		})
	}
}
