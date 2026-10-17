package control

import (
	"reflect"
	"testing"
)

func TestPublicModelCatalogPreservesAliasesAndDoesNotMutateCache(t *testing.T) {
	models := []map[string]any{
		{"id": "glm-5.3", "provider": "workbuddy", "native_model": "deep-model", "display_name": "GLM-5.3", "catalog_context_length": 128000},
		{"id": "glm-5.3", "provider": "trae", "native_model": "glm-5.3", "display_name": "GLM-5.3", "catalog_context_length": 64000},
		{"id": "qwen3.8-flash", "owned_by": "qoder", "native_model": "qfmodel"},
		{"id": "command/deepseek/model", "provider": "command", "display_name": "command/DeepSeek"},
		{"id": "moonshotai/model", "provider": "command"},
	}
	before := CloneModelList(models)
	public := PublicModelCatalog(models)
	want := []string{"workbuddy/glm-5.3", "trae/glm-5.3", "qoder/qwen3.8-flash", "command/deepseek/model", "command/moonshotai/model"}
	for i, id := range want {
		if public[i]["id"] != id {
			t.Fatalf("id[%d]=%v, want %q", i, public[i]["id"], id)
		}
	}
	if public[0]["display_name"] != "workbuddy/GLM-5.3" || public[1]["display_name"] != "trae/GLM-5.3" || public[2]["display_name"] != want[2] {
		t.Fatalf("ambiguous client display names: %v", public)
	}
	if public[0]["native_model"] != "deep-model" || public[2]["native_model"] != "qfmodel" || public[0]["catalog_context_length"] != 128000 || public[1]["catalog_context_length"] != 64000 {
		t.Fatalf("upstream alias or provider capabilities changed: %v", public)
	}
	if !reflect.DeepEqual(models, before) {
		t.Fatalf("presentation mutated shared settings/catalog: %v", models)
	}
	if twice := PublicModelCatalog(public); !reflect.DeepEqual(public, twice) {
		t.Fatalf("repeated presentation adds duplicate prefixes: %v", twice)
	}
}

func TestPublicModelCatalogDoesNotInventUnknownProviderPrefixes(t *testing.T) {
	models := []map[string]any{{"id": "vendor/model", "owned_by": "unknown"}, {"id": "", "provider": "trae"}}
	if got := PublicModelCatalog(models); !reflect.DeepEqual(got, models) {
		t.Fatalf("invented an unsupported route: %v", got)
	}
	if PublicModelCatalog(nil) != nil {
		t.Fatal("nil catalog changed")
	}
}
