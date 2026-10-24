package console

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestModelUsageOverviewPaginatesAggregatesAndDeduplicatesCrossAccountRetries(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "compact-overview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	now := time.Now().UTC().Truncate(time.Second)
	finished := now
	prompt, completion := 10, 4
	for index := 0; index < 205; index++ {
		model := fmt.Sprintf("model-%03d", index)
		requestID := "req-" + model
		if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: requestID, CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusOK,
			RequestedModel: model, MappedModel: model, Provider: "workbuddy"}); err != nil {
			t.Fatal(err)
		}
		for attemptIndex, accountID := range []string{"wb-a", "wb-b"} {
			status := accounts.AttemptStatusFailover
			usageState := "unknown"
			var promptTokens, completionTokens *int
			if attemptIndex == 1 {
				status, usageState = accounts.AttemptStatusOK, "actual"
				promptTokens, completionTokens = &prompt, &completion
			}
			if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: fmt.Sprintf("attempt-%03d-%d", index, attemptIndex), RequestID: requestID,
				AttemptIndex: attemptIndex, AccountID: accountID, Provider: "workbuddy", Region: "cn", Model: model, StartedAt: now,
				FinishedAt: &finished, Status: status, UsageState: usageState, PromptTokens: promptTokens, CompletionTokens: completionTokens}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-model-error", CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusError,
		RequestedModel: "model-error", MappedModel: "model-error", Provider: "workbuddy"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-model-error", RequestID: "req-model-error", AttemptIndex: 0, AccountID: "wb-a",
		Provider: "workbuddy", Region: "cn", Model: "model-error", StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusError,
		ErrorKind: accounts.KindRateLimit, ErrorCode: "3004", ErrorScope: "model"}); err != nil {
		t.Fatal(err)
	}
	pool := executor.NewPool(nil, nil)
	ready := true
	pool.Upsert(executor.Item{ID: "wb-catalog", Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"catalog-unused"}})
	handler := &Handler{Recorder: recorder, Pool: pool}
	from, to := now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	request := func(page int) map[string]any {
		response := httptest.NewRecorder()
		path := fmt.Sprintf("/api/model-usage/overview?from=%s&to=%s&page=%d&page_size=20", from, to, page)
		handler.HandleModelUsage(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("overview page %d status=%d body=%s", page, response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	first, second := request(1), request(2)
	firstData, _ := first["data"].(map[string]any)
	secondData, _ := second["data"].(map[string]any)
	firstRows, _ := firstData["rows"].([]any)
	secondRows, _ := secondData["rows"].([]any)
	if len(firstRows) != 20 || len(secondRows) != 20 {
		t.Fatalf("server-side page sizes = %d and %d; expected 20 on each page (legacy response exposes %d account/model rows)", len(firstRows), len(secondRows), len(firstData["rows"].([]any)))
	}
	if firstData["total"] != float64(207) || secondData["total"] != float64(207) {
		t.Fatalf("catalog model must remain visible and total must not be page-local: first=%+v second=%+v", firstData, secondData)
	}
	firstSummary, _ := firstData["summary"].(map[string]any)
	secondSummary, _ := secondData["summary"].(map[string]any)
	if firstSummary["requests"] != float64(206) || firstSummary["attempts"] != float64(411) || secondSummary["requests"] != float64(206) || secondSummary["attempts"] != float64(411) {
		t.Fatalf("request dedupe, attempt totals, or page invariant failed: first=%+v second=%+v", firstSummary, secondSummary)
	}
	for _, candidate := range append(firstRows, secondRows...) {
		row, _ := candidate.(map[string]any)
		if row["model"] == "model-000" {
			if row["requests"] != float64(1) || row["attempts"] != float64(2) || row["account_count"] != float64(2) || row["recent_usage"] != true {
				t.Fatalf("cross-account retry was not represented as one request and two attempts: %+v", row)
			}
		}
		if row["model"] == "catalog-unused" && (row["recent_usage"] != false || row["requests"] != float64(0)) {
			t.Fatalf("catalog model without recent usage should be explicit: %+v", row)
		}
	}
	unusedResponse := httptest.NewRecorder()
	unusedPath := fmt.Sprintf("/api/model-usage/overview?from=%s&to=%s&activity=unused&page=1&page_size=20", from, to)
	handler.HandleModelUsage(unusedResponse, httptest.NewRequest(http.MethodGet, unusedPath, nil))
	var unusedBody struct {
		Data struct {
			Rows  []accounts.ModelUsageOverviewRow `json:"rows"`
			Total int                              `json:"total"`
		} `json:"data"`
	}
	if unusedResponse.Code != http.StatusOK || json.Unmarshal(unusedResponse.Body.Bytes(), &unusedBody) != nil || unusedBody.Data.Total != 1 || len(unusedBody.Data.Rows) != 1 || unusedBody.Data.Rows[0].Model != "catalog-unused" || unusedBody.Data.Rows[0].RecentUsage {
		t.Fatalf("unused catalog model filter = status %d, body=%s", unusedResponse.Code, unusedResponse.Body.String())
	}
	filteredRequest := func(query string) accounts.ModelUsageOverviewData {
		response := httptest.NewRecorder()
		path := fmt.Sprintf("/api/model-usage/overview?from=%s&to=%s&page=1&page_size=20%s", from, to, query)
		handler.HandleModelUsage(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("filtered overview status=%d body=%s", response.Code, response.Body.String())
		}
		var body struct {
			Data accounts.ModelUsageOverviewData `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.Rows == nil {
			t.Fatalf("filtered overview rows must be an empty or populated array, not null: %s", response.Body.String())
		}
		return body.Data
	}
	searched := filteredRequest("&search=model-204")
	if searched.Total != 1 || len(searched.Rows) != 1 || searched.Rows[0].Model != "model-204" {
		t.Fatalf("search filter = %+v", searched)
	}
	errorsOnly := filteredRequest("&activity=errors")
	if errorsOnly.Total != 1 || len(errorsOnly.Rows) != 1 || errorsOnly.Rows[0].Model != "model-error" {
		t.Fatalf("error activity filter = %+v", errorsOnly)
	}
	wrongProvider := filteredRequest("&provider=trae&region=cn")
	if wrongProvider.Total != 0 || len(wrongProvider.Rows) != 0 {
		t.Fatalf("provider/region filter should produce a typed empty result: %+v", wrongProvider)
	}
	outsideWindow := fmt.Sprintf("/api/model-usage/overview?from=%s&to=%s&page=1&page_size=20", now.Add(-2*time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339))
	timeResponse := httptest.NewRecorder()
	handler.HandleModelUsage(timeResponse, httptest.NewRequest(http.MethodGet, outsideWindow, nil))
	var timeBody struct {
		Data accounts.ModelUsageOverviewData `json:"data"`
	}
	if timeResponse.Code != http.StatusOK || json.Unmarshal(timeResponse.Body.Bytes(), &timeBody) != nil || timeBody.Data.Total != 1 || len(timeBody.Data.Rows) != 1 || timeBody.Data.Rows[0].Model != "catalog-unused" || timeBody.Data.Rows[0].RecentUsage {
		t.Fatalf("time filter status=%d body=%s", timeResponse.Code, timeResponse.Body.String())
	}
}

func TestModelUsageDetailPagesCatalogAccountsAndKeepsBudgetObservationsPrivate(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "compact-detail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	account, err := db.Create(ctx, accounts.CreateAccount{Name: "cache-account", Provider: "workbuddy", Region: "cn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	finished := now
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-detail", CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", MappedModel: "glm-5.3", AccountID: account.ID, Provider: "workbuddy"}); err != nil {
		t.Fatal(err)
	}
	prompt, completion := 7, 3
	if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-detail", RequestID: "req-detail", AccountID: account.ID, Provider: "workbuddy", Region: "cn",
		Model: "glm-5.3", StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusOK, UsageState: "actual", PromptTokens: &prompt, CompletionTokens: &completion}); err != nil {
		t.Fatal(err)
	}
	cacheRead, cacheWrite, unknownRead, unknownWrite := 0, 5, 999, 1000
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-cache-hit", CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", MappedModel: "glm-5.3", AccountID: account.ID, Provider: "workbuddy", UsageSource: "upstream",
		CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite}); err != nil {
		t.Fatal(err)
	}
	for attemptIndex, status := range []string{accounts.AttemptStatusFailover, accounts.AttemptStatusOK} {
		if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: fmt.Sprintf("attempt-cache-hit-%d", attemptIndex), RequestID: "req-cache-hit", AttemptIndex: attemptIndex,
			AccountID: account.ID, Provider: "workbuddy", Region: "cn", Model: "glm-5.3", StartedAt: now, FinishedAt: &finished, Status: status,
			UsageState: "unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: "req-cache-unknown", CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusOK,
		RequestedModel: "glm-5.3", MappedModel: "glm-5.3", AccountID: account.ID, Provider: "workbuddy", UsageSource: "unknown",
		CacheReadTokens: &unknownRead, CacheWriteTokens: &unknownWrite}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: "attempt-cache-unknown", RequestID: "req-cache-unknown", AccountID: account.ID, Provider: "workbuddy", Region: "cn",
		Model: "glm-5.3", StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusOK, UsageState: "unknown"}); err != nil {
		t.Fatal(err)
	}
	pool := executor.NewPool(nil, nil)
	ready := true
	for index := 0; index < 23; index++ {
		accountID := fmt.Sprintf("wb-%02d", index)
		if index == 0 {
			accountID = account.ID
		}
		pool.Upsert(executor.Item{ID: accountID, Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
	}
	handler := &Handler{Recorder: recorder, Pool: pool}
	from, to := now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	request := func(page, pageSize int) (map[string]any, *httptest.ResponseRecorder) {
		response := httptest.NewRecorder()
		path := fmt.Sprintf("/api/model-usage/detail?provider=workbuddy&region=cn&model=glm-5.3&from=%s&to=%s&page=%d&page_size=%d", from, to, page, pageSize)
		handler.HandleModelUsage(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("detail page %d status=%d body=%s", page, response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body, response
	}
	first, response := request(1, 10)
	twenty, _ := request(1, 20)
	last, _ := request(3, 10)
	data, _ := first["data"].(map[string]any)
	twentyData, _ := twenty["data"].(map[string]any)
	rows, _ := data["rows"].([]any)
	twentyRows, _ := twentyData["rows"].([]any)
	lastRows, _ := last["data"].(map[string]any)["rows"].([]any)
	if len(rows) != 10 || len(twentyRows) != 20 || len(lastRows) != 3 || data["total"] != float64(23) || data["has_more"] != true {
		t.Fatalf("account paging = first %d / requested20 %d / last %d, data=%+v", len(rows), len(twentyRows), len(lastRows), data)
	}
	if strings.Contains(response.Body.String(), `"budget_observations"`) {
		t.Fatalf("raw model budget observations must stay server-side")
	}
	for _, value := range rows {
		row, _ := value.(map[string]any)
		if _, exists := row["account"]; !exists {
			t.Fatalf("account view missing: %+v", row)
		}
		budgets, ok := row["budgets"].([]any)
		if !ok || len(budgets) == 0 {
			t.Fatalf("catalog account must receive an explicit unknown estimate: %+v", row)
		}
		for _, budgetValue := range budgets {
			budget, _ := budgetValue.(map[string]any)
			if budget["estimated_tokens"] != nil || budget["estimated_remaining"] != nil {
				t.Fatalf("catalog-only estimate must not invent a limit: %+v", budget)
			}
		}
	}
	cacheRows, _ := twentyData["rows"].([]any)
	foundCacheAccount := false
	for _, value := range cacheRows {
		row, _ := value.(map[string]any)
		accountView, _ := row["account"].(map[string]any)
		if accountView["id"] != account.ID {
			continue
		}
		usage, _ := row["usage"].(map[string]any)
		if usage["cache_read_tokens"] != float64(0) || usage["cache_write_tokens"] != float64(5) || usage["cache_known_requests"] != float64(1) || usage["cache_missing_requests"] != float64(2) {
			t.Fatalf("compact cache metrics must keep explicit zero, source filtering, and logical-request deduplication: %+v", usage)
		}
		foundCacheAccount = true
	}
	if !foundCacheAccount {
		t.Fatalf("page cache aggregation omitted the synthetic account %s", account.ID)
	}
}

func TestModelUsageDiagnosticsPagesErrorsAndRoutesAndFiltersAccount(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "compact-diagnostics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	now := time.Now().UTC().Truncate(time.Second)
	finished := now
	pool := executor.NewPool(nil, nil)
	ready := true
	for index := 0; index < 21; index++ {
		accountID := fmt.Sprintf("wb-%02d", index)
		pool.Upsert(executor.Item{ID: accountID, Provider: "workbuddy", Region: "cn", Ready: &ready, Models: []string{"glm-5.3"}})
		requestID := fmt.Sprintf("req-error-%02d", index)
		if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: requestID, CreatedAt: now, FinishedAt: &finished, Status: accounts.RequestStatusError,
			RequestedModel: "glm-5.3", MappedModel: "glm-5.3", Provider: "workbuddy"}); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertRequestAttempt(ctx, accounts.RequestAttempt{ID: fmt.Sprintf("attempt-error-%02d", index), RequestID: requestID, AttemptIndex: 0, AccountID: accountID,
			Provider: "workbuddy", Region: "cn", Model: "glm-5.3", StartedAt: now, FinishedAt: &finished, Status: accounts.AttemptStatusError,
			ErrorKind: accounts.KindRateLimit, ErrorCode: "3004", ErrorScope: "model"}); err != nil {
			t.Fatal(err)
		}
	}
	handler := &Handler{Recorder: recorder, Pool: pool}
	from, to := now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	request := func(query string) (map[string]any, *httptest.ResponseRecorder) {
		response := httptest.NewRecorder()
		handler.HandleModelUsage(response, httptest.NewRequest(http.MethodGet, "/api/model-usage/diagnostics?provider=workbuddy&region=cn&model=glm-5.3&from="+from+"&to="+to+query, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("diagnostics status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body, response
	}
	first, response := request("&page=1&page_size=10")
	second, _ := request("&page=2&page_size=10")
	data, _ := first["data"].(map[string]any)
	errorsPage, _ := data["errors"].([]any)
	routes, _ := first["routes"].([]any)
	secondData, _ := second["data"].(map[string]any)
	secondErrors, _ := secondData["errors"].([]any)
	if len(errorsPage) != 10 || len(secondErrors) != 10 || data["total"] != float64(21) || len(routes) != 10 || first["routes_total"] != float64(21) {
		t.Fatalf("diagnostic page/route limits = errors %d/%d routes %d; response=%s", len(errorsPage), len(secondErrors), len(routes), response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"budget_observations"`) || strings.Contains(response.Body.String(), `"workbuddy_budgets"`) {
		t.Fatalf("diagnostics must not build or return budget detail payloads")
	}
	filtered, _ := request("&account=wb-00&page=1&page_size=10")
	filteredData, _ := filtered["data"].(map[string]any)
	filteredErrors, _ := filteredData["errors"].([]any)
	if filteredData["total"] != float64(1) || len(filteredErrors) != 1 || filtered["routes_total"] != float64(1) {
		t.Fatalf("account-filtered diagnostics = %+v", filtered)
	}
}

func TestModelUsageErrorActivityIncludesRecoveredFailuresAndLengthButNotCancellation(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "compact-error-activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := logs.NewRequestRecorder(db)
	defer recorder.Close()
	now := time.Now().UTC().Truncate(time.Second)
	finished := now
	fixtures := []struct {
		model        string
		requestState string
		attempts     []accounts.RequestAttempt
		finishReason string
	}{
		{model: "recovered-limit", requestState: accounts.RequestStatusOK, attempts: []accounts.RequestAttempt{
			{AttemptIndex: 0, AccountID: "account-a", Status: accounts.AttemptStatusFailover, ErrorKind: accounts.KindRateLimit, ErrorCode: "6004", ErrorScope: "model"},
			{AttemptIndex: 1, AccountID: "account-b", Status: accounts.AttemptStatusOK},
		}},
		{model: "length-only", requestState: accounts.RequestStatusOK, finishReason: "length", attempts: []accounts.RequestAttempt{
			{AttemptIndex: 0, AccountID: "account-c", Status: accounts.AttemptStatusOK},
		}},
		{model: "clean", requestState: accounts.RequestStatusOK, attempts: []accounts.RequestAttempt{
			{AttemptIndex: 0, AccountID: "account-d", Status: accounts.AttemptStatusOK},
		}},
		{model: "cancel-only", requestState: accounts.RequestStatusCanceled, attempts: []accounts.RequestAttempt{
			{AttemptIndex: 0, AccountID: "account-e", Status: accounts.AttemptStatusCanceled},
		}},
	}
	for _, fixture := range fixtures {
		requestID := "req-activity-" + fixture.model
		if err := db.InsertRequestLog(ctx, accounts.RequestLog{ID: requestID, CreatedAt: now, FinishedAt: &finished,
			Status: fixture.requestState, RequestedModel: fixture.model, MappedModel: fixture.model, Provider: "workbuddy",
			FinishReason: fixture.finishReason, FinishReasonSource: "upstream"}); err != nil {
			t.Fatal(err)
		}
		for _, attempt := range fixture.attempts {
			attempt.ID = requestID + fmt.Sprintf("-%d", attempt.AttemptIndex)
			attempt.RequestID = requestID
			attempt.Provider = "workbuddy"
			attempt.Region = "cn"
			attempt.Model = fixture.model
			attempt.StartedAt = now
			attempt.FinishedAt = &finished
			if err := db.InsertRequestAttempt(ctx, attempt); err != nil {
				t.Fatal(err)
			}
		}
	}
	handler := &Handler{Recorder: recorder, Pool: executor.NewPool(nil, nil)}
	from, to := now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	response := httptest.NewRecorder()
	path := fmt.Sprintf("/api/model-usage/overview?provider=workbuddy&activity=errors&from=%s&to=%s&page=1&page_size=20", from, to)
	handler.HandleModelUsage(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("error activity status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data accounts.ModelUsageOverviewData `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(body.Data.Rows))
	for _, row := range body.Data.Rows {
		got[row.Model] = true
	}
	if len(got) != 2 || !got["recovered-limit"] || !got["length-only"] || got["clean"] || got["cancel-only"] {
		t.Fatalf("error activity must include recovered error evidence and upstream length, while excluding clean/cancel-only requests: got=%v body=%s", got, response.Body.String())
	}
}
