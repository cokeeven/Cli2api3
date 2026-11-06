package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type alternativeSettings string

func (s alternativeSettings) GetSecret(context.Context, string) (string, bool, error) {
	return string(s), s != "", nil
}

type alternativeChat struct {
	calls           []string
	partial         bool
	failAlternative bool
}

func (c *alternativeChat) ChatNonStream(ctx context.Context, _ string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	c.calls = append(c.calls, req.Model)
	if req.Model == "m" {
		if c.partial {
			return providers.ChatOutcome{Content: "partial"}, &providers.Error{Kind: accounts.KindQuota, Status: 429, Scope: "model", RetrySafe: true, Code: "6004"}
		}
		return providers.ChatOutcome{}, &providers.Error{Kind: accounts.KindQuota, Status: 429, Scope: "model", RetrySafe: true, Code: "6004"}
	}
	if !providers.FallbackValidationRequested(ctx) {
		return providers.ChatOutcome{}, errors.New("missing validation marker")
	}
	if c.failAlternative {
		return providers.ChatOutcome{}, errors.New("alternative failure")
	}
	return providers.ChatOutcome{Content: "ok"}, nil
}
func (c *alternativeChat) ChatStream(ctx context.Context, id string, req translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	_, err := c.ChatNonStream(ctx, id, req)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, providers.ResolvedChat{}, nil
}
func alternativeExecutor(c *alternativeChat, enabled bool) ChatExecutor {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m", "n"}, FallbackModels: map[string]providers.FallbackCapabilities{"n": {ContextWindow: 8192, MaxOutput: 1024, Tools: true}}})
	e := NewChatExecutor(p, "")
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: c})
	policy := accounts.ModelAlternativePolicy{Enabled: enabled, Rules: []accounts.ModelAlternativeRule{{Provider: "workbuddy", Region: "cn", RequestedModel: "m", AlternativeModel: "n"}}}
	b, _ := json.Marshal(policy)
	e.ModelAlternatives = alternativeSettings(b)
	return e
}
func TestConfiguredAlternativeSingleAttemptAndOriginalFailure(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		enabled, partial, fail bool
		wantCalls              int
		success                bool
	}{
		{"disabled", false, false, false, 1, false}, {"success", true, false, false, 2, true}, {"partial", true, true, false, 1, false}, {"failure", true, false, true, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &alternativeChat{partial: tc.partial, failAlternative: tc.fail}
			e := alternativeExecutor(c, tc.enabled)
			var attempts []accounts.RequestAttempt
			e.OnAttempt = func(a accounts.RequestAttempt) { attempts = append(attempts, a) }
			req := translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("32")}
			result, err := e.ChatNonStream(WithRequestID(context.Background(), "synthetic"), req, "", "workbuddy")
			if len(c.calls) != tc.wantCalls {
				t.Fatalf("calls %v err %v", c.calls, err)
			}
			if tc.success {
				if len(attempts) != 2 || attempts[0].Model != "m" || attempts[1].Model != "n" || attempts[1].RouteReason != "configured_model_fallback" || attempts[1].Region != "cn" {
					t.Fatalf("attempt diagnostics %+v", attempts)
				}
				if err != nil || !result.ModelFallback || result.Model != "n" || result.AttemptCount != 2 {
					t.Fatalf("result %+v err %v", result, err)
				}
			} else if err == nil || (tc.enabled && tc.fail && ClassifyError(err).Code != "6004") {
				t.Fatalf("expected original 6004, got %v", err)
			}
		})
	}
}
func TestConfiguredAlternativeStreamingAndProbeLease(t *testing.T) {
	c := &alternativeChat{}
	e := alternativeExecutor(c, true)
	req := translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("32"), Stream: true}
	result, err := e.ChatStreamProxy(context.Background(), req, "", "workbuddy")
	if err != nil || !result.ModelFallback || result.ActualModel != "n" || len(c.calls) != 2 {
		t.Fatalf("result %+v calls %v err %v", result, c.calls, err)
	}
	io.ReadAll(result.Response.Body)
	result.Finalize(StreamCompletion{Completed: true})
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "x", Provider: "workbuddy", Region: "cn", Runtime: "in_process", MaxInFlight: 4, Models: []string{"m", "n"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second), "n": time.Now().Add(time.Hour)}})
	e = NewChatExecutor(p, "")
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{}})
	result, err = e.ChatStreamProxy(context.Background(), translate.ChatRequest{Model: "m", Stream: true}, "", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(result.Response.Body)
	q := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}
	if _, release, ok := p.acquireRoute(q, routingIdentity{}); ok {
		release()
		t.Fatal("probe released at EOF before finalization")
	}
	result.Finalize(StreamCompletion{Completed: true})
	item, _ := p.ByID("x")
	if _, ok := item.ModelDownUntil["m"]; ok {
		t.Fatal("successful probe did not clear model")
	}
	if !item.ModelDownUntil["n"].After(time.Now()) {
		t.Fatal("other model cleared")
	}
}
func TestExpiredModelProbeCancellationReleasesWithoutSuccess(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Runtime: "in_process", MaxInFlight: 4, Models: []string{"m"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
	e := NewChatExecutor(p, "")
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: admissionChat{}})
	ctx, cancel := context.WithCancel(context.Background())
	result, err := e.ChatStreamProxy(ctx, translate.ChatRequest{Model: "m", Stream: true}, "", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		item, _ := p.ByID("a")
		if item.LiveInFlight == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled probe leaked")
		}
		time.Sleep(time.Millisecond)
	}
	result.Finalize(StreamCompletion{Completed: true})
	item, _ := p.ByID("a")
	if item.ModelDownUntil["m"].IsZero() {
		t.Fatal("canceled probe was marked successful")
	}
	q := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}
	_, release, ok := p.acquireRoute(q, routingIdentity{})
	if !ok {
		t.Fatal("canceled probe gate not released")
	}
	release()
}
func TestConfiguredAlternativeUnknownCapabilityPreservesOriginal(t *testing.T) {
	c := &alternativeChat{}
	e := alternativeExecutor(c, true)
	item, _ := e.Pool.ByID("a")
	item.FallbackModels = map[string]providers.FallbackCapabilities{}
	e.Pool.Upsert(item)
	result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("32")}, "", "workbuddy")
	if len(c.calls) != 1 || err == nil || ClassifyError(err).Code != "6004" || result.ModelFallback {
		t.Fatalf("calls=%v result=%+v err=%v", c.calls, result, err)
	}
}
func TestAlternativeRespectsAttemptLimitAndRegion(t *testing.T) {
	t.Run("attempt limit", func(t *testing.T) {
		c := &alternativeChat{}
		e := alternativeExecutor(c, true)
		e.MaxAttempts = 1
		_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("32")}, "", "workbuddy")
		if err == nil || len(c.calls) != 1 || ClassifyError(err).Code != "6004" {
			t.Fatalf("calls=%v err=%v", c.calls, err)
		}
	})
	t.Run("other region", func(t *testing.T) {
		c := &alternativeChat{}
		e := alternativeExecutor(c, true)
		a, _ := e.Pool.ByID("a")
		a.FallbackModels = map[string]providers.FallbackCapabilities{}
		e.Pool.Upsert(a)
		e.Pool.Upsert(Item{ID: "b", Provider: "workbuddy", Region: "global", Runtime: "in_process", Models: []string{"n"}, FallbackModels: map[string]providers.FallbackCapabilities{"n": {ContextWindow: 8192, MaxOutput: 1024}}})
		_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m", MaxTokens: json.RawMessage("32")}, "", "workbuddy")
		if err == nil || len(c.calls) != 1 || ClassifyError(err).Code != "6004" {
			t.Fatalf("calls=%v err=%v", c.calls, err)
		}
	})
}
func TestSuccessfulModelProbePreservesConcurrentModelEvents(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "other model", true: "same model"}[same], func(t *testing.T) {
			p := NewPool(nil, nil)
			p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"m", "n"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(-time.Second)}})
			item, release, ok := p.acquireRoute(RouteQuery{PublicModel: "m"}, routingIdentity{})
			if !ok || !item.modelProbe {
				t.Fatal("probe not admitted")
			}
			defer release()
			target := "n"
			if same {
				target = "m"
			}
			p.MarkClassified("a", Classified{Kind: KindRateLimit, Scope: "model", Cooldown: time.Minute, Model: target})
			p.MarkProbeOK(item, "m")
			got, _ := p.ByID("a")
			if same && !got.ModelDownUntil["m"].After(time.Now()) {
				t.Fatal("newer same-model restriction cleared")
			}
			if !same && (!got.ModelDownUntil["m"].IsZero() || !got.ModelDownUntil["n"].After(time.Now())) {
				t.Fatalf("wrong scope: %+v", got.ModelDownUntil)
			}
		})
	}
}
