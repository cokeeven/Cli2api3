package console

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
)

type modelUsageAccount struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Provider       string                  `json:"provider"`
	Region         string                  `json:"region"`
	Enabled        bool                    `json:"enabled"`
	Status         string                  `json:"status"`
	Ready          bool                    `json:"ready"`
	Hot            bool                    `json:"hot"`
	InFlight       int                     `json:"in_flight"`
	MaxInFlight    int                     `json:"max_in_flight"`
	DownUntil      string                  `json:"down_until,omitempty"`
	Quota          *accounts.QuotaSnapshot `json:"quota,omitempty"`
	QuotaSource    string                  `json:"quota_source,omitempty"`
	ModelBudgetTag string                  `json:"model_budget_tag"`
}

type modelUsageRoute struct {
	Provider   string                    `json:"provider"`
	Region     string                    `json:"region"`
	Model      string                    `json:"model"`
	Candidates []executor.RouteCandidate `json:"candidates"`
}

func (h *Handler) HandleModelUsage(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeErr(w, http.StatusServiceUnavailable, "model_usage_unavailable", "模型用量服务不可用")
		return
	}
	if strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/model-usage"), "/") == "probe" {
		h.HandleModelUsageProbe(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	if h.Recorder == nil || h.Recorder.Store() == nil {
		writeErr(w, http.StatusServiceUnavailable, "model_usage_unavailable", "模型用量数据源不可用")
		return
	}
	store, ok := h.Recorder.Store().(accounts.ModelUsageStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "model_usage_unavailable", "模型用量数据源不可用")
		return
	}
	query := r.URL.Query()
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	if provider != "" && provider != "workbuddy" && provider != "trae" {
		writeErr(w, http.StatusBadRequest, "invalid_provider", "只支持 WorkBuddy 或 Trae")
		return
	}
	fromRaw, toRaw := query.Get("from"), query.Get("to")
	from, to := ParseQueryTime(fromRaw, false), ParseQueryTime(toRaw, true)
	if (strings.TrimSpace(fromRaw) != "" && from == nil) || (strings.TrimSpace(toRaw) != "" && to == nil) {
		writeErr(w, http.StatusBadRequest, "invalid_time", "时间范围格式无效")
		return
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
		return
	}
	limit := 500
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		if value, err := parseBoundedInt(raw, 1, 2000); err == nil {
			limit = value
		} else {
			writeErr(w, http.StatusBadRequest, "invalid_limit", "分页上限必须在1到2000之间")
			return
		}
	}
	filter := accounts.ModelUsageQuery{
		Provider: provider, Region: strings.ToLower(strings.TrimSpace(query.Get("region"))),
		Model: strings.TrimSpace(query.Get("model")), Account: strings.TrimSpace(query.Get("account")),
		From: *from, To: *to, Limit: limit,
	}
	data, err := store.ReadModelUsage(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "model_usage_failed", "读取模型用量失败")
		return
	}
	var budgets []accounts.WorkBuddyBudgetEstimate
	if !strings.EqualFold(provider, "trae") {
		allBudgets := accounts.EstimateWorkBuddyBudgetsInRange(data.BudgetObservations, data.To, data.BudgetObservationFrom, data.BudgetObservationsTruncated)
		for _, budget := range allBudgets {
			if filter.Account == "" || budget.AccountID == filter.Account {
				budgets = append(budgets, budget)
			}
		}
	}
	response := map[string]any{
		"data":                        data,
		"accounts":                    h.modelUsageAccounts(r, provider, filter.Region),
		"routes":                      h.modelUsageRoutes(provider, filter.Region, filter.Model, filter.Account, data.Rows),
		"workbuddy_budgets":           budgets,
		"budget_observation_complete": !data.BudgetObservationsTruncated,
		"source":                      "gateway_local_observation",
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) modelUsageAccounts(r *http.Request, provider, region string) []modelUsageAccount {
	if h.Control == nil || h.Control.Accounts == nil {
		return []modelUsageAccount{}
	}
	views, err := h.Control.Accounts.List(r.Context(), false)
	if err != nil {
		return []modelUsageAccount{}
	}
	poolItems := map[string]executor.Item{}
	if h.Pool != nil {
		for _, item := range h.Pool.Items() {
			poolItems[item.ID] = item
		}
	}
	out := make([]modelUsageAccount, 0, len(views))
	for _, view := range views {
		if provider != "" && !strings.EqualFold(provider, view.Provider) {
			continue
		}
		if region != "" && !strings.EqualFold(region, view.ProviderRegion) {
			continue
		}
		item, hasItem := poolItems[view.ID]
		ready := view.Ready
		hot := view.Hot
		inFlight, maxInFlight := view.InFlight, view.MaxInFlight
		if hasItem {
			ready = item.Ready == nil || *item.Ready
			hot = item.Hot != nil && *item.Hot
			inFlight, maxInFlight = max(item.LiveInFlight, item.InFlight), item.MaxInFlight
		}
		account := modelUsageAccount{
			ID: view.ID, Name: view.Name, Provider: view.Provider, Region: view.ProviderRegion,
			Enabled: view.Enabled, Status: view.Status, Ready: ready, Hot: hot,
			InFlight: inFlight, MaxInFlight: maxInFlight, DownUntil: view.DownUntil,
			ModelBudgetTag: "unknown",
		}
		if view.Quota != nil {
			quota := *view.Quota
			account.Quota = &quota
			account.QuotaSource = "upstream_snapshot"
		}
		out = append(out, account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (h *Handler) modelUsageRoutes(provider, region, model, account string, rows []accounts.ModelUsageRow) []modelUsageRoute {
	if h.Pool == nil {
		return []modelUsageRoute{}
	}
	models := map[string]struct{}{}
	if model != "" {
		models[model] = struct{}{}
	} else {
		for _, item := range h.Pool.Items() {
			itemProvider := executor.NormalizeProviderFamily(item.Provider)
			if provider != "" && !strings.EqualFold(provider, itemProvider) || region != "" && !strings.EqualFold(region, item.Region) {
				continue
			}
			if itemProvider != "workbuddy" && itemProvider != "trae" {
				continue
			}
			for _, itemModel := range item.Models {
				models[itemModel] = struct{}{}
			}
		}
		for _, row := range rows {
			if provider != "" && row.Provider != provider || region != "" && !strings.EqualFold(row.Region, region) {
				continue
			}
			models[row.Model] = struct{}{}
			if len(models) >= 50 {
				break
			}
		}
	}
	ordered := make([]string, 0, len(models))
	for value := range models {
		if strings.TrimSpace(value) != "" {
			ordered = append(ordered, value)
		}
	}
	sort.Strings(ordered)
	var out []modelUsageRoute
	for _, modelID := range ordered {
		for _, platform := range []struct{ provider, region string }{{"workbuddy", "cn"}, {"workbuddy", "global"}, {"trae", "cn"}} {
			if provider != "" && platform.provider != provider || region != "" && !strings.EqualFold(platform.region, region) {
				continue
			}
			candidates := h.Pool.InspectRoute(executor.RouteQuery{PublicModel: modelID, ProviderFilter: platform.provider, RegionFilter: platform.region})
			if account != "" {
				filtered := candidates[:0]
				for _, candidate := range candidates {
					if candidate.ID == account {
						filtered = append(filtered, candidate)
					}
				}
				candidates = filtered
			}
			out = append(out, modelUsageRoute{Provider: platform.provider, Region: platform.region, Model: modelID, Candidates: candidates})
		}
	}
	if out == nil {
		out = []modelUsageRoute{}
	}
	return out
}

func parseBoundedInt(raw string, minValue, maxValue int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minValue || value > maxValue {
		return 0, strconv.ErrSyntax
	}
	return value, nil
}
