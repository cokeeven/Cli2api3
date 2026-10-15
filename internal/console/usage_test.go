package console_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/console"
	"github.com/caigee-cmd/cli2api/internal/control"
	"github.com/caigee-cmd/cli2api/internal/store"
)

func TestUsageHTTPPolicyAnalysisAndExplicitReconciliation(t *testing.T) {
	s, err := store.OpenStore(filepath.Join(t.TempDir(), "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, err := s.Create(ctx, accounts.CreateAccount{Name: "test", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	h := &console.Handler{Control: &control.Services{Usage: control.NewUsage(s)}}
	call := func(method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/accounts/"+a.ID+suffix, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.HandleAccountUsage(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/usage-policy", "", 200},
		{http.MethodPut, "/usage-policy", `{"daily_tokens":100,"prefer_expiring":true}`, 200},
		{http.MethodPut, "/usage-policy", `{"daily_tokens":-1}`, 400},
		{http.MethodPut, "/usage-policy", `not json`, 400},
		{http.MethodPost, "/usage", "", 405},
		{http.MethodGet, "/usage?days=7", "", 200},
		{http.MethodPost, "/usage-reconcile", `{"confirm_covered":false}`, 400},
		{http.MethodGet, "/usage-reconcile", "", 405},
	} {
		w := call(tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	var view accounts.UsageAnalysis
	w := call(http.MethodGet, "/usage?days=7", "")
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Policy.DailyTokens != 100 || !view.Policy.PreferExpiring || view.Quota.Known {
		t.Fatalf("analysis=%+v", view)
	}
	now := time.Now().UTC()
	if err = s.ReserveUsage(ctx, accounts.UsageReservation{ID: "pending", AccountID: a.ID, Model: "kimi-k3", EstimatedTokens: 10, At: now.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkUsageDispatched(ctx, "pending"); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleUsage(ctx, accounts.UsageSettlement{ID: "pending", At: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	q := &accounts.QuotaSnapshot{Remaining: 50, Unit: "credits", FetchedAt: now.Format(time.RFC3339Nano)}
	if err = s.SaveQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPost, "/usage-reconcile", `{"confirm_covered":true,"snapshot_fetched_at":"`+q.FetchedAt+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"covered_attempts":1`) {
		t.Fatalf("reconcile status=%d body=%s", w.Code, w.Body)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/accounts/missing/usage", nil)
	w = httptest.NewRecorder()
	h.HandleAccountUsage(w, r)
	if w.Code != 404 {
		t.Fatalf("missing status=%d", w.Code)
	}
}

func TestKeyHTTPRoutingPolicyPreservesExistingFenceAndSecret(t *testing.T) {
	s, err := store.OpenStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := &console.Handler{Control: &control.Services{Keys: control.NewKeys(s)}}
	w := httptest.NewRecorder()
	h.HandleAPIKeys(w, httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(`{"name":"route","providers":["workbuddy:cn"],"routing_policy":{"platforms":[{"provider":"workbuddy","region":"cn","priority":20,"max_inflight":2}],"cross_provider_failover":false}}`)))
	if w.Code != 201 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body)
	}
	var created accounts.APIKey
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" {
		t.Fatal("create omitted one-time secret")
	}
	w = httptest.NewRecorder()
	h.HandleAPIKeyByID(w, httptest.NewRequest(http.MethodPatch, "/api/keys/"+created.ID, strings.NewReader(`{"name":"renamed"}`)))
	if w.Code != 200 {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body)
	}
	var updated accounts.APIKey
	if err = json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Secret != "" || updated.Name != "renamed" || len(updated.Providers) != 1 || updated.Providers[0] != "workbuddy:cn" || updated.RoutingPolicy.Platforms[0].MaxInFlight != 2 {
		t.Fatalf("partial update lost key settings")
	}
	matched, ok, err := s.LookupAPIKey(context.Background(), created.Secret)
	if err != nil || !ok || matched.ID != created.ID {
		t.Fatal("routing policy update changed the key secret")
	}
	w = httptest.NewRecorder()
	h.HandleAPIKeyByID(w, httptest.NewRequest(http.MethodPatch, "/api/keys/"+created.ID, strings.NewReader(`{"routing_policy":{"platforms":[{"provider":"workbuddy","region":"cn","max_inflight":-1}]}}`)))
	if w.Code != 400 {
		t.Fatalf("invalid policy status=%d", w.Code)
	}
}
