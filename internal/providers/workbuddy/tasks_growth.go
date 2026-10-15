// WorkBuddy CN growth activities use the chat host; credit gifts use billing.
package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// ---------------------------------------------------------------------------
// CN endpoint contracts cross-checked with workbuddy2api-hub wb_tasks.py.
// ---------------------------------------------------------------------------
const (
	pathGrowthTasksV2     = "/v2/activity/growth/tasks"
	pathGrowthTasksAccept = "/v2/activity/growth/tasks/accept"
	pathGrowthTasksClaim  = "/activity/growth/tasks/"

	pathTravelStatus   = "/activity/growth/buddy/travel/status"
	pathTravelDepart   = "/activity/growth/buddy/travel/depart"
	pathTravelClaim    = "/activity/growth/buddy/travel/claim"
	pathTravelConfig   = "/activity/growth/buddy/travel/config"
	pathStreak         = "/activity/growth/streak"
	pathStreakRedeem   = "/activity/growth/redeem"
	pathHeatmap        = "/activity/growth/heatmap"
	pathMakeup         = "/activity/growth/makeup-cards/use"
	pathLotterySummary = "/activity/growth/lottery/summary"
	pathLotteryDraw    = "/activity/growth/lottery/draw"

	pathClaimGift         = "/billing/meter/claim-gift"
	pathClaimCompensation = "/billing/meter/claim-compensation"
)

type growthBusinessError struct {
	code    int
	message string
}

func (e *growthBusinessError) Error() string {
	return fmt.Sprintf("growth operation rejected (code %d)", e.code)
}

// ---------------------------------------------------------------------------
// 通用 growth 域请求
// ---------------------------------------------------------------------------

// growthDo returns the data envelope using the identity of the endpoint host.
// base 由调用方指定（ChatBase 或 BillingBase），以匹配上游各端点归属域。
func (c *Client) growthDo(ctx context.Context, accountID string, credential Credential, base, method, path string, body []byte) (json.RawMessage, error) {
	if body == nil && method != http.MethodGet {
		body = []byte("{}")
	}
	raw, status, err := c.do(ctx, accountID, method, base+path, body,
		func(h http.Header) {
			if base == credential.BillingBase() {
				SetBillingHeaders(h, credential)
			} else {
				SetChatHeaders(h, credential)
				InjectDeviceToken(h, credential)
			}
		})
	if err != nil {
		return nil, err
	}
	text := string(raw)
	if classified := Classify(status, text); classified.Kind == accounts.KindAuth {
		return nil, &ClassifiedError{Kind: accounts.KindAuth, Status: http.StatusUnauthorized, Message: "workbuddy session dead: re-login required"}
	}
	var env struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(raw, &env)
	if env.Code != nil && *env.Code == http.StatusUnauthorized || growthAuthMessage(env.Msg) {
		return nil, &ClassifiedError{Kind: accounts.KindAuth, Status: http.StatusUnauthorized, Message: "workbuddy session dead: re-login required"}
	}
	if status == http.StatusTooManyRequests {
		return nil, &ClassifiedError{Kind: accounts.KindRateLimit, Status: status, Message: "growth request rate limited"}
	}
	if status >= 500 {
		return nil, &ClassifiedError{Kind: accounts.KindUnavailable, Status: status, Message: "growth service unavailable"}
	}
	if status == http.StatusForbidden {
		message := env.Msg
		return nil, &growthBusinessError{code: status, message: message}
	}
	if decodeErr == nil && env.Code != nil && *env.Code != 0 {
		return nil, &growthBusinessError{code: *env.Code, message: env.Msg}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("growth %s http=%d", path, status)
	}
	if decodeErr != nil || env.Code == nil {
		return nil, fmt.Errorf("growth response missing valid status")
	}
	return env.Data, nil
}

func growthAuthMessage(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"session expired", "token expired", "invalid token", "unauthorized", "not authenticated", "login required", "未登录", "登录已过期", "登录失效", "登录态失效", "会话已失效"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 成长任务
// ---------------------------------------------------------------------------

// GrowthTask 单个成长任务的对外视图。
type GrowthTask struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title"`
	Credit       int64  `json:"credit"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
	AcceptStatus string `json:"accept_status"`
	Status       string `json:"status"`
	Claimable    bool   `json:"claimable"`
	Claimed      bool   `json:"claimed"`
	Locked       bool   `json:"locked"`
	TaskType     string `json:"task_type"`
	JumpURL      string `json:"jump_url"`
	Unforgeable  bool   `json:"unforgeable"`
}

func (task *GrowthTask) UnmarshalJSON(data []byte) error {
	type flatTask GrowthTask
	var parsed flatTask
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	var extra struct {
		Progress     json.RawMessage `json:"progress"`
		RewardCredit *int64          `json:"reward_credit"`
	}
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	if len(extra.Progress) > 0 && string(extra.Progress) != "null" {
		var progress struct {
			Current *int64 `json:"current"`
			Target  *int64 `json:"target"`
		}
		if err := json.Unmarshal(extra.Progress, &progress); err != nil {
			return fmt.Errorf("invalid task progress: %w", err)
		}
		if progress.Current != nil {
			parsed.Current = *progress.Current
		}
		if progress.Target != nil {
			parsed.Target = *progress.Target
		}
	}
	if extra.RewardCredit != nil {
		parsed.Credit = *extra.RewardCredit
	}
	parsed.Claimed = parsed.Claimed || parsed.AcceptStatus == "claimed" || parsed.Status == "claimed"
	parsed.Locked = parsed.Locked || parsed.AcceptStatus == "locked" || parsed.Status == "locked"
	parsed.Claimable = !parsed.Claimed && !parsed.Locked && (parsed.Claimable || parsed.AcceptStatus == "completed" || parsed.Status == "completed" || (parsed.Target > 0 && parsed.Current >= parsed.Target))
	*task = GrowthTask(parsed)
	return nil
}

// ListGrowthTasks 拉取全量成长任务（v2 口径）。
func (c *Client) ListGrowthTasks(ctx context.Context, accountID string, credential Credential) ([]GrowthTask, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathGrowthTasksV2, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tasks *[]GrowthTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed.Tasks == nil {
		return nil, fmt.Errorf("growth response missing tasks")
	}
	return *parsed.Tasks, nil
}

// AcceptGrowthTasks 报名（accept）任务；可幂等重放。
func (c *Client) AcceptGrowthTasks(ctx context.Context, accountID string, credential Credential, codes []string) error {
	// The upstream accepts at most 20 task codes per batch.
	for start := 0; start < len(codes); start += 20 {
		end := min(start+20, len(codes))
		if err := c.acceptGrowthTaskBatch(ctx, accountID, credential, codes[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) acceptGrowthTaskBatch(ctx context.Context, accountID string, credential Credential, codes []string) error {
	body, _ := json.Marshal(map[string]any{"task_codes": codes})
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathGrowthTasksAccept, body)
	if err != nil {
		return err
	}
	var response struct {
		Results *[]struct {
			TaskCode string `json:"task_code"`
			Status   string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode growth acceptance results: %w", err)
	}
	confirmed := make(map[string]bool, len(codes))
	if response.Results != nil {
		// A successful envelope can contain individual business failures.
		for _, result := range *response.Results {
			ok := result.Status == "accepted" || result.Status == "already_accepted"
			if previous, seen := confirmed[result.TaskCode]; seen {
				ok = ok && previous
			}
			confirmed[result.TaskCode] = ok
		}
	} else {
		// Older responses omit results. Confirm from upstream state instead of
		// treating the envelope alone as proof that every task was accepted.
		tasks, err := c.ListGrowthTasks(ctx, accountID, credential)
		if err != nil {
			return fmt.Errorf("verify growth acceptance: %w", err)
		}
		for _, task := range tasks {
			confirmed[task.TaskCode] = task.AcceptStatus == "accepted" || task.AcceptStatus == "already_accepted" || task.AcceptStatus == "in_progress" || task.Claimed || task.Claimable
		}
	}
	unconfirmed := 0
	for _, code := range codes {
		if !confirmed[code] {
			unconfirmed++
		}
	}
	if unconfirmed > 0 {
		return fmt.Errorf("growth acceptance unconfirmed for %d of %d tasks", unconfirmed, len(codes))
	}
	return nil
}

// ClaimGrowthTask 领取单个已达标的任务奖励；重复领返回业务错误（安全）。
func (c *Client) ClaimGrowthTask(ctx context.Context, accountID string, credential Credential, code string) error {
	if code == "" || code == "." || code == ".." || strings.ContainsAny(code, "/\\?#%") {
		return fmt.Errorf("invalid growth task code")
	}
	_, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathGrowthTasksClaim+code+"/claim", nil)
	return err
}

// ---------------------------------------------------------------------------
// 猫猫旅行
// ---------------------------------------------------------------------------

// TravelState 猫猫旅行状态。
type TravelState struct {
	State             string `json:"state"`
	DailyLimitReached bool   `json:"daily_limit_reached"`
	RecordID          int64  `json:"record_id"`
	RewardCredit      int64  `json:"reward_credit"`
}

type TravelLocation struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) TravelConfig(ctx context.Context, accountID string, credential Credential) ([]TravelLocation, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathTravelConfig, nil)
	if err != nil {
		return nil, err
	}
	var config struct {
		Locations *[]TravelLocation `json:"locations"`
	}
	if err := json.Unmarshal(data, &config); err != nil || config.Locations == nil {
		return nil, fmt.Errorf("invalid travel configuration")
	}
	for _, location := range *config.Locations {
		if location.ID <= 0 {
			return nil, fmt.Errorf("invalid travel destination")
		}
	}
	return *config.Locations, nil
}

func (c *Client) TravelStatus(ctx context.Context, accountID string, credential Credential) (*TravelState, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathTravelStatus, nil)
	if err != nil {
		return nil, err
	}
	var st TravelState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// TravelDepart dispatches to a location returned by TravelConfig.
func (c *Client) TravelDepart(ctx context.Context, accountID string, credential Credential, locationID int) error {
	body, _ := json.Marshal(map[string]any{"location_id": locationID})
	_, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathTravelDepart, body)
	return err
}

// TravelClaim 领取到站奖励，返回 reward_credit。
func (c *Client) TravelClaim(ctx context.Context, accountID string, credential Credential, recordID int64) (int64, error) {
	// The server identifies the arrived trip from the account, not record_id.
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathTravelClaim, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		RewardCredit int64 `json:"reward_credit"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("decode travel reward: %w", err)
	}
	return resp.RewardCredit, nil
}

// ---------------------------------------------------------------------------
// 连登兑换 + 抽奖
// ---------------------------------------------------------------------------

type GrowthStreakTier struct {
	Tier string `json:"tier"`
}

type GrowthStreakState struct {
	Streak struct {
		Days int `json:"days"`
	} `json:"streak"`
	MakeupCards struct {
		Balance int `json:"balance"`
	} `json:"makeup_cards"`
	RedemptionStatus struct {
		Tiers         []GrowthStreakTier `json:"tiers"`
		Tier7DStatus  string             `json:"tier_7d_status"`
		Tier14DStatus string             `json:"tier_14d_status"`
		Tier28DStatus string             `json:"tier_28d_status"`
	} `json:"redemption_status"`
}

func (s *GrowthStreakState) TierStatus(tier string) string {
	switch tier {
	case "7d":
		return s.RedemptionStatus.Tier7DStatus
	case "14d":
		return s.RedemptionStatus.Tier14DStatus
	case "28d":
		return s.RedemptionStatus.Tier28DStatus
	default:
		return ""
	}
}

func (c *Client) GrowthStreakFull(ctx context.Context, accountID string, credential Credential) (*GrowthStreakState, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathStreak, nil)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	var state GrowthStreakState
	if err := json.Unmarshal(data, &fields); err != nil || len(fields["streak"]) == 0 || string(fields["streak"]) == "null" || len(fields["makeup_cards"]) == 0 || string(fields["makeup_cards"]) == "null" || len(fields["redemption_status"]) == 0 || string(fields["redemption_status"]) == "null" {
		return nil, fmt.Errorf("incomplete growth streak state")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid growth streak state")
	}
	if state.Streak.Days < 0 || state.MakeupCards.Balance < 0 {
		return nil, fmt.Errorf("invalid growth streak counters")
	}
	return &state, nil
}

type GrowthHeatmapCell struct {
	Date  string `json:"date"`
	Score int    `json:"score"`
}

func (c *Client) GrowthHeatmap(ctx context.Context, accountID string, credential Credential) ([]GrowthHeatmapCell, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathHeatmap, nil)
	if err != nil {
		return nil, err
	}
	var heatmap struct {
		Cells *[]struct {
			Date  string `json:"date"`
			Score *int   `json:"score"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(data, &heatmap); err != nil || heatmap.Cells == nil {
		return nil, fmt.Errorf("invalid growth heatmap")
	}
	cells := make([]GrowthHeatmapCell, 0, len(*heatmap.Cells))
	for _, cell := range *heatmap.Cells {
		if _, err := time.Parse("2006-01-02", cell.Date); err != nil || cell.Score == nil || *cell.Score < 0 {
			return nil, fmt.Errorf("invalid growth heatmap cell")
		}
		cells = append(cells, GrowthHeatmapCell{Date: cell.Date, Score: *cell.Score})
	}
	return cells, nil
}

func (c *Client) GrowthMakeup(ctx context.Context, accountID string, credential Credential, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid makeup date")
	}
	body, _ := json.Marshal(map[string]string{"target_date": date})
	_, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathMakeup, body)
	return err
}

func (c *Client) LotteryChances(ctx context.Context, accountID string, credential Credential) (int, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodGet, pathLotterySummary, nil)
	if err != nil {
		return 0, err
	}
	var summary struct {
		Chances *int `json:"chances"`
	}
	if err := json.Unmarshal(data, &summary); err != nil || summary.Chances == nil || *summary.Chances < 0 {
		return 0, fmt.Errorf("invalid growth lottery summary")
	}
	return *summary.Chances, nil
}

// GrowthRedeemTier 兑换连登档位（tier: "7d"|"14d"|"28d"）。未解锁返回错误（按 locked 跳过）。
func (c *Client) GrowthRedeemTier(ctx context.Context, accountID string, credential Credential, tier string) error {
	body, _ := json.Marshal(map[string]any{"tier": tier, "client_token": newCLIRequestID()})
	_, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathStreakRedeem, body)
	return err
}

// LotteryDraw 抽一次奖（消耗 1 次 chances）。
func (c *Client) LotteryDraw(ctx context.Context, accountID string, credential Credential) error {
	body, _ := json.Marshal(map[string]any{"client_token": newCLIRequestID()})
	_, err := c.growthDo(ctx, accountID, credential, credential.ChatBase(), http.MethodPost, pathLotteryDraw, body)
	return err
}

// ---------------------------------------------------------------------------
// 夜猫子窗口
// ---------------------------------------------------------------------------

// InNightWindow 当前是否处于夜猫子计数窗口（23:00–08:00 本地时区）。
func InNightWindow(now time.Time) bool {
	h := now.Hour()
	return h >= 23 || h < 8
}

// ---------------------------------------------------------------------------
// 新手礼包 / 活动补偿
// ---------------------------------------------------------------------------

func (c *Client) claimCredit(ctx context.Context, accountID string, credential Credential, path string) (int64, error) {
	data, err := c.growthDo(ctx, accountID, credential, credential.BillingBase(), http.MethodPost, path, []byte("{}"))
	if err != nil {
		return 0, err
	}
	var resp struct {
		Credit int64 `json:"credit"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("decode credit reward: %w", err)
	}
	return resp.Credit, nil
}

// ClaimGift 领取新手礼包（每号一次，已领返回业务错误）。
func (c *Client) ClaimGift(ctx context.Context, accountID string, credential Credential) (int64, error) {
	return c.claimCredit(ctx, accountID, credential, pathClaimGift)
}

// ClaimCompensation 领取活动补偿（有则领，无则业务错误）。
func (c *Client) ClaimCompensation(ctx context.Context, accountID string, credential Credential) (int64, error) {
	return c.claimCredit(ctx, accountID, credential, pathClaimCompensation)
}
