package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/auth"
	appconsole "github.com/caigee-cmd/cli2api/internal/console"
	"github.com/caigee-cmd/cli2api/internal/control"
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/store"
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

func TestGatewayModelsExportsEffectiveTraeCapacityWithoutChangingConsoleOrCache(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	raw := []map[string]any{
		{
			"id": "deepseek-v4.1-flash", "provider": "trae", "owned_by": "trae", "regions": []string{"cn"},
			"catalog_context_length": 100000, "catalog_context_length_max": 1000000,
			"prompt_max_tokens": 100000, "prompt_max_tokens_max": 936000,
			"max_output_tokens": 16000, "max_output_tokens_max": 64000,
		},
		{
			"id": "trae-default-only", "provider": "trae", "owned_by": "trae", "regions": []string{"cn"},
			"catalog_context_length": 200000, "catalog_context_length_max": 1000000,
			"prompt_max_tokens": 180000, "max_output_tokens": 32000,
		},
		{
			"id": "deepseek-v4.1-flash", "provider": "workbuddy", "owned_by": "workbuddy", "regions": []string{"cn"},
			"catalog_context_length": 1000000, "max_output_tokens": 128000,
		},
	}
	catalog := control.NewCatalog(func(bool, string, control.CatalogMode) ([]map[string]any, error) {
		return raw, nil
	})
	settings := control.NewSettings(db)
	settings.BindCatalog(catalog)
	services := &control.Services{Catalog: catalog, Settings: settings}
	app := &App{Control: services}
	publicHandler := app.newGateway()
	consoleHandler := &appconsole.Handler{
		Control: services,
		FetchDisplayModels: func(refresh bool, accountID string, mode control.CatalogMode) ([]map[string]any, error) {
			return catalog.Get(refresh, accountID, mode)
		},
		DecorateModels: func(ctx context.Context, models []map[string]any) []map[string]any {
			return control.DecorateModelsWithContext(ctx, settings, models)
		},
	}

	readPublic := func() []map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.ConsoleIdentity()))
		rec := httptest.NewRecorder()
		publicHandler.HandleModels(rec, req)
		var response struct {
			Data []map[string]any `json:"data"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil {
			t.Fatalf("public model listing status=%d body=%s", rec.Code, rec.Body.String())
		}
		return response.Data
	}
	readConsole := func() []map[string]any {
		rec := httptest.NewRecorder()
		consoleHandler.HandleModelsAPI(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
		var response struct {
			Data []map[string]any `json:"data"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil {
			t.Fatalf("console model listing status=%d body=%s", rec.Code, rec.Body.String())
		}
		return response.Data
	}
	find := func(models []map[string]any, provider, id string) map[string]any {
		t.Helper()
		for _, model := range models {
			if model["provider"] == provider && (model["id"] == id || model["id"] == provider+"/"+id) {
				return model
			}
		}
		t.Fatalf("missing %s model %s in %#v", provider, id, models)
		return nil
	}
	assertInt := func(model map[string]any, field string, want int) {
		t.Helper()
		var got int
		switch value := model[field].(type) {
		case int:
			got = value
		case int64:
			got = int(value)
		case float64:
			got = int(value)
		default:
			t.Fatalf("%s[%s]=%#v, want %d", model["id"], field, model[field], want)
		}
		if got != want {
			t.Fatalf("%s[%s]=%d, want %d", model["id"], field, got, want)
		}
	}
	setMaxMode := func(model string, enabled bool) {
		t.Helper()
		_, err := settings.UpdateModelSetting(ctx, "trae", model, nil, control.ProviderModelSettingPatch{MaxMode: &enabled})
		if err != nil {
			t.Fatalf("set Max mode %s=%v: %v", model, enabled, err)
		}
	}

	defaultListing := readPublic()
	traeDefault := find(defaultListing, "trae", "deepseek-v4.1-flash")
	assertInt(traeDefault, "max_output_tokens", 16000)
	assertInt(traeDefault, "prompt_max_tokens", 100000)
	assertInt(find(defaultListing, "workbuddy", "deepseek-v4.1-flash"), "max_output_tokens", 128000)

	setMaxMode("deepseek-v4.1-flash", true)
	setMaxMode("trae-default-only", true)
	maxListing := readPublic()
	traeMax := find(maxListing, "trae", "deepseek-v4.1-flash")
	assertInt(traeMax, "max_output_tokens", 64000)
	assertInt(traeMax, "prompt_max_tokens", 936000)
	assertInt(traeMax, "max_output_tokens_max", 64000)
	assertInt(traeMax, "prompt_max_tokens_max", 936000)
	if traeMax["max_mode"] != true {
		t.Fatalf("Trae Max setting was not reflected: %#v", traeMax)
	}
	traeWithoutMaxOutput := find(maxListing, "trae", "trae-default-only")
	assertInt(traeWithoutMaxOutput, "max_output_tokens", 32000)
	assertInt(traeWithoutMaxOutput, "prompt_max_tokens", 180000)
	assertInt(find(maxListing, "workbuddy", "deepseek-v4.1-flash"), "max_output_tokens", 128000)

	// A public projection must not replace the raw defaults in the shared catalog
	// or the values the console uses to render its own default/Max toggle.
	consoleListing := readConsole()
	traeConsole := find(consoleListing, "trae", "deepseek-v4.1-flash")
	assertInt(traeConsole, "max_output_tokens", 16000)
	assertInt(traeConsole, "prompt_max_tokens", 100000)
	assertInt(traeConsole, "max_output_tokens_max", 64000)
	assertInt(traeConsole, "prompt_max_tokens_max", 936000)
	cache, err := catalog.Get(false, "", control.CatalogModeMerge)
	if err != nil {
		t.Fatal(err)
	}
	assertInt(find(cache, "trae", "deepseek-v4.1-flash"), "max_output_tokens", 16000)
	assertInt(find(cache, "trae", "deepseek-v4.1-flash"), "prompt_max_tokens", 100000)

	setMaxMode("deepseek-v4.1-flash", false)
	setMaxMode("trae-default-only", false)
	maxDisabled := readPublic()
	traeAfterDisable := find(maxDisabled, "trae", "deepseek-v4.1-flash")
	assertInt(traeAfterDisable, "max_output_tokens", 16000)
	assertInt(traeAfterDisable, "prompt_max_tokens", 100000)
	if traeAfterDisable["max_mode"] != false {
		t.Fatalf("disabled Max mode did not restore the default state: %#v", traeAfterDisable)
	}
	assertInt(find(maxDisabled, "trae", "trae-default-only"), "max_output_tokens", 32000)
	assertInt(find(maxDisabled, "workbuddy", "deepseek-v4.1-flash"), "max_output_tokens", 128000)
}
