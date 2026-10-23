package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestPoolPreservesExplicitModelScopeForUnavailableAndAuth(t *testing.T) {
	for _, kind := range []string{KindUnavailable, KindAuth} {
		t.Run(kind, func(t *testing.T) {
			pool := NewPool(nil, nil)
			pool.Upsert(Item{ID: "wb-a", Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
			pool.MarkClassified("wb-a", Classified{
				Kind: kind, Scope: "model", Model: "minimax-m3", Cooldown: time.Minute,
				Code: "synthetic", Message: "synthetic model-scoped failure",
			})

			item, ok := pool.ByID("wb-a")
			if !ok {
				t.Fatal("account missing")
			}
			if !item.DownUntil.IsZero() {
				t.Fatalf("explicit model scope caused account cooldown: %v", item.DownUntil)
			}
			if item.ModelDownUntil["minimax-m3"].IsZero() {
				t.Fatalf("explicit model scope was not retained: %+v", item.ModelDownUntil)
			}
			evidence := item.CooldownEvidence["minimax-m3"]
			if evidence.Scope != "model" || evidence.ReportedScope != "model" {
				t.Fatalf("model cooldown evidence lost the explicit scope: %+v", evidence)
			}
			if item.LastError != "" {
				t.Fatalf("model-scoped failure leaked into account error UI: %q", item.LastError)
			}
		})
	}
}

func TestAccountScopeOverridesAccidentalModelForAuth(t *testing.T) {
	pool := NewPool(nil, nil)
	pool.Upsert(Item{ID: "wb-a", Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
	pool.MarkClassified("wb-a", Classified{
		Kind: KindAuth, Scope: "account", Model: "minimax-m3", Cooldown: time.Minute,
		Message: "synthetic account authentication failure",
	})

	item, _ := pool.ByID("wb-a")
	if item.DownUntil.IsZero() {
		t.Fatal("explicit account auth scope was narrowed to a model")
	}
	if len(item.ModelDownUntil) != 0 {
		t.Fatalf("account auth failure created model cooldowns: %+v", item.ModelDownUntil)
	}
	if item.LastError == "" || item.LastKind != KindAuth {
		t.Fatalf("account auth failure should remain visible at account scope: %+v", item)
	}
}

func TestWorkBuddyChatUnavailableIsLocallyScopedToRequestedModel(t *testing.T) {
	pool := NewPool(nil, nil)
	pool.Upsert(Item{
		ID: "wb-a", Provider: "workbuddy", Region: "cn", Runtime: "in_process",
		Models: []string{"minimax-m3", "deepseek-v4-flash"},
	})
	chat := &scopeIsolationChat{}
	registry := providers.NewRegistry()
	registry.Register(providers.Adapter{ID: "workbuddy", Chat: chat})
	executor := NewChatExecutor(pool, "")
	executor.Providers = registry
	executor.MaxAttempts = 1

	_, err := executor.ChatNonStream(context.Background(), translate.ChatRequest{Model: "minimax-m3"}, "wb-a", "workbuddy")
	if err == nil {
		t.Fatal("synthetic MiniMax chat failure was not returned")
	}
	item, _ := pool.ByID("wb-a")
	if !item.DownUntil.IsZero() || item.ModelDownUntil["minimax-m3"].IsZero() {
		t.Fatalf("chat failure must cool only the selected model: account=%v models=%+v", item.DownUntil, item.ModelDownUntil)
	}
	evidence := item.CooldownEvidence["minimax-m3"]
	if evidence.Scope != "model" || evidence.ReportedScope != "" || evidence.Code != "synthetic-503" || evidence.Source != "provider_adapter_error" {
		t.Fatalf("local scope inference must not claim provider-reported scope: %+v", evidence)
	}
	if !item.CooldownEvidence[""].LastObservedAt.IsZero() || item.LastError != "" || item.LastKind != "" {
		t.Fatalf("model failure was also recorded as an account failure: %+v", item)
	}

	result, err := executor.ChatNonStream(context.Background(), translate.ChatRequest{Model: "deepseek-v4-flash"}, "wb-a", "workbuddy")
	if err != nil || result.AccountID != "wb-a" || result.Content != "synthetic success" {
		t.Fatalf("another model should still succeed on the same account: result=%+v err=%v", result, err)
	}
	item, _ = pool.ByID("wb-a")
	if item.ModelDownUntil["minimax-m3"].IsZero() {
		t.Fatal("success on another model cleared the MiniMax cooldown")
	}
	executor.markClassified("wb-a", Classified{
		Kind: KindUnavailable, Status: http.StatusServiceUnavailable, Cooldown: time.Minute,
		Code: "synthetic-repeat", ErrorPhase: "chat", ErrorSource: "provider_adapter_error",
	}, "minimax-m3")
	executor.markClassified("wb-a", Classified{
		Kind: KindUnavailable, Status: http.StatusServiceUnavailable, Cooldown: time.Minute,
		Code: "synthetic-repeat", ErrorPhase: "chat", ErrorSource: "provider_adapter_error",
	}, "minimax-m3")
	item, _ = pool.ByID("wb-a")
	if item.ModelBackoff["minimax-m3"] == 0 || item.ModelBackoff["deepseek-v4-flash"] != 0 || !item.DownUntil.IsZero() {
		t.Fatalf("repeated MiniMax failures must back off only MiniMax: %+v", item)
	}

	pool.Upsert(Item{
		ID: "wb-b", Provider: "workbuddy", Region: "cn", Runtime: "in_process",
		Models: []string{"minimax-m3"},
	})
	result, err = executor.ChatNonStream(context.Background(), translate.ChatRequest{Model: "minimax-m3"}, "", "workbuddy")
	if err != nil || result.AccountID != "wb-b" || result.Content != "synthetic success" {
		t.Fatalf("a healthy account should remain available for MiniMax: result=%+v err=%v", result, err)
	}
}

func TestWorkBuddyLocalInferenceDoesNotApplyToOtherProvidersOrRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		kind     string
		cooldown time.Duration
	}{
		{name: "other provider unavailable", provider: "trae", kind: KindUnavailable, cooldown: time.Minute},
		{name: "WorkBuddy request rejection", provider: "workbuddy", kind: KindInvalidRequest},
		{name: "WorkBuddy cancellation", provider: "workbuddy", kind: KindCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewPool(nil, nil)
			pool.Upsert(Item{ID: "a", Provider: tc.provider, Region: "cn", Runtime: "in_process"})
			executor := NewChatExecutor(pool, "")
			executor.markClassified("a", Classified{
				Kind: tc.kind, Status: 503, Cooldown: tc.cooldown, ErrorPhase: "chat",
				ErrorSource: "provider_adapter_error", Code: "synthetic",
			}, "minimax-m3")

			item, _ := pool.ByID("a")
			if tc.provider == "trae" {
				if item.DownUntil.IsZero() || len(item.ModelDownUntil) != 0 {
					t.Fatalf("non-WorkBuddy classification changed scope: %+v", item)
				}
				return
			}
			if !item.DownUntil.IsZero() || len(item.ModelDownUntil) != 0 {
				t.Fatalf("request rejection/cancellation must not cool the WorkBuddy account or model: %+v", item)
			}
		})
	}
}

type scopeIsolationChat struct{}

func (*scopeIsolationChat) ChatNonStream(_ context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	if req.Model == "minimax-m3" && accountID == "wb-a" {
		return providers.ChatOutcome{}, &providers.Error{
			Kind: KindUnavailable, Status: http.StatusServiceUnavailable, Code: "synthetic-503",
			Message: "synthetic chat service unavailable", Phase: "chat", Source: "synthetic-workbuddy-chat",
			Cause: errors.New("synthetic chat failure"),
		}
	}
	return providers.ChatOutcome{Model: req.Model, Content: "synthetic success", FinishReason: "stop"}, nil
}

func (*scopeIsolationChat) ChatStream(context.Context, string, translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return nil, providers.ResolvedChat{}, nil
}

var _ providers.ProviderChat = (*scopeIsolationChat)(nil)

func TestExplicitModelScopeDoesNotAlterCooldownEvidenceProviderScope(t *testing.T) {
	pool := NewPool(nil, nil)
	pool.Upsert(Item{ID: "wb-a", Provider: "workbuddy", Region: "cn", Runtime: "in_process"})
	classified := Classified{
		Kind: KindUnavailable, Scope: "model", Model: "minimax-m3", Cooldown: time.Minute,
		Code: "opaque", ErrorPhase: "chat", ErrorSource: "provider_adapter_error",
	}
	NewChatExecutor(pool, "").markClassified("wb-a", classified, "deepseek-v4-flash")
	item, _ := pool.ByID("wb-a")
	if item.ModelDownUntil["minimax-m3"].IsZero() || !item.DownUntil.IsZero() {
		t.Fatalf("explicit model scope must be preserved over the request fallback model: %+v", item)
	}
}
