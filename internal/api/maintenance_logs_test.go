package api

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
	"github.com/caigee-cmd/cli2api/internal/config"
)

func TestMaintenanceLogsHTTPAuthFiltersAndSafeProjection(t *testing.T) {
	root := t.TempDir()
	srv := New(config.Config{DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "runtime"), ProxyAPIKey: "maintenance-test-key"})
	defer srv.Close()
	ctx := context.Background()
	account, err := srv.Control.Accounts.Create(ctx, accounts.CreateAccount{Name: "history", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	if err = srv.Manager.Store().RecordCheckin(accounts.WithMaintenanceTrigger(ctx, "manual"), account.ID, "error", "Authorization: Bearer sensitive-fixture-value", time.Now()); err != nil {
		t.Fatal(err)
	}
	request := func(method, url string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer maintenance-test-key")
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/api/logs/maintenance", false); w.Code != 401 {
		t.Fatalf("auth=%d", w.Code)
	}
	w := request(http.MethodGet, "/api/logs/maintenance?provider=workbuddy&region=cn&kind=checkin&trigger=manual&status=error&limit=1", true)
	var page accounts.MaintenanceLogPage
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Total != 1 || page.Count != 1 {
		t.Fatalf("page status=%d err=%v", w.Code, err)
	}
	if strings.Contains(w.Body.String(), "sensitive-fixture-value") || page.Items[0].Steps[0].Message != "Sensitive upstream details omitted" {
		t.Fatal("sensitive historical details leaked")
	}
	if w = request(http.MethodPost, "/api/logs/maintenance", true); w.Code != 405 {
		t.Fatalf("method=%d", w.Code)
	}
	for _, query := range []string{"from=invalid", "from=2026-10-16&to=2026-10-15"} {
		if w = request(http.MethodGet, "/api/logs/maintenance?"+query, true); w.Code != 400 {
			t.Fatalf("filter=%d", w.Code)
		}
	}
}
