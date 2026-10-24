package console

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
)

const (
	compactModelUsageCatalogLimit = 5000
	compactModelUsagePageMax      = 10000
	compactModelUsagePageSizeMax  = 100
)

type modelUsageDetailItem struct {
	Usage   accounts.ModelUsageAccountRow      `json:"usage"`
	Account modelUsageAccount                  `json:"account"`
	Route   *executor.RouteCandidate           `json:"route,omitempty"`
	Budgets []accounts.WorkBuddyBudgetEstimate `json:"budgets"`
}

func (h *Handler) compactModelUsageStore(w http.ResponseWriter) (accounts.CompactModelUsageStore, bool) {
	if h == nil || h.Recorder == nil || h.Recorder.Store() == nil {
		writeErr(w, http.StatusServiceUnavailable, "model_usage_unavailable", "模型用量数据源不可用")
		return nil, false
	}
	store, ok := h.Recorder.Store().(accounts.CompactModelUsageStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "model_usage_unavailable", "模型用量分页数据源不可用")
		return nil, false
	}
	return store, true
}

func parseCompactPage(queryValue string, fallback int, maximum int) (int, bool) {
	if strings.TrimSpace(queryValue) == "" {
		return fallback, true
	}
	value, err := parseBoundedInt(queryValue, 1, maximum)
	return value, err == nil
}

func (h *Handler) HandleModelUsageOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	store, ok := h.compactModelUsageStore(w)
	if !ok {
		return
	}
	from, to, ok := parseCompactModelUsageWindowForHandler(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	if provider != "" && provider != "workbuddy" && provider != "trae" {
		writeErr(w, http.StatusBadRequest, "invalid_provider", "只支持 WorkBuddy 或 Trae")
		return
	}
	region := strings.ToLower(strings.TrimSpace(query.Get("region")))
	if region != "" && region != "cn" && region != "global" && region != "unknown" {
		writeErr(w, http.StatusBadRequest, "invalid_region", "地区筛选值无效")
		return
	}
	search := strings.TrimSpace(query.Get("search"))
	if len(search) > 120 {
		writeErr(w, http.StatusBadRequest, "invalid_search", "搜索内容不能超过120个字符")
		return
	}
	activity := strings.ToLower(strings.TrimSpace(query.Get("activity")))
	if activity == "" {
		activity = "all"
	}
	if activity != "all" && activity != "used" && activity != "errors" && activity != "unused" {
		writeErr(w, http.StatusBadRequest, "invalid_activity", "活动筛选值无效")
		return
	}
	page, valid := parseCompactPage(query.Get("page"), 1, compactModelUsagePageMax)
	if !valid {
		writeErr(w, http.StatusBadRequest, "invalid_page", "页码格式无效")
		return
	}
	pageSize := 20
	if raw := strings.TrimSpace(query.Get("page_size")); raw != "" {
		value, err := parseBoundedInt(raw, 1, compactModelUsagePageSizeMax)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_page_size", "每页模型数必须在1到100之间")
			return
		}
		pageSize = value
	}
	catalog, catalogLimited := h.modelUsageCatalog(provider, region)
	data, err := store.ReadModelUsageOverview(r.Context(), accounts.ModelUsageOverviewQuery{
		Provider: provider, Region: region, Search: search, Activity: activity, From: from, To: to,
		Page: page, PageSize: pageSize, CatalogModels: catalog, CatalogLimited: catalogLimited,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "model_usage_failed", "读取模型用量总览失败")
		return
	}
	for index := range data.Rows {
		row := &data.Rows[index]
		if h.Pool == nil {
			continue
		}
		candidates := h.Pool.InspectRoute(executor.RouteQuery{PublicModel: row.Model, ProviderFilter: row.Provider, RegionFilter: row.Region})
		for _, candidate := range candidates {
			if candidate.Schedulable {
				row.SchedulableAccounts++
			} else {
				row.RestrictedAccounts++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "source": "gateway_local_observation"})
}

func (h *Handler) HandleModelUsageDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	store, ok := h.compactModelUsageStore(w)
	if !ok {
		return
	}
	from, to, ok := parseCompactModelUsageWindowForHandler(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	region := strings.ToLower(strings.TrimSpace(query.Get("region")))
	model := strings.TrimSpace(query.Get("model"))
	if (provider != "workbuddy" && provider != "trae") || model == "" {
		writeErr(w, http.StatusBadRequest, "invalid_model_selection", "详情需要有效的平台与模型")
		return
	}
	if region != "cn" && region != "global" && region != "unknown" {
		writeErr(w, http.StatusBadRequest, "invalid_region", "地区筛选值无效")
		return
	}
	page, valid := parseCompactPage(query.Get("page"), 1, compactModelUsagePageMax)
	if !valid {
		writeErr(w, http.StatusBadRequest, "invalid_page", "页码格式无效")
		return
	}
	pageSize := 10
	if raw := strings.TrimSpace(query.Get("page_size")); raw != "" {
		value, err := parseBoundedInt(raw, 10, 20)
		if err != nil || (value != 10 && value != 20) {
			writeErr(w, http.StatusBadRequest, "invalid_page_size", "每页账号数只能是10或20")
			return
		}
		pageSize = value
	}
	sortMode := strings.ToLower(strings.TrimSpace(query.Get("sort")))
	if sortMode != "" && sortMode != "account" && sortMode != "errors" {
		writeErr(w, http.StatusBadRequest, "invalid_sort", "排序值无效")
		return
	}
	catalogAccounts, catalogLimited := h.modelUsageCatalogAccounts(provider, region, model)
	data, err := store.ReadModelUsageDetail(r.Context(), accounts.ModelUsageDetailQuery{
		Provider: provider, Region: region, Model: model, From: from, To: to, Page: page, PageSize: pageSize,
		Sort: sortMode, CatalogAccounts: catalogAccounts, CatalogLimited: catalogLimited,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "model_usage_failed", "读取模型账号明细失败")
		return
	}
	data.BudgetObservations = nonNilBudgetObservations(data.BudgetObservations)
	budgets := []accounts.WorkBuddyBudgetEstimate{}
	if provider == "workbuddy" {
		targets := make([]accounts.WorkBuddyBudgetTarget, 0, len(data.Rows))
		selectedAccounts := make(map[string]struct{}, len(data.Rows))
		for _, row := range data.Rows {
			targets = append(targets, accounts.WorkBuddyBudgetTarget{AccountID: row.AccountID, Region: region, Model: model})
			selectedAccounts[row.AccountID] = struct{}{}
		}
		allBudgets := accounts.EstimateWorkBuddyBudgetsForTargetsInRange(data.BudgetObservations, targets, data.To, data.BudgetObservationFrom, data.BudgetObservationsTruncated)
		for _, budget := range allBudgets {
			if _, selected := selectedAccounts[budget.AccountID]; selected {
				budgets = append(budgets, budget)
			}
		}
	}
	budgetByAccount := make(map[string][]accounts.WorkBuddyBudgetEstimate)
	for _, budget := range budgets {
		budgetByAccount[budget.AccountID] = append(budgetByAccount[budget.AccountID], budget)
	}
	var routeCandidates []executor.RouteCandidate
	if h.Pool != nil {
		routeCandidates = h.Pool.InspectRoute(executor.RouteQuery{PublicModel: model, ProviderFilter: provider, RegionFilter: region})
	}
	byAccount := make(map[string]executor.RouteCandidate, len(routeCandidates))
	for _, candidate := range routeCandidates {
		byAccount[candidate.ID] = candidate
	}
	rows := make([]modelUsageDetailItem, 0, len(data.Rows))
	for _, usage := range data.Rows {
		account := h.modelUsageAccountByID(r, usage.AccountID)
		item := modelUsageDetailItem{Usage: usage, Account: account, Budgets: budgetByAccount[usage.AccountID]}
		if candidate, exists := byAccount[usage.AccountID]; exists {
			copy := candidate
			item.Route = &copy
		}
		if item.Budgets == nil {
			item.Budgets = []accounts.WorkBuddyBudgetEstimate{}
		}
		rows = append(rows, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"rows": rows, "total": data.Total, "page": data.Page, "page_size": data.PageSize, "has_more": data.HasMore,
			"from": data.From, "to": data.To, "budget_observation_from": data.BudgetObservationFrom,
			"budget_observations_truncated": data.BudgetObservationsTruncated, "catalog_limited": data.CatalogLimited,
		},
		"source": "gateway_local_observation",
	})
}

func (h *Handler) HandleModelUsageDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	store, ok := h.compactModelUsageStore(w)
	if !ok {
		return
	}
	from, to, ok := parseCompactModelUsageWindowForHandler(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	if provider != "" && provider != "workbuddy" && provider != "trae" {
		writeErr(w, http.StatusBadRequest, "invalid_provider", "只支持 WorkBuddy 或 Trae")
		return
	}
	region := strings.ToLower(strings.TrimSpace(query.Get("region")))
	if region != "" && region != "cn" && region != "global" && region != "unknown" {
		writeErr(w, http.StatusBadRequest, "invalid_region", "地区筛选值无效")
		return
	}
	model := strings.TrimSpace(query.Get("model"))
	account := strings.TrimSpace(query.Get("account"))
	page, valid := parseCompactPage(query.Get("page"), 1, compactModelUsagePageMax)
	if !valid {
		writeErr(w, http.StatusBadRequest, "invalid_page", "页码格式无效")
		return
	}
	pageSize := 20
	if raw := strings.TrimSpace(query.Get("page_size")); raw != "" {
		value, err := parseBoundedInt(raw, 1, 50)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_page_size", "每页诊断数必须在1到50之间")
			return
		}
		pageSize = value
	}
	data, err := store.ReadModelUsageDiagnostics(r.Context(), accounts.ModelUsageDiagnosticsQuery{
		Provider: provider, Region: region, Model: model, Account: account, From: from, To: to, Page: page, PageSize: pageSize,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "model_usage_failed", "读取模型诊断失败")
		return
	}
	candidates := []executor.RouteCandidate{}
	if h.Pool != nil && model != "" {
		candidates = h.Pool.InspectRoute(executor.RouteQuery{PublicModel: model, ProviderFilter: provider, RegionFilter: region})
	}
	if account != "" {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidate.ID == account {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	routeTotal := len(candidates)
	routeStart := (page - 1) * pageSize
	if routeStart > routeTotal {
		routeStart = routeTotal
	}
	routeEnd := min(routeStart+pageSize, routeTotal)
	routes := append([]executor.RouteCandidate{}, candidates[routeStart:routeEnd]...)
	if routes == nil {
		routes = []executor.RouteCandidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":             data,
		"routes":           routes,
		"routes_total":     routeTotal,
		"routes_page":      page,
		"routes_page_size": pageSize,
		"routes_has_more":  page*pageSize < routeTotal,
		"source":           "gateway_local_observation",
	})
}

func (h *Handler) modelUsageCatalog(provider, region string) ([]accounts.ModelUsageCatalogModel, bool) {
	if h == nil || h.Pool == nil {
		return []accounts.ModelUsageCatalogModel{}, false
	}
	seen := make(map[string]accounts.ModelUsageCatalogModel)
	for _, item := range h.Pool.Items() {
		itemProvider := executor.NormalizeProviderFamily(item.Provider)
		itemRegion := strings.ToLower(strings.TrimSpace(item.Region))
		if (itemProvider != "workbuddy" && itemProvider != "trae") || (provider != "" && provider != itemProvider) || (region != "" && region != itemRegion) {
			continue
		}
		for _, rawModel := range item.Models {
			model := strings.TrimSpace(rawModel)
			if model == "" {
				continue
			}
			entry := accounts.ModelUsageCatalogModel{Provider: itemProvider, Region: itemRegion, Model: model}
			seen[entry.Provider+"\x00"+entry.Region+"\x00"+entry.Model] = entry
			if len(seen) > compactModelUsageCatalogLimit {
				break
			}
		}
		if len(seen) > compactModelUsageCatalogLimit {
			break
		}
	}
	out := make([]accounts.ModelUsageCatalogModel, 0, min(len(seen), compactModelUsageCatalogLimit))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].Model < out[j].Model
	})
	limited := len(out) > compactModelUsageCatalogLimit
	if limited {
		out = out[:compactModelUsageCatalogLimit]
	}
	return out, limited
}

func (h *Handler) modelUsageCatalogAccounts(provider, region, model string) ([]string, bool) {
	if h == nil || h.Pool == nil {
		return []string{}, false
	}
	seen := make(map[string]struct{})
	for _, item := range h.Pool.Items() {
		if executor.NormalizeProviderFamily(item.Provider) != provider || !strings.EqualFold(strings.TrimSpace(item.Region), region) {
			continue
		}
		for _, itemModel := range item.Models {
			if strings.EqualFold(strings.TrimSpace(itemModel), model) {
				seen[item.ID] = struct{}{}
				break
			}
		}
		if len(seen) > compactModelUsageCatalogLimit {
			break
		}
	}
	out := make([]string, 0, min(len(seen), compactModelUsageCatalogLimit))
	for id := range seen {
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	limited := len(out) > compactModelUsageCatalogLimit
	if limited {
		out = out[:compactModelUsageCatalogLimit]
	}
	return out, limited
}

func (h *Handler) modelUsageAccountByID(r *http.Request, id string) modelUsageAccount {
	account := modelUsageAccount{ID: id, Name: id, Status: "unknown", ModelBudgetTag: "unknown"}
	if h == nil || h.Control == nil || h.Control.Accounts == nil || id == "" {
		return account
	}
	view, err := h.Control.Accounts.Get(r.Context(), id)
	if err != nil {
		return account
	}
	ready, hot := view.Ready, view.Hot
	inFlight, maxInFlight := view.InFlight, view.MaxInFlight
	if h.Pool != nil {
		for _, item := range h.Pool.Items() {
			if item.ID != id {
				continue
			}
			ready = item.Ready == nil || *item.Ready
			hot = item.Hot != nil && *item.Hot
			inFlight, maxInFlight = max(item.LiveInFlight, item.InFlight), item.MaxInFlight
			break
		}
	}
	account = modelUsageAccount{
		ID: view.ID, Name: view.Name, Provider: view.Provider, Region: view.ProviderRegion, Enabled: view.Enabled,
		Status: view.Status, Ready: ready, Hot: hot, InFlight: inFlight, MaxInFlight: maxInFlight,
		DownUntil: view.DownUntil, ModelBudgetTag: "unknown",
	}
	if account.Name == "" {
		account.Name = account.ID
	}
	if view.Quota != nil {
		quota := *view.Quota
		account.Quota, account.QuotaSource = &quota, "upstream_snapshot"
	}
	return account
}

func parseCompactModelUsageWindowForHandler(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	query := r.URL.Query()
	fromRaw, toRaw := query.Get("from"), query.Get("to")
	from, to := ParseQueryTime(fromRaw, false), ParseQueryTime(toRaw, true)
	if (strings.TrimSpace(fromRaw) != "" && from == nil) || (strings.TrimSpace(toRaw) != "" && to == nil) {
		writeErr(w, http.StatusBadRequest, "invalid_time", "时间范围格式无效")
		return time.Time{}, time.Time{}, false
	}
	now := time.Now().UTC()
	if to == nil {
		to = &now
	}
	if from == nil {
		value := to.Add(-24 * time.Hour)
		from = &value
	}
	if !from.Before(*to) || to.Sub(*from) > 90*24*time.Hour || to.After(now.Add(time.Minute)) {
		writeErr(w, http.StatusBadRequest, "invalid_time_range", "时间范围需为过去90天内的有效区间")
		return time.Time{}, time.Time{}, false
	}
	return from.UTC(), to.UTC(), true
}

func nonNilBudgetObservations(items []accounts.ModelBudgetObservation) []accounts.ModelBudgetObservation {
	if items == nil {
		return []accounts.ModelBudgetObservation{}
	}
	return items
}
