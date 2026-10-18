package console

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/buildinfo"
	"github.com/caigee-cmd/cli2api/internal/executor"
)

const (
	modelProbeTimeout     = 30 * time.Second
	modelProbeMaxInflight = 2
	modelProbeMaxTokens   = 16
)

type modelProbeRequest struct {
	AccountID string `json:"account_id"`
	Model     string `json:"model"`
}

type modelProbeResponse struct {
	OK                 bool   `json:"ok"`
	RequestID          string `json:"request_id,omitempty"`
	Provider           string `json:"provider"`
	Region             string `json:"region"`
	AccountID          string `json:"account_id"`
	ObservedAccountID  string `json:"observed_account_id,omitempty"`
	Model              string `json:"model"`
	RequestedMaxTokens int    `json:"requested_max_tokens"`
	Output             string `json:"output,omitempty"`
	FinishReason       string `json:"finish_reason,omitempty"`
	FinishReasonSource string `json:"finish_reason_source,omitempty"`
	PromptTokens       *int64 `json:"prompt_tokens"`
	CompletionTokens   *int64 `json:"completion_tokens"`
	UsageSource        string `json:"usage_source,omitempty"`
	BuildFingerprint   string `json:"build_fingerprint"`
	ErrorCode          string `json:"error_code,omitempty"`
	ErrorMessage       string `json:"error_message,omitempty"`
}

type modelProbeCall struct {
	done   chan struct{}
	result modelProbeResponse
}

func (h *Handler) HandleModelUsageProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	var input modelProbeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_probe", "探测参数无效")
		return
	}
	input.AccountID, input.Model = strings.TrimSpace(input.AccountID), strings.TrimSpace(input.Model)
	if input.AccountID == "" || input.Model == "" || len(input.Model) > 256 {
		writeErr(w, http.StatusBadRequest, "invalid_probe", "必须明确选择账号和模型")
		return
	}
	view, _, err := h.validateModelProbe(r.Context(), input)
	if err != nil {
		writeErr(w, http.StatusConflict, "probe_not_available", err.Error())
		return
	}
	callKey := strings.Join([]string{view.ID, strings.ToLower(view.Provider), view.ProviderRegion, input.Model}, "\x00")
	h.modelProbeMu.Lock()
	if h.modelProbeInflight == nil {
		h.modelProbeInflight = make(map[string]*modelProbeCall)
	}
	if existing := h.modelProbeInflight[callKey]; existing != nil {
		h.modelProbeMu.Unlock()
		select {
		case <-existing.done:
			writeJSON(w, http.StatusOK, existing.result)
		case <-r.Context().Done():
			writeErr(w, http.StatusRequestTimeout, "probe_wait_canceled", "等待合并探测时请求已取消")
		}
		return
	}
	if len(h.modelProbeInflight) >= modelProbeMaxInflight {
		h.modelProbeMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "probe_busy", "当前已有两个探测任务，请稍后再试")
		return
	}
	call := &modelProbeCall{done: make(chan struct{})}
	h.modelProbeInflight[callKey] = call
	h.modelProbeMu.Unlock()

	call.result = h.runModelProbe(r, input, view)
	h.modelProbeMu.Lock()
	delete(h.modelProbeInflight, callKey)
	close(call.done)
	h.modelProbeMu.Unlock()
	writeJSON(w, http.StatusOK, call.result)
}

func (h *Handler) validateModelProbe(ctx context.Context, input modelProbeRequest) (accounts.AccountView, executor.Item, error) {
	if h.Control == nil || h.Control.Accounts == nil || h.Pool == nil || h.Chat == nil {
		return accounts.AccountView{}, executor.Item{}, errors.New("模型探测服务不可用")
	}
	views, err := h.Control.Accounts.List(ctx, false)
	if err != nil {
		return accounts.AccountView{}, executor.Item{}, errors.New("读取本机账号状态失败")
	}
	var selected *accounts.AccountView
	for index := range views {
		if views[index].ID == input.AccountID {
			selected = &views[index]
			break
		}
	}
	if selected == nil || !selected.Enabled {
		return accounts.AccountView{}, executor.Item{}, errors.New("所选账号不存在或已停用")
	}
	provider := strings.ToLower(strings.TrimSpace(selected.Provider))
	if provider != "workbuddy" && provider != "trae" {
		return accounts.AccountView{}, executor.Item{}, errors.New("仅支持 WorkBuddy 或 Trae 账号探测")
	}
	item, ok := h.Pool.ByID(selected.ID)
	if !ok {
		return accounts.AccountView{}, executor.Item{}, errors.New("所选账号当前未运行")
	}
	if executor.NormalizeProviderFamily(item.Provider) != provider || executor.NormalizeRegion(item.Region) != executor.NormalizeRegion(selected.ProviderRegion) {
		return accounts.AccountView{}, executor.Item{}, errors.New("账号运行时信息不一致")
	}
	route := h.Pool.InspectRoute(executor.RouteQuery{PublicModel: input.Model, ProviderFilter: provider, RegionFilter: selected.ProviderRegion})
	for _, candidate := range route {
		if candidate.ID != selected.ID {
			continue
		}
		if candidate.Reason != "available" {
			return accounts.AccountView{}, executor.Item{}, probeReasonError(candidate.Reason)
		}
		return *selected, item, nil
	}
	return accounts.AccountView{}, executor.Item{}, errors.New("所选模型不在当前可调度候选中")
}

func probeReasonError(reason string) error {
	return probeValidationError{reason: reason}
}

type probeValidationError struct{ reason string }

func (e probeValidationError) Error() string {
	switch e.reason {
	case "account_cooldown", "model_cooldown":
		return "所选账号或模型仍在冷却中，探测未发送"
	case "quota_exhausted":
		return "所选账号额度已耗尽，探测未发送"
	case "account_concurrency":
		return "所选账号当前并发已满，探测未发送"
	case "model_catalog":
		return "所选模型不在该账号目录中，探测未发送"
	case "not_ready":
		return "所选账号未就绪，探测未发送"
	default:
		return "所选账号当前不可调度，探测未发送"
	}
}

func (h *Handler) runModelProbe(parent *http.Request, input modelProbeRequest, view accounts.AccountView) modelProbeResponse {
	result := modelProbeResponse{
		Provider: strings.ToLower(view.Provider), Region: view.ProviderRegion, AccountID: view.ID,
		Model: input.Model, RequestedMaxTokens: modelProbeMaxTokens, BuildFingerprint: strings.TrimSpace(buildinfo.Version + "/" + buildinfo.Commit),
	}
	if h.Chat == nil {
		result.ErrorCode, result.ErrorMessage = "chat_unavailable", "正常聊天调用链不可用"
		return result
	}
	requestID, err := randomProbeID()
	if err != nil {
		result.ErrorCode, result.ErrorMessage = "probe_id_failed", "无法创建探测请求标识"
		return result
	}
	body, _ := json.Marshal(map[string]any{
		"model":    input.Model,
		"messages": []map[string]string{{"role": "user", "content": "Reply with exactly the word probe-ok. Do not use tools."}},
		"stream":   false, "max_tokens": modelProbeMaxTokens,
	})
	ctx, cancel := context.WithTimeout(parent.Context(), modelProbeTimeout)
	defer cancel()
	ctx = executor.WithStrictAccount(ctx, view.ID)
	probe := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	probe.Header.Set("Content-Type", "application/json")
	probe.Header.Set("X-Client-Request-ID", "manual-probe-"+requestID)
	probe.Header.Set("X-Qoder-Account", view.ID)
	writer := httptest.NewRecorder()
	h.Chat(writer, probe)
	result.RequestID = writer.Header().Get("X-Request-Id")
	result.ObservedAccountID = writer.Header().Get("X-CLI2API-Account")
	if result.ObservedAccountID == "" {
		result.ObservedAccountID = writer.Header().Get("X-Qoder-Account")
	}
	if writer.Code != http.StatusOK {
		result.ErrorCode = writer.Header().Get("X-Qoder-Error-Kind")
		if result.ErrorCode == "" {
			result.ErrorCode = "probe_upstream_error"
		}
		result.ErrorMessage = "探测请求未成功完成，请查看关联请求日志"
		return result
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			Source           string `json:"source"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(writer.Body, 1<<20)).Decode(&response); err != nil {
		result.ErrorCode, result.ErrorMessage = "probe_invalid_response", "网关返回无法解析的探测响应"
		return result
	}
	if len(response.Choices) > 0 {
		result.Output = strings.TrimSpace(response.Choices[0].Message.Content)
		result.FinishReason = writer.Header().Get("X-CLI2API-Finish-Reason")
		result.FinishReasonSource = writer.Header().Get("X-CLI2API-Finish-Reason-Source")
		if result.FinishReason == "" {
			result.FinishReason = response.Choices[0].FinishReason
		}
		if result.FinishReasonSource == "" {
			result.FinishReasonSource = "gateway_fallback_or_unknown"
		}
	}
	result.PromptTokens, result.CompletionTokens, result.UsageSource = response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.Source
	switch {
	case result.RequestID == "":
		result.ErrorCode, result.ErrorMessage = "probe_request_id_missing", "网关未返回可关联的 request_id"
	case result.ObservedAccountID != view.ID:
		result.ErrorCode, result.ErrorMessage = "probe_account_mismatch", "实际账号与选择账号不一致，探测未通过"
	case result.Output == "":
		result.ErrorCode, result.ErrorMessage = "probe_empty_output", "探测响应没有可见正文"
	case result.FinishReason == "":
		result.ErrorCode, result.ErrorMessage = "probe_terminal_missing", "探测响应缺少结束原因"
	case result.FinishReason != "stop":
		result.ErrorCode, result.ErrorMessage = "probe_non_normal_finish", "探测响应未以正常 stop 结束"
	case result.FinishReasonSource != "upstream":
		result.ErrorCode, result.ErrorMessage = "probe_terminal_not_confirmed", "结束原因不是经适配器确认的上游终态"
	case result.PromptTokens == nil || result.CompletionTokens == nil || (result.UsageSource != "upstream" && result.UsageSource != "provider"):
		result.ErrorCode, result.ErrorMessage = "probe_usage_missing", "探测响应缺少上游确认的完整 usage"
	default:
		result.OK = true
	}
	return result
}

func randomProbeID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
