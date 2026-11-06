package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/control"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/runtime"
	"github.com/caigee-cmd/cli2api/internal/store"
)

type testCredentialForcer struct{ err error }

func (f *testCredentialForcer) Keepalive(context.Context, string) error              { return nil }
func (f *testCredentialForcer) ForceCredentialRefresh(context.Context, string) error { return f.err }

func TestCredentialRefreshResponseDistinguishesOutcomes(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Create(context.Background(), accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m := runtime.NewManager(runtime.ManagerConfig{DataDir: t.TempDir()}, db, nil)
	defer m.Close()
	fake := &testCredentialForcer{}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", CredentialMaintenance: fake})
	m.SetProviders(registry)
	h := &Handler{Control: control.New(m)}
	for _, tc := range []struct {
		name    string
		err     error
		status  string
		relogin bool
	}{
		{"success", nil, "success", false},
		{"rejected", &providers.Error{Kind: accounts.KindAuth, Status: 401, Message: "synthetic secret must not be returned"}, "rejected", true},
		{"missing", providers.ErrCredentialMissing, "missing", true},
		{"conflict", providers.ErrCredentialSuperseded, "superseded", false},
		{"unavailable", errors.New("synthetic secret must not be returned"), "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake.err = tc.err
			w := httptest.NewRecorder()
			h.HandleAccountByID(w, httptest.NewRequest(http.MethodPost, "/api/accounts/"+a.ID+"/credential-refresh", nil))
			var got struct {
				Status  string `json:"status"`
				Relogin bool   `json:"relogin_required"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != tc.status || got.Relogin != tc.relogin {
				t.Fatalf("response %d %s", w.Code, w.Body.String())
			}
			if tc.err != nil && len(w.Body.String()) > 180 {
				t.Fatal("unexpected detail in response")
			}
		})
	}
}
