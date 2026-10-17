package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/control"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestGatewayModelsAdvertisesPinnedRoutesWithoutChangingBareRouting(t *testing.T) {
	raw := []map[string]any{
		{"id": "glm-5.3", "provider": "workbuddy", "native_model": "deep-model", "display_name": "GLM-5.3", "regions": []string{"cn"}},
		{"id": "glm-5.3", "provider": "trae", "display_name": "GLM-5.3", "regions": []string{"cn"}},
		{"id": "glm-5.3", "provider": "qoder", "regions": []string{"cn"}},
	}
	a := &App{Control: &control.Services{Catalog: control.NewCatalog(func(bool, string, control.CatalogMode) ([]map[string]any, error) {
		return raw, nil
	})}}
	h := a.newGateway()
	for _, enabled := range []bool{true, false} {
		a.CrossProviderModelPool.Store(enabled)
		for _, identity := range []auth.Identity{auth.ConsoleIdentity(), {Kind: auth.KindKey, AllowedProviders: []string{"workbuddy:cn"}}} {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))
			rec := httptest.NewRecorder()
			h.HandleModels(rec, req)
			var list struct {
				Data []struct {
					ID       string `json:"id"`
					Provider string `json:"provider"`
				} `json:"data"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
				t.Fatalf("models=%d %s", rec.Code, rec.Body.String())
			}
			want := 3
			if identity.Kind == auth.KindKey {
				want = 1
			}
			if len(list.Data) != want {
				t.Fatalf("identity=%+v pool=%v models=%s", identity, enabled, rec.Body.String())
			}
			seen := map[string]bool{}
			for _, model := range list.Data {
				if model.ID != model.Provider+"/glm-5.3" || seen[model.ID] {
					t.Fatalf("ambiguous public model: %+v", model)
				}
				seen[model.ID] = true
				prepared, err := h.Executor.Prepare(executor.PrepareInput{Context: context.Background(), Identity: identity, CrossProviderPool: enabled, Request: translate.ChatRequest{Model: model.ID}})
				if err != nil || prepared.ProviderFilter != model.Provider || prepared.Request.Model != "glm-5.3" {
					t.Fatalf("advertised model cannot pin the matching upstream: %+v %v", prepared, err)
				}
			}
		}
		prepared, err := h.Executor.Prepare(executor.PrepareInput{Identity: auth.ConsoleIdentity(), CrossProviderPool: enabled, Request: translate.ChatRequest{Model: "glm-5.3"}})
		if enabled && (err != nil || prepared.ProviderFilter != "") || !enabled && err == nil {
			t.Fatalf("bare-model routing setting changed: enabled=%v prepared=%+v error=%v", enabled, prepared, err)
		}
	}
	internal, err := a.Control.Catalog.Get(false, "", control.CatalogModeMerge)
	if err != nil || internal[0]["id"] != "glm-5.3" || internal[0]["native_model"] != "deep-model" {
		t.Fatalf("public request changed internal alias/settings identity: %v %v", internal, err)
	}
}
