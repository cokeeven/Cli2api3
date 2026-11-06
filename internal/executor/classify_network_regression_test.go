package executor

import (
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestNetworkPortsAreNotAuthStatus(t *testing.T) {
	for _, body := range []string{
		`dial tcp 127.0.0.1:61401: connectex: connection timed out`,
		`read tcp 127.0.0.1:61403->127.0.0.1:443: wsarecv: connection timed out`,
		`dial tcp 127.0.0.1:61402: connection refused`,
	} {
		if got := Classify(0, body, "", "", ""); got.Kind != KindUnavailable {
			t.Errorf("Classify(%q) = %+v; want unavailable", body, got)
		}
	}
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, `upstream denied`}, {403, `upstream denied`},
		{0, `{"error":{"code":401,"message":"rejected"}}`},
		{0, `{"error":{"code":"403","message":"rejected"}}`},
	} {
		if got := Classify(tc.status, tc.body, "", "", ""); got.Kind != KindAuth {
			t.Errorf("Classify(%d, %q) = %+v; want auth", tc.status, tc.body, got)
		}
	}
}

func TestStructuredProviderErrorKeepsAuthAndRateLimitEvidence(t *testing.T) {
	auth := ClassifyError(&providers.Error{Kind: KindAuth, Status: 401, Code: "12153", Scope: "account", Message: "session dead"})
	if auth.Kind != KindAuth || auth.Code != "12153" || auth.Scope != "account" || !auth.Failover {
		t.Fatalf("auth evidence changed: %+v", auth)
	}
	rate := ClassifyError(&providers.Error{Kind: KindRateLimit, Status: 429, Code: "6004", Scope: "model", RetrySafe: true, RetryAfter: time.Minute})
	if rate.Kind != KindRateLimit || rate.Code != "6004" || rate.Scope != "model" || !rate.RetrySafe || rate.Cooldown != time.Minute {
		t.Fatalf("rate limit evidence changed: %+v", rate)
	}
}
