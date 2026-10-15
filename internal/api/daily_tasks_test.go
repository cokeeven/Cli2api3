package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/config"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

type dailyTaskHTTPFixture struct{}

func (dailyTaskHTTPFixture) DailyCheckin(context.Context, string) (string, error) { return "ok", nil }
func (dailyTaskHTTPFixture) Keepalive(context.Context, string) error              { return nil }
func (dailyTaskHTTPFixture) RunDailyTasks(context.Context, string) (string, error) {
	panic("legacy runner called")
}
func (dailyTaskHTTPFixture) RunDailyTasksReport(context.Context, string, []providers.DailyTaskStep) (*providers.DailyTasksReport, error) {
	r := &providers.DailyTasksReport{}
	r.Add("checkin", "success", "checked in")
	r.Add("growth", "error", "upstream request failed")
	return r, nil
}

func TestDailyTasksHTTPHistoryStatusAuthAndConflict(t *testing.T) {
	root := t.TempDir()
	srv := New(config.Config{DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "runtime"), ProxyAPIKey: "daily-test-key"})
	defer srv.Close()
	srv.Manager.SetWorkBuddy(dailyTaskHTTPFixture{})
	account, err := srv.Control.Accounts.Create(context.Background(), accounts.CreateAccount{Name: "daily", Provider: "workbuddy", Region: "cn"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/accounts/" + account.ID + "/daily-tasks"
	request := func(method, url string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer daily-test-key")
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := request(method, path, false); w.Code != 401 {
			t.Fatalf("unauthorized %s=%d", method, w.Code)
		}
	}
	w := request(http.MethodPost, path, true)
	var result struct {
		Status  string                `json:"status"`
		Summary string                `json:"summary"`
		Run     accounts.DailyTaskRun `json:"run"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Status != "partial" || result.Run.ID == "" {
		t.Fatalf("post=%d %s err=%v", w.Code, w.Body, err)
	}
	w = request(http.MethodGet, path+"?limit=20", true)
	var history struct {
		Data []accounts.DailyTaskRun `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || len(history.Data) != 1 || history.Data[0].Status != "partial" {
		t.Fatalf("history=%d %s err=%v", w.Code, w.Body, err)
	}
	if w = request(http.MethodPut, path, true); w.Code != 405 {
		t.Fatalf("method=%d", w.Code)
	}
	if w = request(http.MethodGet, "/api/accounts/missing/daily-tasks", true); w.Code != 404 {
		t.Fatalf("missing=%d", w.Code)
	}
	store := srv.Manager.Store().(accounts.DailyTaskStore)
	now := time.Now()
	if _, err = store.ClaimDailyTaskRun(context.Background(), account.ID, now.Format("2006-01-02"), "manual", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if w = request(http.MethodPost, path, true); w.Code != 409 {
		t.Fatalf("conflict=%d %s", w.Code, w.Body)
	}
}
