package providers

import "testing"

func TestMatchReasoningIsCatalogBoundAndPreservesIntent(t *testing.T) {
	cases := []struct {
		input string
		caps  ModelCapabilities
		want  string
	}{
		{"high", ModelCapabilities{ReasoningOptions: []string{"medium", "low"}}, "medium"},
		{"low", ModelCapabilities{ReasoningOptions: []string{"xhigh", "high"}}, "high"},
		{"max", ModelCapabilities{ReasoningOptions: []string{"high", "xhigh", "max"}}, "max"},
		{"none", ModelCapabilities{ReasoningOptions: []string{"high"}}, ""},
		{"none", ModelCapabilities{ReasoningOptions: []string{"none", "high"}, CanDisableThinking: true}, "none"},
		{"medium", ModelCapabilities{ReasoningOptions: []string{"none"}, CanDisableThinking: true}, ""},
		{"new-tier", ModelCapabilities{ReasoningOptions: []string{"high"}}, ""},
		{"high", ModelCapabilities{}, ""},
	}
	for _, tc := range cases {
		if got := MatchReasoningLevel(tc.input, tc.caps); got != tc.want {
			t.Errorf("%s options=%v got=%q want=%q", tc.input, tc.caps.ReasoningOptions, got, tc.want)
		}
	}
}
