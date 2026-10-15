package trae

import (
	"strings"
	"testing"
)

func TestNonStreamAbsentUsageIsNotActualZero(t *testing.T) {
	for _, tc := range []struct{ event, source string }{
		{"", "unknown"},
		{"event: token_usage\ndata: {\"prompt_tokens\":0,\"completion_tokens\":0}\n\n", "upstream"},
		{"event: token_usage\ndata: {\"prompt_tokens\":3}\n\n", "unknown"},
		{"event: token_usage\ndata: {\"prompt_tokens\":3,\"completion_tokens\":2,\"source\":\"estimate\"}\n\n", "estimate"},
	} {
		a, err := Aggregate(strings.NewReader("event: output\ndata: {\"response\":\"OK\"}\n\n" + tc.event + "event: done\ndata: {}\n\n"))
		if err != nil {
			t.Fatal(err)
		}
		o, err := outcomeFromAggregate(a, "model")
		if err != nil {
			t.Fatal(err)
		}
		if o.UsageSource != tc.source {
			t.Fatalf("source=%s want %s", o.UsageSource, tc.source)
		}
	}
}
