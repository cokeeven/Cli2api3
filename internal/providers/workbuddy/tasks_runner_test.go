package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestDailyTasksReportsFailuresAndContinues(t *testing.T) {
	c, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any = map[string]any{}
		switch r.URL.Path {
		case pathDailyCheckin:
		case pathGrowthReport:
		case pathHeatmap:
			data = map[string]any{"cells": []any{map[string]any{"date": time.Now().In(quotaResetLocation).Format("2006-01-02"), "score": 1}}}
		case pathGrowthTasksV2:
			data = map[string]any{"tasks": []any{map[string]any{"task_code": "one", "target": 1, "current": 1}}}
		case pathGrowthTasksAccept, pathGrowthTasksClaim + "one/claim", pathClaimGift:
			w.WriteHeader(500)
			_, _ = w.Write([]byte("private-token-must-not-leak"))
			return
		case pathTravelStatus:
			data = map[string]any{"state": "traveling"}
		case pathStreak:
			data = map[string]any{"streak": map[string]any{"days": 28}, "makeup_cards": map[string]any{"balance": 0}, "redemption_status": map[string]any{"tier_7d_status": "available", "tier_14d_status": "available", "tier_28d_status": "available"}}
		case pathStreakRedeem:
			w.WriteHeader(http.StatusInternalServerError)
			return
		case pathLotterySummary:
			data = map[string]any{"chances": 0}
		case pathClaimCompensation:
			data = map[string]any{"credit": 3}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	payload, _ := Credential{AccessToken: "test", UID: "u1", Domain: DomainCN, ExpiresAt: 4102444800}.Encode()
	store.items = map[string][]byte{"acc1": payload}
	report, err := c.RunDailyTasksReport(context.Background(), "acc1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status() != "partial" {
		t.Fatalf("status=%s report=%+v", report.Status(), report)
	}
	want := map[string]string{"checkin": "success", "activity": "already", "growth:one": "error", "travel": "skipped", "makeup": "skipped", "streak_7d": "error", "streak_14d": "error", "streak_28d": "error", "lottery": "skipped", "gift": "error", "compensation": "success"}
	for _, step := range report.Steps {
		if step.Status != want[step.Step] {
			t.Errorf("step=%+v", step)
		}
		delete(want, step.Step)
		if strings.Contains(step.Message, "private-token") {
			t.Fatal("upstream body leaked")
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing results: %v", want)
	}
	if _, err := c.RunDailyTasks(context.Background(), "acc1"); err == nil {
		t.Fatal("legacy caller must receive partial failure")
	}
}

func TestDailyTasksResumeConfirmedStepsAndClassifySkips(t *testing.T) {
	c, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathGrowthTasksV2:
			_, _ = w.Write([]byte(`{"code":0,"data":{"tasks":[{"task_code":"one","status":"claimed"}]}}`))
		case pathStreak:
			_, _ = w.Write([]byte(`{"code":0,"data":{"streak":{"days":0},"makeup_cards":{"balance":0},"redemption_status":{}}}`))
		case pathLotterySummary:
			_, _ = w.Write([]byte(`{"code":0,"data":{"chances":0}}`))
		case pathClaimGift:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 8, "msg": "already claimed"})
		case pathClaimCompensation:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 9, "msg": "not eligible"})
		default:
			t.Errorf("confirmed step called again: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	payload, _ := Credential{AccessToken: "test", UID: "u1", Domain: DomainCN, ExpiresAt: 4102444800}.Encode()
	store.items = map[string][]byte{"acc1": payload}
	var previous []providers.DailyTaskStep
	for _, name := range []string{"checkin", "activity", "growth:one", "travel", "makeup", "streak_7d", "streak_14d", "streak_28d"} {
		previous = append(previous, providers.DailyTaskStep{Step: name, Status: "success", OK: true})
	}
	report, err := c.RunDailyTasksReport(context.Background(), "acc1", previous)
	if err != nil || report.Status() != "success" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if stepStatus(report, "gift") != "already" || stepStatus(report, "compensation") != "skipped" {
		t.Fatalf("steps=%+v", report.Steps)
	}
}

func TestTaskBusinessErrorsNeverBecomeFalseSuccess(t *testing.T) {
	for _, tc := range []struct{ message, status string }{{"already claimed", "already"}, {"未解锁", "skipped"}, {"session expired", "error"}, {"unknown failure", "error"}} {
		status, _ := taskResult(&growthBusinessError{code: 9, message: tc.message})
		if status != tc.status {
			t.Errorf("%s: %s", tc.message, status)
		}
	}
}

func TestGrowthAcceptanceRequiresPerTaskConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, response, state string
		wantError, wantRead   bool
	}{
		{"accepted", `{"results":[{"task_code":"a","status":"accepted"},{"task_code":"b","status":"already_accepted"}]}`, "", false, false},
		{"partial rejection", `{"results":[{"task_code":"a","status":"accepted"},{"task_code":"b","status":"failed","message":"private-token"}]}`, "", true, false},
		{"missing task", `{"results":[{"task_code":"a","status":"accepted"}]}`, "", true, false},
		{"empty results", `{"results":[]}`, "", true, false},
		{"conflicting duplicate", `{"results":[{"task_code":"a","status":"failed"},{"task_code":"a","status":"accepted"},{"task_code":"b","status":"accepted"}]}`, "", true, false},
		{"legacy confirmed", `{}`, `{"tasks":[{"task_code":"a","accept_status":"accepted"},{"task_code":"b","accept_status":"claimed"}]}`, false, true},
		{"legacy unconfirmed", `{}`, `{"tasks":[{"task_code":"a","accept_status":"accepted"},{"task_code":"b","accept_status":"not_accepted"}]}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var read atomic.Bool
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := tc.response
				switch r.URL.Path {
				case pathGrowthTasksAccept:
				case pathGrowthTasksV2:
					read.Store(true)
					data = tc.state
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"code":0,"data":` + data + `}`))
			}))
			err := c.AcceptGrowthTasks(context.Background(), "acc1", Credential{AccessToken: "fixture", UID: "u1", Domain: DomainCN}, []string{"a", "b"})
			if (err != nil) != tc.wantError || read.Load() != tc.wantRead {
				t.Fatalf("error=%v read=%v", err, read.Load())
			}
			if err != nil && strings.Contains(err.Error(), "private-token") {
				t.Fatal("acceptance business message leaked")
			}
		})
	}
}

func TestDailyTasksParsesNestedProgressAndLockedRewards(t *testing.T) {
	var tasks []GrowthTask
	err := json.Unmarshal([]byte(`[
	{"task_code":"nested","progress":{"current":2,"target":2},"reward_credit":100,"accept_status":"accepted"},
	{"task_code":"claimed","current":2,"target":2,"accept_status":"claimed"},
	{"task_code":"locked","progress":{"current":0,"target":1},"locked":true}
	]`), &tasks)
	if err != nil {
		t.Fatal(err)
	}
	if !tasks[0].Claimable || tasks[0].Credit != 100 || !tasks[1].Claimed || tasks[1].Claimable || !tasks[2].Locked || tasks[2].Claimable {
		t.Fatalf("tasks=%+v", tasks)
	}
}

func TestDailyTasksGlobalSkipsCNGrowthAndLockedTierIsSkipped(t *testing.T) {
	c, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathStreakRedeem {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"msg":"连续登录天数不足"}`))
			return
		}
		if r.URL.Path != pathDailyCheckin {
			t.Errorf("global growth call: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
	}))
	store.region = "global"
	credential := Credential{AccessToken: "fixture", UID: "u1", Domain: DomainGlobal, ExpiresAt: 4102444800}
	payload, _ := credential.Encode()
	store.items = map[string][]byte{"acc1": payload}
	report, err := c.RunDailyTasksReport(context.Background(), "acc1", nil)
	if err != nil || report.Status() != "skipped" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	for _, step := range report.Steps {
		if step.Step != "checkin" && step.Status != "skipped" {
			t.Errorf("step=%+v", step)
		}
	}
	status, _ := taskResult(c.GrowthRedeemTier(context.Background(), "acc1", credential, "7d"))
	if status != "skipped" {
		t.Fatalf("locked tier=%s", status)
	}
}
