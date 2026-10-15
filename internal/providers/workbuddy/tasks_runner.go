package workbuddy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

type TaskStepResult = providers.DailyTaskStep
type DailyTasksReport = providers.DailyTasksReport

type DailyTasksOptions struct {
	TravelLocationID int
	RedeemTiers      bool
	ClaimGift        bool
	NightChats       bool // Reserved; no background chat is generated.
	Previous         []providers.DailyTaskStep
}

var growthReadbackDelays = []time.Duration{0, time.Second, 2 * time.Second}
var growthEventInterval = time.Second

func taskFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	default:
		return "upstream request failed"
	}
}

func taskResult(err error) (string, string) {
	if err == nil {
		return "success", "completed"
	}
	var business *growthBusinessError
	if errors.As(err, &business) {
		msg := strings.ToLower(business.message)
		for _, marker := range []string{"already claimed", "already redeemed", "已领取", "已兑换"} {
			if strings.Contains(msg, marker) {
				return "already", "already completed"
			}
		}
		for _, marker := range []string{"未解锁", "未达标", "条件不满足", "连续登录天数不足", "企业账号不支持该操作", "not eligible", "not unlocked"} {
			if strings.Contains(msg, marker) {
				return "skipped", "requirements not met"
			}
		}
		return "error", fmt.Sprintf("upstream rejected operation (code %d)", business.code)
	}
	return "error", taskFailure(err)
}

func taskPause(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type dailyTaskRunner struct {
	client         *Client
	ctx            context.Context
	id             string
	credential     Credential
	options        DailyTasksOptions
	report         *DailyTasksReport
	events         int
	lastEventAt    time.Time
	firstProcessed bool
}

func (r *dailyTaskRunner) completed(name string) bool {
	for _, step := range r.options.Previous {
		if step.Step == name && (step.Status == "success" || step.Status == "already") {
			r.report.Reuse(step)
			return true
		}
	}
	return false
}

func (r *dailyTaskRunner) result(name string, err error) {
	status, message := taskResult(err)
	r.report.Add(name, status, message)
}

func (r *dailyTaskRunner) submitEvent(event map[string]any) error {
	if !r.lastEventAt.IsZero() {
		if delay := time.Until(r.lastEventAt.Add(growthEventInterval)); delay > 0 {
			if err := taskPause(r.ctx, delay); err != nil {
				return err
			}
		}
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	r.lastEventAt = time.Now()
	r.events++
	return r.client.ReportGrowthEvents(r.ctx, r.id, r.credential, []map[string]any{event})
}

func (r *dailyTaskRunner) findTask(code string, ready func(GrowthTask) bool) (*GrowthTask, error) {
	var found *GrowthTask
	for _, delay := range growthReadbackDelays {
		if err := taskPause(r.ctx, delay); err != nil {
			return nil, err
		}
		tasks, err := r.client.ListGrowthTasks(r.ctx, r.id, r.credential)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.TaskCode == code {
				copy := task
				found = &copy
				if ready(task) {
					return found, nil
				}
				break
			}
		}
	}
	if found == nil {
		return nil, errors.New("task absent from upstream readback")
	}
	return found, nil
}

func growthReady(task GrowthTask) bool {
	return task.Claimed || task.Claimable || task.Status == "completed" || task.Target > 0 && task.Current >= task.Target
}

func (r *dailyTaskRunner) claimTask(task GrowthTask) {
	name := "growth:" + task.TaskCode
	if task.Claimed {
		r.report.Add(name, "already", "reward already claimed")
		return
	}
	alreadyClaimed := false
	if err := r.client.ClaimGrowthTask(r.ctx, r.id, r.credential, task.TaskCode); err != nil {
		status, _ := taskResult(err)
		if status != "already" {
			r.result(name, err)
			return
		}
		alreadyClaimed = true
	}
	fresh, err := r.findTask(task.TaskCode, func(task GrowthTask) bool { return task.Claimed })
	if err != nil {
		r.result(name, err)
		return
	}
	if !fresh.Claimed {
		r.report.Add(name, "error", "claim acknowledged but reward status is not confirmed")
		return
	}
	if alreadyClaimed {
		r.report.Add(name, "already", "reward already claimed and confirmed by upstream task state")
		return
	}
	r.report.Add(name, "success", "reward claimed and confirmed by upstream task state")
}

func (r *dailyTaskRunner) progressTask(task GrowthTask) {
	name := "growth:" + task.TaskCode
	if r.completed(name) {
		return
	}
	if task.Claimed {
		r.claimTask(task)
		return
	}
	if task.Locked {
		r.report.Add(name, "skipped", "task locked")
		return
	}
	if growthReady(task) {
		r.claimTask(task)
		return
	}
	if reason, manual := growthManualTasks[task.TaskCode]; manual {
		r.report.Add(name, "skipped", reason)
		return
	}
	kind, supported := growthEventKinds[task.TaskCode]
	if task.Unforgeable || !supported {
		r.report.Add(name, "skipped", "requires a real action or an unsupported task flow")
		return
	}
	if task.TaskType != "auto" && task.TaskCode != "first_buddy" && !growthAccepted(task) {
		r.report.Add(name, "error", "task acceptance is not confirmed; progress was not submitted")
		return
	}
	if task.Target <= 0 {
		r.report.Add(name, "error", "task target is missing; progress was not submitted")
		return
	}
	need := task.Target - task.Current
	if need > 8 || need > int64(20-r.events) {
		r.report.Add(name, "error", "bounded event budget reached; task deferred")
		return
	}
	for index := int64(0); index < need; index++ {
		if err := r.ctx.Err(); err != nil {
			r.result(name, err)
			return
		}
		event, err := buildGrowthEvent(r.credential, kind, task.Current+index)
		if err != nil {
			r.result(name, err)
			return
		}
		if err = r.submitEvent(event); err != nil {
			r.result(name, err)
			return
		}
	}
	fresh, err := r.findTask(task.TaskCode, growthReady)
	if err != nil {
		r.result(name, err)
		return
	}
	if !growthReady(*fresh) {
		r.report.Add(name, "error", fmt.Sprintf("event acknowledged; progress %d/%d remains incomplete, reward not claimed", fresh.Current, fresh.Target))
		return
	}
	r.claimTask(*fresh)
}

func (r *dailyTaskRunner) growth() {
	tasks, err := r.client.ListGrowthTasks(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("growth", err)
		return
	}
	if len(tasks) == 0 {
		r.report.Add("growth", "skipped", "no growth tasks available")
		return
	}
	// The automatic first_buddy reward grants the prerequisite Buddy instance.
	for _, task := range tasks {
		if task.TaskCode == "first_buddy" && !r.firstProcessed {
			r.progressTask(task)
			break
		}
	}
	for _, task := range tasks {
		if task.TaskCode != "first_buddy" && task.TaskType == "auto" {
			r.progressTask(task)
		}
	}
	tasks, err = r.client.ListGrowthTasks(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("growth", err)
		return
	}
	for _, task := range tasks {
		if task.TaskCode == "first_buddy" || task.TaskType == "auto" {
			continue
		}
		if r.completed("growth:" + task.TaskCode) {
			continue
		}
		if !task.Locked && !task.Claimed && !growthReady(task) && !task.Unforgeable && growthEventKinds[task.TaskCode] != "" &&
			(task.AcceptStatus == "" || task.AcceptStatus == "not_accepted") {
			if err := r.client.AcceptGrowthTasks(r.ctx, r.id, r.credential, []string{task.TaskCode}); err != nil {
				r.result("growth:"+task.TaskCode, err)
				continue
			}
			fresh, err := r.findTask(task.TaskCode, func(task GrowthTask) bool {
				return growthAccepted(task) || growthReady(task)
			})
			if err != nil {
				r.result("growth:"+task.TaskCode, err)
				continue
			}
			task = *fresh
		}
		r.progressTask(task)
	}
}

func growthAccepted(task GrowthTask) bool {
	return task.AcceptStatus == "accepted" || task.AcceptStatus == "already_accepted" || task.AcceptStatus == "in_progress"
}

func (r *dailyTaskRunner) prerequisite() {
	tasks, err := r.client.ListGrowthTasks(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("growth_prerequisite", err)
		return
	}
	for _, task := range tasks {
		if task.TaskCode == "first_buddy" {
			r.progressTask(task)
			r.firstProcessed = true
			return
		}
	}
}

func (r *dailyTaskRunner) activity() {
	if r.completed("activity") {
		return
	}
	today := time.Now().In(quotaResetLocation).Format("2006-01-02")
	cells, err := r.client.GrowthHeatmap(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("activity", err)
		return
	}
	for _, cell := range cells {
		if cell.Date == today && cell.Score > 0 {
			r.report.Add("activity", "already", "CN daily activity was already present in upstream heatmap")
			return
		}
	}
	event, err := buildGrowthEvent(r.credential, "chat", 0)
	if err == nil {
		err = r.submitEvent(event)
	}
	if err != nil {
		r.result("activity", err)
		return
	}
	for _, delay := range growthReadbackDelays {
		if err := taskPause(r.ctx, delay); err != nil {
			r.result("activity", err)
			return
		}
		cells, err := r.client.GrowthHeatmap(r.ctx, r.id, r.credential)
		if err != nil {
			r.result("activity", err)
			return
		}
		for _, cell := range cells {
			if cell.Date == today && cell.Score > 0 {
				r.report.Add("activity", "success", "CN daily activity confirmed in upstream heatmap")
				return
			}
		}
	}
	r.report.Add("activity", "error", "activity event acknowledged but today's activity is not confirmed")
}

func (r *dailyTaskRunner) travel() {
	if r.completed("travel") {
		return
	}
	travel, err := r.client.TravelStatus(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("travel", err)
		return
	}
	switch travel.State {
	case "arrived":
		_, err = r.client.TravelClaim(r.ctx, r.id, r.credential, travel.RecordID)
		if err != nil {
			r.result("travel", err)
			return
		}
		fresh, err := r.client.TravelStatus(r.ctx, r.id, r.credential)
		if err != nil {
			r.result("travel", err)
			return
		}
		if fresh.State != "idle" && fresh.State != "traveling" {
			r.report.Add("travel", "error", "travel claim acknowledged but arrival reward remains unconfirmed")
			return
		}
		r.report.Add("travel", "success", "travel reward claimed and state confirmed")
	case "traveling":
		r.report.Add("travel", "skipped", "travel in progress")
	case "idle":
		if travel.DailyLimitReached {
			r.report.Add("travel", "skipped", "daily limit reached")
			return
		}
		locations, err := r.client.TravelConfig(r.ctx, r.id, r.credential)
		if err != nil {
			r.result("travel", err)
			return
		}
		if len(locations) == 0 {
			r.report.Add("travel", "error", "upstream returned no travel destinations")
			return
		}
		locationID := locations[0].ID
		for _, location := range locations {
			if location.ID == r.options.TravelLocationID {
				locationID = location.ID
				break
			}
		}
		if err := r.client.TravelDepart(r.ctx, r.id, r.credential, locationID); err != nil {
			r.result("travel", err)
			return
		}
		fresh, err := r.client.TravelStatus(r.ctx, r.id, r.credential)
		if err != nil {
			r.result("travel", err)
			return
		}
		if fresh.State != "traveling" && fresh.State != "arrived" {
			r.report.Add("travel", "error", "departure acknowledged but travel is not confirmed")
			return
		}
		r.report.Add("travel", "success", "travel departure confirmed")
	default:
		r.report.Add("travel", "error", "unknown travel state")
	}
}

func (r *dailyTaskRunner) streak() {
	state, err := r.client.GrowthStreakFull(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("streak", err)
		return
	}
	if !r.completed("makeup") {
		cells, err := r.client.GrowthHeatmap(r.ctx, r.id, r.credential)
		if err != nil {
			r.result("makeup", err)
		} else {
			yesterday := time.Now().In(quotaResetLocation).AddDate(0, 0, -1).Format("2006-01-02")
			missing := false
			for _, cell := range cells {
				if cell.Date == yesterday {
					missing = cell.Score == 0
					break
				}
			}
			if !missing || state.MakeupCards.Balance <= 0 {
				r.report.Add("makeup", "skipped", "no confirmed missed day with an available makeup card")
			} else if err := r.client.GrowthMakeup(r.ctx, r.id, r.credential, yesterday); err != nil {
				r.result("makeup", err)
			} else {
				fresh, err := r.client.GrowthHeatmap(r.ctx, r.id, r.credential)
				verified := false
				for _, cell := range fresh {
					if cell.Date == yesterday && cell.Score > 0 {
						verified = true
					}
				}
				if err != nil {
					r.result("makeup", err)
				} else if !verified {
					r.report.Add("makeup", "error", "makeup acknowledged but yesterday's activity is not confirmed")
				} else {
					r.report.Add("makeup", "success", "yesterday restored using one existing makeup card")
				}
				state, err = r.client.GrowthStreakFull(r.ctx, r.id, r.credential)
				if err != nil {
					r.result("streak", err)
					return
				}
			}
		}
	}
	for _, tier := range []string{"7d", "14d", "28d"} {
		name := "streak_" + tier
		if r.completed(name) {
			continue
		}
		switch state.TierStatus(tier) {
		case "claimed":
			r.report.Add(name, "already", "tier reward already claimed")
		case "locked":
			r.report.Add(name, "skipped", "tier locked")
		case "available", "claimable", "unlocked", "redeemable":
			if err := r.client.GrowthRedeemTier(r.ctx, r.id, r.credential, tier); err != nil {
				r.result(name, err)
				continue
			}
			fresh, err := r.client.GrowthStreakFull(r.ctx, r.id, r.credential)
			if err != nil {
				r.result(name, err)
			} else if fresh.TierStatus(tier) != "claimed" {
				r.report.Add(name, "error", "redemption acknowledged but tier is not confirmed claimed")
			} else {
				r.report.Add(name, "success", "tier redemption confirmed")
			}
		default:
			r.report.Add(name, "error", "tier status is unknown; redemption was not attempted")
		}
	}
	chances, err := r.client.LotteryChances(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("lottery", err)
		return
	}
	if chances <= 0 {
		r.report.Add("lottery", "skipped", "no existing lottery chances")
		return
	}
	drawn := 0
	for drawn < chances && drawn < 5 {
		if err := r.client.LotteryDraw(r.ctx, r.id, r.credential); err != nil {
			r.result("lottery", err)
			return
		}
		drawn++
	}
	remaining, err := r.client.LotteryChances(r.ctx, r.id, r.credential)
	if err != nil {
		r.result("lottery", err)
		return
	}
	if remaining > chances-drawn {
		r.report.Add("lottery", "error", "draw acknowledged but consumed chances are not confirmed")
		return
	}
	if remaining > 0 {
		r.report.Add("lottery", "error", fmt.Sprintf("confirmed %d existing chances consumed; %d deferred by the per-run draw limit", drawn, remaining))
		return
	}
	r.report.Add("lottery", "success", fmt.Sprintf("confirmed %d existing chances consumed; %d remaining", drawn, remaining))
}

func (c *Client) RunDailyTasksWithOptions(ctx context.Context, accountID string, opts DailyTasksOptions) (*DailyTasksReport, error) {
	// Scope is checked before token refresh, which is itself an upstream write.
	_, payload, err := c.store.LoadCredentialPayload(ctx, accountID)
	if err != nil {
		return nil, errors.New("workbuddy credential unavailable")
	}
	credential, err := DecodeCredential(payload)
	if err != nil {
		return nil, errors.New("workbuddy credential unavailable")
	}
	credential = c.overlayRegion(ctx, accountID, credential)
	report := &DailyTasksReport{AccountID: accountID, RanAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if credential.IsGlobal() || credential.EnterpriseID != "" {
		report.Add("growth", "skipped", "daily growth automation supports personal CN accounts only")
		return report, nil
	}
	credential, err = c.resolvedCredential(ctx, accountID)
	if err != nil {
		return nil, errors.New("workbuddy credential unavailable")
	}
	r := &dailyTaskRunner{client: c, ctx: ctx, id: accountID, credential: credential, options: opts, report: report}
	if !r.completed("checkin") {
		_, err := c.DailyCheckin(ctx, accountID)
		var already AlreadyCheckedInError
		if errors.As(err, &already) {
			report.Add("checkin", "already", "already checked in")
		} else if err != nil {
			r.result("checkin", err)
		} else {
			report.Add("checkin", "success", "checked in")
		}
	}
	r.activity()
	r.prerequisite()
	r.travel()
	if opts.ClaimGift {
		for _, step := range []struct {
			name  string
			claim func(context.Context, string, Credential) (int64, error)
		}{{"gift", c.ClaimGift}, {"compensation", c.ClaimCompensation}} {
			if r.completed(step.name) {
				continue
			}
			credit, err := step.claim(ctx, accountID, credential)
			status, message := taskResult(err)
			if err == nil {
				if credit > 0 {
					message = fmt.Sprintf("claimed %d credits", credit)
				} else {
					status, message = "skipped", "no reward reported"
				}
			}
			report.Add(step.name, status, message)
		}
	}
	if opts.RedeemTiers {
		r.streak()
	}
	// Core daily rewards run before the bounded, potentially slower growth batch.
	r.growth()
	return report, nil
}

func (c *Client) RunDailyTasksReport(ctx context.Context, accountID string, previous []providers.DailyTaskStep) (*DailyTasksReport, error) {
	return c.RunDailyTasksWithOptions(ctx, accountID, DailyTasksOptions{RedeemTiers: true, ClaimGift: true, Previous: previous})
}

func (c *Client) RunDailyTasks(ctx context.Context, accountID string) (string, error) {
	report, err := c.RunDailyTasksReport(ctx, accountID, nil)
	if err != nil {
		return "", err
	}
	if report.Status() == "error" || report.Status() == "partial" {
		return report.Summary(), fmt.Errorf("daily tasks %s: %s", report.Status(), report.Summary())
	}
	return report.Summary(), nil
}
