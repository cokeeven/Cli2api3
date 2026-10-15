package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func fixtureTaskRunner(t *testing.T, handler http.HandlerFunc) *dailyTaskRunner {
	t.Helper()
	client, _ := newTestClient(t, handler)
	return &dailyTaskRunner{client: client, ctx: context.Background(), id: "acc1", credential: Credential{AccessToken: "fixture", UID: "u1", Domain: DomainCN}, report: &DailyTasksReport{AccountID: "acc1"}}
}

func noGrowthWait(t *testing.T) {
	t.Helper()
	previous := growthReadbackDelays
	previousInterval := growthEventInterval
	growthReadbackDelays = []time.Duration{0, 0, 0}
	growthEventInterval = 0
	t.Cleanup(func() { growthReadbackDelays = previous; growthEventInterval = previousInterval })
}

func replyGrowth(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func stepStatus(report *DailyTasksReport, name string) string {
	for _, step := range report.Steps {
		if step.Step == name {
			return step.Status
		}
	}
	return ""
}

func TestCNGrowthFirstBuddyPrerequisiteAndReadback(t *testing.T) {
	noGrowthWait(t)
	firstClaimed, singleAccepted, singleComplete, singleClaimed := false, false, false, false
	var order []string
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		var data any = map[string]any{}
		switch request.URL.Path {
		case pathGrowthTasksV2:
			firstStatus, singleStatus, singleCurrent := "completed", "not_accepted", 0
			if firstClaimed {
				firstStatus = "claimed"
			}
			if singleAccepted {
				singleStatus = "accepted"
			}
			if singleComplete {
				singleCurrent = 1
			}
			if singleClaimed {
				singleStatus = "claimed"
			}
			data = map[string]any{"tasks": []any{
				map[string]any{"task_code": "first_buddy", "task_type": "auto", "status": firstStatus, "progress": map[string]int{"current": 1, "target": 1}},
				map[string]any{"task_code": "create_canvas", "task_type": "single", "accept_status": singleStatus, "status": singleStatus, "progress": map[string]int{"current": singleCurrent, "target": 1}},
				map[string]any{"task_code": "Library_read", "task_type": "single", "accept_status": "not_accepted", "progress": map[string]int{"current": 0, "target": 1}},
				map[string]any{"task_code": "Buddy_App", "task_type": "single", "accept_status": "accepted", "progress": map[string]int{"current": 1, "target": 1}},
			}}
		case pathGrowthTasksClaim + "first_buddy/claim":
			firstClaimed = true
			order = append(order, "first claim")
		case pathGrowthTasksAccept:
			if !firstClaimed {
				t.Error("single accepted before first Buddy reward")
			}
			var body struct {
				Codes []string `json:"task_codes"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if len(body.Codes) != 1 || body.Codes[0] != "create_canvas" {
				t.Errorf("accept=%v", body.Codes)
			}
			singleAccepted = true
			order = append(order, "accept")
			data = map[string]any{"results": []any{map[string]any{"task_code": "create_canvas", "status": "accepted"}}}
		case pathGrowthReport:
			if !singleAccepted {
				t.Error("progress sent before confirmed acceptance")
			}
			var events []map[string]any
			_ = json.NewDecoder(request.Body).Decode(&events)
			if len(events) != 1 || events[0]["eventCode"] != "wbx_design_canvas_task_create" || events[0]["userId"] != "u1" {
				t.Errorf("events=%v", events)
			}
			singleComplete = true
			order = append(order, "progress")
		case pathGrowthTasksClaim + "create_canvas/claim":
			if !singleComplete {
				t.Error("claimed incomplete task")
			}
			singleClaimed = true
			order = append(order, "single claim")
		case pathGrowthTasksClaim + "Buddy_App/claim":
			// A desktop-only task already completed by a real user may be claimed.
			data = nil
			w.WriteHeader(http.StatusInternalServerError)
			return
		default:
			t.Errorf("unexpected path=%s", request.URL.Path)
		}
		replyGrowth(w, data)
	})
	runner.growth()
	if strings.Join(order, ",") != "first claim,accept,progress,single claim" {
		t.Fatalf("order=%v", order)
	}
	for name, status := range map[string]string{"growth:first_buddy": "success", "growth:create_canvas": "success", "growth:Library_read": "skipped", "growth:Buddy_App": "error"} {
		if got := stepStatus(runner.report, name); got != status {
			t.Errorf("%s=%s wanted=%s report=%+v", name, got, status, runner.report)
		}
	}
}

func TestCNGrowthUnconfirmedProgressNeverClaims(t *testing.T) {
	noGrowthWait(t)
	for _, acceptance := range []string{"accepted", "in_progress", "not_accepted"} {
		t.Run(acceptance, func(t *testing.T) {
			events, claims := 0, 0
			runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case pathGrowthTasksV2:
					replyGrowth(w, map[string]any{"tasks": []any{map[string]any{"task_code": "create_canvas", "accept_status": acceptance, "target": 1, "current": 0}}})
				case pathGrowthTasksAccept:
					replyGrowth(w, map[string]any{"results": []any{map[string]any{"task_code": "create_canvas", "status": "accepted"}}})
				case pathGrowthReport:
					events++
					replyGrowth(w, map[string]any{})
				default:
					claims++
					t.Errorf("unconfirmed task mutation=%s", request.URL.Path)
					w.WriteHeader(500)
				}
			})
			runner.options.Previous = []providers.DailyTaskStep{{Step: "growth", Status: "success", OK: true}}
			runner.growth()
			if stepStatus(runner.report, "growth:create_canvas") != "error" || claims != 0 || acceptance == "not_accepted" && events != 0 {
				t.Fatalf("events=%d claims=%d report=%+v", events, claims, runner.report)
			}
			if acceptance == "in_progress" && events != 1 {
				t.Fatalf("in-progress task did not advance: events=%d", events)
			}
		})
	}
}

func TestCNGrowthResumeIsPerTask(t *testing.T) {
	noGrowthWait(t)
	claims := 0
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == pathGrowthTasksV2 {
			status := "completed"
			if claims > 0 {
				status = "claimed"
			}
			replyGrowth(w, map[string]any{"tasks": []any{map[string]any{"task_code": "first_buddy", "status": "claimed"}, map[string]any{"task_code": "chat_5", "status": status, "target": 1, "current": 1}}})
		} else if request.URL.Path == pathGrowthTasksClaim+"chat_5/claim" {
			claims++
			replyGrowth(w, map[string]any{})
		} else {
			t.Errorf("unexpected %s", request.URL.Path)
		}
	})
	runner.options.Previous = []providers.DailyTaskStep{{Step: "growth:first_buddy", Status: "success", OK: true}, {Step: "growth", Status: "success", OK: true}}
	runner.growth()
	if claims != 1 || stepStatus(runner.report, "growth:chat_5") != "success" || !runner.report.Steps[0].Reused {
		t.Fatalf("claims=%d report=%+v", claims, runner.report)
	}
}

func TestCNGrowthActivityRequiresTodayReadbackAndCancellation(t *testing.T) {
	noGrowthWait(t)
	events := 0
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == pathGrowthReport {
			events++
			replyGrowth(w, map[string]any{})
		} else if request.URL.Path == pathHeatmap {
			replyGrowth(w, map[string]any{"cells": []any{map[string]any{"date": time.Now().In(quotaResetLocation).Format("2006-01-02"), "score": 0}}})
		} else {
			t.Errorf("unexpected %s", request.URL.Path)
		}
	})
	runner.activity()
	if events != 1 || stepStatus(runner.report, "activity") != "error" {
		t.Fatalf("report=%+v", runner.report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.ctx = ctx
	runner.report = &DailyTasksReport{}
	runner.progressTask(GrowthTask{TaskCode: "first_buddy", TaskType: "auto", Target: 1})
	if events != 1 || stepStatus(runner.report, "growth:first_buddy") != "error" {
		t.Fatalf("report=%+v events=%d", runner.report, events)
	}
}

func TestCNGrowthStreakMakeupRedeemAndBoundedLottery(t *testing.T) {
	noGrowthWait(t)
	yesterday := time.Now().In(quotaResetLocation).AddDate(0, 0, -1).Format("2006-01-02")
	makeups, redeems, draws := 0, 0, 0
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case pathStreak:
			status := "available"
			if redeems > 0 {
				status = "claimed"
			}
			replyGrowth(w, map[string]any{"streak": map[string]int{"days": 7}, "makeup_cards": map[string]int{"balance": 1 - makeups}, "redemption_status": map[string]any{"tier_7d_status": status, "tier_14d_status": "locked", "tier_28d_status": "claimed"}})
		case pathHeatmap:
			replyGrowth(w, map[string]any{"cells": []any{map[string]any{"date": yesterday, "score": makeups}}})
		case pathMakeup:
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["target_date"] != yesterday {
				t.Errorf("date=%v", body)
			}
			makeups++
			replyGrowth(w, map[string]any{})
		case pathStreakRedeem:
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["tier"] != "7d" {
				t.Errorf("locked/claimed tier redeemed: %v", body)
			}
			redeems++
			replyGrowth(w, map[string]any{})
		case pathLotterySummary:
			replyGrowth(w, map[string]any{"chances": 9 - draws})
		case pathLotteryDraw:
			draws++
			replyGrowth(w, map[string]any{})
		default:
			t.Errorf("unexpected path=%s", request.URL.Path)
		}
	})
	runner.streak()
	if makeups != 1 || redeems != 1 || draws != 5 {
		t.Fatalf("makeups=%d redeems=%d draws=%d", makeups, redeems, draws)
	}
	for name, status := range map[string]string{"makeup": "success", "streak_7d": "success", "streak_14d": "skipped", "streak_28d": "already", "lottery": "error"} {
		if got := stepStatus(runner.report, name); got != status {
			t.Errorf("%s=%s report=%+v", name, got, runner.report)
		}
	}
}

func TestCNGrowthStreakMissingDayAndCardGate(t *testing.T) {
	for _, cards := range []int{0, 1} {
		t.Run(fmtInt(cards), func(t *testing.T) {
			runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case pathStreak:
					replyGrowth(w, map[string]any{"streak": map[string]int{"days": 1}, "makeup_cards": map[string]int{"balance": cards}, "redemption_status": map[string]string{"tier_7d_status": "locked", "tier_14d_status": "locked", "tier_28d_status": "locked"}})
				case pathHeatmap:
					replyGrowth(w, map[string]any{"cells": []any{}})
				case pathLotterySummary:
					replyGrowth(w, map[string]int{"chances": 0})
				default:
					t.Errorf("no evidence for mutation: %s", request.URL.Path)
				}
			})
			runner.streak()
			if stepStatus(runner.report, "makeup") != "skipped" {
				t.Fatalf("report=%+v", runner.report)
			}
		})
	}
}

func fmtInt(value int) string {
	if value == 0 {
		return "without cards"
	}
	return "without yesterday cell"
}

func TestCNGrowthTravelUsesConfiguredDestinationAndReadback(t *testing.T) {
	departed := false
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case pathTravelStatus:
			state := "idle"
			if departed {
				state = "traveling"
			}
			replyGrowth(w, map[string]any{"state": state})
		case pathTravelConfig:
			replyGrowth(w, map[string]any{"locations": []any{map[string]any{"id": 42, "name": "upstream destination"}}})
		case pathTravelDepart:
			var body map[string]int
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["location_id"] != 42 {
				t.Errorf("location=%v", body)
			}
			departed = true
			replyGrowth(w, map[string]any{})
		default:
			t.Errorf("unexpected %s", request.URL.Path)
		}
	})
	runner.options.TravelLocationID = 999
	runner.travel()
	if !departed || stepStatus(runner.report, "travel") != "success" {
		t.Fatalf("report=%+v", runner.report)
	}
}

func TestCNGrowthEventShapeAndPersonalCNGate(t *testing.T) {
	credential := Credential{UID: "u1", Domain: DomainCN}
	for _, kind := range growthEventKinds {
		event, err := buildGrowthEvent(credential, kind, 0)
		if err != nil || event["userId"] != "u1" || event["eventCode"] == "" || event["requestId"] == "" {
			t.Fatalf("kind=%s event=%v err=%v", kind, event, err)
		}
	}
	for _, cred := range []Credential{{UID: "u1", Domain: DomainGlobal}, {UID: "u1", Domain: DomainCN, EnterpriseID: "enterprise"}, {Domain: DomainCN}} {
		if _, err := buildGrowthEvent(cred, "chat", 0); err == nil {
			t.Fatal("nonpersonal CN event accepted")
		}
	}
	if _, err := buildGrowthEvent(credential, "team", -1); err == nil {
		t.Fatal("negative progress accepted")
	}
}

func TestCNGrowthEventIntervalHonorsCancellation(t *testing.T) {
	runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
		t.Error("event submitted after cancellation while waiting")
	})
	previous := growthEventInterval
	growthEventInterval = time.Hour
	t.Cleanup(func() { growthEventInterval = previous })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	runner.ctx = ctx
	runner.lastEventAt = time.Now()
	started := time.Now()
	if err := runner.submitEvent(map[string]any{}); err == nil || time.Since(started) > time.Second || runner.events != 0 {
		t.Fatalf("cancellation err=%v elapsed=%s events=%d", err, time.Since(started), runner.events)
	}
}

func TestCNGrowthCoreRewardsRunBeforeGrowthBudget(t *testing.T) {
	noGrowthWait(t)
	var order []string
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var data any = map[string]any{}
		switch request.URL.Path {
		case pathDailyCheckin:
		case pathHeatmap:
			data = map[string]any{"cells": []any{map[string]any{"date": time.Now().In(quotaResetLocation).Format("2006-01-02"), "score": 1}}}
		case pathGrowthTasksV2:
			order = append(order, "tasks")
			data = map[string]any{"tasks": []any{map[string]any{"task_code": "first_buddy", "status": "claimed", "task_type": "auto"}, map[string]any{"task_code": "create_canvas", "accept_status": "accepted", "current": 0, "target": 9}}}
		case pathTravelStatus:
			order = append(order, "travel")
			data = map[string]any{"state": "traveling"}
		case pathClaimGift, pathClaimCompensation:
			order = append(order, "gift")
			data = map[string]int{"credit": 1}
		case pathStreak:
			order = append(order, "streak")
			data = map[string]any{"streak": map[string]int{"days": 1}, "makeup_cards": map[string]int{"balance": 0}, "redemption_status": map[string]string{"tier_7d_status": "locked", "tier_14d_status": "locked", "tier_28d_status": "locked"}}
		case pathLotterySummary:
			data = map[string]int{"chances": 0}
		default:
			t.Errorf("unexpected mutation %s", request.URL.Path)
		}
		replyGrowth(w, data)
	}))
	payload, _ := Credential{AccessToken: "fixture", UID: "u1", Domain: DomainCN, ExpiresAt: 4102444800}.Encode()
	store.items = map[string][]byte{"acc1": payload}
	report, err := client.RunDailyTasksReport(context.Background(), "acc1", nil)
	if err != nil || stepStatus(report, "growth:create_canvas") != "error" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := strings.Join(order, ","); got != "tasks,travel,gift,gift,streak,tasks,tasks" {
		t.Fatalf("daily phases=%s", got)
	}
}

func TestCNGrowthExpiredExcludedAccountsNeverRefresh(t *testing.T) {
	for _, tc := range []struct{ name, region, domain, enterprise string }{
		{"global credential", "global", DomainGlobal, ""},
		{"global region overlay", "global", DomainCN, ""},
		{"enterprise", "cn", DomainCN, "e1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				t.Errorf("excluded account upstream request: %s", request.URL.Path)
			}))
			store.region = tc.region
			payload, _ := Credential{AccessToken: "expired-fixture", RefreshToken: "fixture-refresh", UID: "u1", Domain: tc.domain, EnterpriseID: tc.enterprise, ExpiresAt: 1}.Encode()
			store.items = map[string][]byte{"acc1": payload}
			report, err := client.RunDailyTasksReport(context.Background(), "acc1", nil)
			if err != nil || report.Status() != "skipped" || report.Succeeded() != 0 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestCNGrowthAlreadyClaimedRequiresReadback(t *testing.T) {
	noGrowthWait(t)
	for _, tc := range []struct {
		name, state, want string
		readFailure       bool
	}{
		{"confirmed", "claimed", "already", false},
		{"contradictory state", "completed", "error", false},
		{"read failed", "", "error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			runner := fixtureTaskRunner(t, func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case pathGrowthTasksClaim + "chat_5/claim":
					_, _ = w.Write([]byte(`{"code":9,"msg":"already claimed"}`))
				case pathGrowthTasksV2:
					reads++
					if tc.readFailure {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					replyGrowth(w, map[string]any{"tasks": []any{map[string]any{"task_code": "chat_5", "status": tc.state, "current": 5, "target": 5}}})
				default:
					t.Errorf("unexpected %s", request.URL.Path)
				}
			})
			runner.claimTask(GrowthTask{TaskCode: "chat_5", Current: 5, Target: 5})
			if reads == 0 || stepStatus(runner.report, "growth:chat_5") != tc.want {
				t.Fatalf("reads=%d report=%+v", reads, runner.report)
			}
		})
	}
}
