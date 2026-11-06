package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/config"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

type syntheticUnconfirmedCheckin struct{}

func (syntheticUnconfirmedCheckin) Checkin(context.Context, string) (providers.CheckinResult, error) {
	return providers.CheckinResult{Status: "success", Message: "checkin claimed; outcome=success; phase=status; attempts=3; http=200; code=0; reward=unconfirmed"}, nil
}
func TestCheckinAPIKeepsUnconfirmedRewardUnknown(t *testing.T) {
	srv := New(config.Config{Host: "127.0.0.1", ProxyAPIKey: "test-key", QoderHome: t.TempDir(), DataDir: t.TempDir()})
	defer srv.Close()
	srv.Providers.Register(providers.Adapter{ID: "trae", Checkin: syntheticUnconfirmedCheckin{}})
	account, err := srv.Manager.Store().Create(context.Background(), accounts.CreateAccount{Name: "synthetic", Provider: "trae", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/"+account.ID+"/checkin", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	out := httptest.NewRecorder()
	srv.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK || !bytes.Contains(out.Body.Bytes(), []byte(`"last_checkin_status":"success"`)) || !bytes.Contains(out.Body.Bytes(), []byte("reward=unconfirmed")) || bytes.Contains(out.Body.Bytes(), []byte(`"reward_credits"`)) {
		t.Fatalf("code=%d body=%s", out.Code, out.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/accounts/"+account.ID+"/checkins", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	out = httptest.NewRecorder()
	srv.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK || !bytes.Contains(out.Body.Bytes(), []byte("reward=unconfirmed")) || bytes.Contains(out.Body.Bytes(), []byte(`"reward_credits"`)) {
		t.Fatalf("history code=%d body=%s", out.Code, out.Body.String())
	}
}
