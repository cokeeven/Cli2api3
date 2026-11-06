package workbuddy

import (
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"testing"
)

func TestStructuredModelServiceNotFound11102IsNarrow(t *testing.T) {
	yes := Classify(500, `{"code":11102,"msg":"model [m] service info not found"}`)
	if yes.Kind != accounts.KindUnavailable || yes.Scope != "model" || yes.Code != "11102" || !yes.RetrySafe {
		t.Fatalf("%+v", yes)
	}
	for _, body := range []string{`{"code":11102,"msg":"other failure"}`, `{"requestId":"11102","msg":"model [m] service info not found"}`, `model 11102 service info not found`} {
		got := Classify(500, body)
		if got.Scope == "model" || got.RetrySafe {
			t.Fatalf("unsafe inference %+v", got)
		}
	}
}

func TestBusinessCode6004AndGenuineAuthScopes(t *testing.T) {
	for _, body := range []string{`{"code":6004,"msg":"rate limit"}`, `{"code":"6004","msg":"rate limit"}`} {
		got := Classify(429, body)
		if got.Code != "6004" || got.Scope != "model" || !got.RetrySafe {
			t.Fatalf("%s %+v", body, got)
		}
	}
	for _, tc := range []struct {
		status int
		body   string
	}{{401, `{"code":6004,"msg":"rate limit"}`}, {403, `{"code":12153,"msg":"session dead"}`}} {
		got := Classify(tc.status, tc.body)
		if got.Kind != accounts.KindAuth || got.Scope != "account" {
			t.Fatalf("%d %+v", tc.status, got)
		}
	}
}
