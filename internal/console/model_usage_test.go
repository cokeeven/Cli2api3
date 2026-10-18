package console

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
	"github.com/caigee-cmd/cli2api/internal/executor"
	"github.com/caigee-cmd/cli2api/internal/logs"
	"github.com/caigee-cmd/cli2api/internal/store"
)

func TestModelUsageHandlerReadsBoundedLocalDataAndRouteReasons(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-observed", CreatedAt: now, Status: accounts.RequestStatusOK, RequestedModel: "glm-5.3", MappedModel: "glm-5.3", Provider: "workbuddy", AccountID: "wb-a"}); err != nil {
		t.Fatal(err)
	}
	prompt, completion := 10, 5
	if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-observed", RequestID: "req-observed", AccountID: "wb-a", Provider: "workbuddy", Region: "cn", Model: "glm-5.3", StartedAt: now, Status: accounts.AttemptStatusOK, UsageState: "actual", PromptTokens: &prompt, CompletionTokens: &completion}); err != nil {
		t.Fatal(err)
	}
	ready := true
	pool := executor.NewPool(nil, nil)
	pool.Upsert(executor.Item{ID: "wb-a", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	handler := &Handler{Recorder: recorder, Pool: pool}
	from := now.Add(-time.Minute).Format(time.RFC3339)
	to := now.Add(time.Minute).Format(time.RFC3339)
	req := httptest.NewRequest("GET", "/api/model-usage?provider=workbuddy&region=cn&model=glm-5.3&from="+from+"&to="+to, nil)
	response := httptest.NewRecorder()
	handler.HandleModelUsage(response, req)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Rows []accounts.ModelUsageRow `json:"rows"`
		} `json:"data"`
		Routes  []modelUsageRoute                  `json:"routes"`
		Budgets []accounts.WorkBuddyBudgetEstimate `json:"workbuddy_budgets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Rows) != 1 || body.Data.Rows[0].Requests != 1 || body.Data.Rows[0].PromptTokens == nil || *body.Data.Rows[0].PromptTokens != 10 {
		t.Fatalf("data=%+v", body.Data)
	}
	if len(body.Routes) != 1 || len(body.Routes[0].Candidates) != 1 || body.Routes[0].Candidates[0].Reason != "available" || !body.Routes[0].Candidates[0].Schedulable {
		t.Fatalf("routes=%+v", body.Routes)
	}
	if len(body.Budgets) != 0 {
		t.Fatalf("unqualified WorkBuddy history must not estimate: %+v", body.Budgets)
	}

	bad := httptest.NewRecorder()
	handler.HandleModelUsage(bad, httptest.NewRequest("GET", "/api/model-usage?from=not-a-time", nil))
	if bad.Code != 400 {
		t.Fatalf("invalid time status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestModelUsageRoutesIncludeCurrentCatalogWithoutRequestHistory(t *testing.T) {
	ready := true
	pool := executor.NewPool(nil, nil)
	pool.Upsert(executor.Item{ID: "wb-a", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	handler := &Handler{Pool: pool}
	routes := handler.modelUsageRoutes("workbuddy", "cn", "", "", nil)
	if len(routes) != 1 || routes[0].Model != "glm-5.3" || len(routes[0].Candidates) != 1 || routes[0].Candidates[0].ID != "wb-a" {
		t.Fatalf("routes without request history = %+v", routes)
	}
}

func TestModelUsageAPISeparatesConfirmedLimitFromUnknownBudget(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "confirmed-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Hour)
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-confirmed-limit", CreatedAt: now, Status: accounts.RequestStatusError,
		RequestedModel: "glm-5.3", MappedModel: "glm-5.3", Provider: "workbuddy", RequestPath: "openai_chat_completions"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-confirmed-limit", RequestID: "req-confirmed-limit", AccountID: "wb-a",
		Provider: "workbuddy", Region: "cn", ProviderPlan: "pro", Model: "glm-5.3", StartedAt: now, FinishedAt: &now,
		Status: accounts.AttemptStatusFailover, ErrorKind: accounts.KindRateLimit, ErrorCode: "6004", ErrorScope: "model", UpstreamResetAt: &reset, UsageState: "unknown"}); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Recorder: recorder}
	from, to := now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	response := httptest.NewRecorder()
	handler.HandleModelUsage(response, httptest.NewRequest("GET", "/api/model-usage?provider=workbuddy&from="+from+"&to="+to, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Budgets []accounts.WorkBuddyBudgetEstimate `json:"workbuddy_budgets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Budgets) != 1 {
		t.Fatalf("confirmed restriction should be returned even without enough budget windows: %+v", body.Budgets)
	}
	budget := body.Budgets[0]
	if !budget.Restricted || budget.ResetAt == nil || budget.EstimatedTokens != nil || budget.EstimatedRemaining != nil || budget.RequestPath != "openai_chat_completions" {
		t.Fatalf("limit status must not be collapsed into a made-up budget: %+v", budget)
	}
}

func TestManualProbeRequiresSelectedAccountVisibleOutputUpstreamFinishAndUsage(t *testing.T) {
	view := accounts.AccountView{Account: accounts.Account{ID: "wb-selected", Provider: "workbuddy", ProviderRegion: "cn"}}
	input := modelProbeRequest{AccountID: view.ID, Model: "glm-5.3"}
	var sentAccount string
	var sent struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	newHandler := func(source, usageSource string) *Handler {
		return &Handler{Chat: func(w http.ResponseWriter, r *http.Request) {
			sentAccount = r.Header.Get("X-Qoder-Account")
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode actual probe request: %v", err)
			}
			w.Header().Set("X-Request-Id", "req_synthetic_probe")
			w.Header().Set("X-CLI2API-Account", view.ID)
			w.Header().Set("X-CLI2API-Finish-Reason", "stop")
			w.Header().Set("X-CLI2API-Finish-Reason-Source", source)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"probe-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"source":"` + usageSource + `"}}`))
		}}
	}
	parent := httptest.NewRequest(http.MethodPost, "/api/model-usage/probe", strings.NewReader("{}"))
	got := newHandler("upstream", "provider").runModelProbe(parent, input, view)
	if !got.OK || got.RequestID != "req_synthetic_probe" || got.ObservedAccountID != view.ID || got.Output != "probe-ok" || got.RequestedMaxTokens != modelProbeMaxTokens || got.FinishReasonSource != "upstream" || got.PromptTokens == nil || *got.PromptTokens != 5 || got.CompletionTokens == nil || *got.CompletionTokens != 1 {
		t.Fatalf("successful probe = %+v", got)
	}
	if got.BuildFingerprint == "" {
		t.Fatalf("build fingerprint missing: %+v", got)
	}
	if sentAccount != view.ID || sent.Model != input.Model || sent.Stream || sent.MaxTokens != modelProbeMaxTokens || len(sent.Messages) != 1 || sent.Messages[0].Role != "user" || sent.Messages[0].Content != "Reply with exactly the word probe-ok. Do not use tools." {
		t.Fatalf("actual probe request was not pinned and bounded as expected: account=%q request=%+v", sentAccount, sent)
	}

	defaulted := newHandler("gateway_fallback", "provider").runModelProbe(parent, input, view)
	if defaulted.OK || defaulted.ErrorCode != "probe_terminal_not_confirmed" {
		t.Fatalf("fallback finish reason was treated as upstream: %+v", defaulted)
	}
	missing := newHandler("upstream", "estimate").runModelProbe(parent, input, view)
	if missing.OK || missing.ErrorCode != "probe_usage_missing" {
		t.Fatalf("estimated usage was treated as actual: %+v", missing)
	}
}
