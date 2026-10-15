package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestNamedKeyCarriesIndependentRoutingPolicyAndFence(t *testing.T) {
	key := accounts.APIKey{ID: "key_1", Enabled: true, Providers: []string{"workbuddy:cn"}, RoutingPolicy: accounts.KeyRoutingPolicy{
		Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", Priority: 10, MaxInFlight: 2}}, CrossProviderFailover: true,
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer named-secret")
	id, ok := NewVerifier("console", stubKeys{key: key, ok: true}).Authenticate(context.Background(), req)
	if !ok || !id.RoutingPolicy.CrossProviderFailover || id.RoutingPolicy.Platforms[0].MaxInFlight != 2 {
		t.Fatalf("routing policy missing: %+v", id)
	}
	if id.AllowsProviderRegion("trae", "cn") {
		t.Fatal("routing policy widened authorization fence")
	}
	id.RoutingPolicy.Platforms[0].MaxInFlight = 100
	if key.RoutingPolicy.Platforms[0].MaxInFlight != 2 {
		t.Fatal("identity aliases mutable key policy")
	}
}
