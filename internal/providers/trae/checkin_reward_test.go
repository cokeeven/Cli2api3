package trae

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCheckinRuleAmountsNeverBecomeConfirmedReward(t *testing.T) {
	cases := []struct{ name, fields string }{
		{"rule_values", `,"credits":100,"extra_credits":100`},
		{"zero_rule", `,"credits":0,"extra_credits":0`},
		{"missing_rule", ``},
		{"explicit_null_rule", `,"credits":null,"extra_credits":null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statuses := 0
			c, s := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == pathCheckinStatus {
					statuses++
					checked := statuses > 1
					fmt.Fprintf(w, `{"code":0,"checked_in":%t,"enable":true%s}`, checked, tc.fields)
					return
				}
				// This synthetic, unverified field must not be treated as proof either.
				fmt.Fprint(w, `{"code":0,"reward_credits":200}`)
			}))
			seedCheckinCredential(t, s)
			result, err := c.Checkin(context.Background(), "acc1")
			if err != nil || result.Status != "success" || result.RewardCredits != nil || !strings.Contains(result.Message, "reward=unconfirmed") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), `"reward_credits"`) {
				t.Fatalf("unexpected amount=%s", encoded)
			}
		})
	}
}
