package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckinRewardJSONDistinguishesUnknownConfirmedAndZero(t *testing.T) {
	zero, positive := 0.0, 123.5
	for _, tc := range []struct {
		name     string
		reward   *float64
		contains string
		absent   bool
	}{
		{"unknown", nil, "reward_credits", true},
		{"confirmed_zero", &zero, `"reward_credits":0`, false},
		{"confirmed_positive", &positive, `"reward_credits":123.5`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(CheckinResult{Status: "success", RewardCredits: tc.reward})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), tc.contains) == tc.absent {
				t.Fatalf("json=%s", encoded)
			}
		})
	}
}
