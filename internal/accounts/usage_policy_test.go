package accounts

import (
	"math"
	"testing"
)

func TestUsagePolicyNormalizesKnownProviderModelAndRejectsAliasCollisions(t *testing.T) {
	p, err := NormalizeUsagePolicy(UsagePolicy{ModelDailyTokens: map[string]int64{"workbuddy/kimi-k3": 10}})
	if err != nil || p.ModelDailyTokens["kimi-k3"] != 10 {
		t.Fatalf("policy=%+v err=%v", p, err)
	}
	for _, key := range []string{"madeup/kimi-k3", "workbuddy/", "workbuddy/a/b", "auto"} {
		if _, err = NormalizeUsagePolicy(UsagePolicy{ModelDailyTokens: map[string]int64{key: 1}}); err == nil {
			t.Fatalf("accepted %q", key)
		}
	}
	if _, err = NormalizeUsagePolicy(UsagePolicy{ModelDailyTokens: map[string]int64{"workbuddy/kimi-k3": 10, "kimi_k3": 20}}); err == nil {
		t.Fatal("accepted conflicting canonical model limits")
	}
	if _, err = NormalizeUsagePolicy(UsagePolicy{DailyCredits: math.NaN()}); err == nil {
		t.Fatal("accepted NaN")
	}
}

func TestKeyRoutingPolicyValidatesPlatformAndLimits(t *testing.T) {
	for _, p := range []KeyRoutingPolicy{{Platforms: []PlatformRoutePolicy{{Provider: "workbuddy", Region: "unknown"}}}, {Platforms: []PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", MaxInFlight: -1}}}, {Platforms: []PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn"}, {Provider: "workbuddy", Region: "cn"}}}} {
		if _, err := NormalizeKeyRoutingPolicy(p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	p, err := NormalizeKeyRoutingPolicy(KeyRoutingPolicy{})
	if err != nil || p.Platforms == nil || p.CrossProviderFailover {
		t.Fatalf("default=%+v err=%v", p, err)
	}
}
