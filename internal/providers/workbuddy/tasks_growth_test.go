package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type growthInspectTransport struct {
	next  http.RoundTripper
	check func(*http.Request)
}

func (tr growthInspectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.check(r)
	return tr.next.RoundTrip(r)
}

func TestCNGrowthEndpointContracts(t *testing.T) {
	ctx := context.Background()
	credential := Credential{AccessToken: "fixture", UID: "u1", Domain: DomainCN, DeviceToken: "fixture-device"}
	for _, tc := range []struct {
		name, method, host, path, body, data string
		call                                 func(*Client) error
	}{
		{"tasks", "GET", "copilot.tencent.com", "/v2/activity/growth/tasks", "", `{"tasks":[]}`, func(c *Client) error { _, err := c.ListGrowthTasks(ctx, "acc1", credential); return err }},
		{"accept", "POST", "copilot.tencent.com", "/v2/activity/growth/tasks/accept", `{"task_codes":["first_buddy"]}`, `{"results":[{"task_code":"first_buddy","status":"accepted"}]}`, func(c *Client) error { return c.AcceptGrowthTasks(ctx, "acc1", credential, []string{"first_buddy"}) }},
		{"claim", "POST", "copilot.tencent.com", "/activity/growth/tasks/first_buddy/claim", `{}`, `{}`, func(c *Client) error { return c.ClaimGrowthTask(ctx, "acc1", credential, "first_buddy") }},
		{"gift", "POST", "www.codebuddy.cn", "/billing/meter/claim-gift", `{}`, `{"credit":100}`, func(c *Client) error { _, err := c.ClaimGift(ctx, "acc1", credential); return err }},
		{"compensation", "POST", "www.codebuddy.cn", "/billing/meter/claim-compensation", `{}`, `{"credit":10}`, func(c *Client) error { _, err := c.ClaimCompensation(ctx, "acc1", credential); return err }},
		{"travel claim", "POST", "copilot.tencent.com", "/activity/growth/buddy/travel/claim", `{}`, `{"reward_credit":3}`, func(c *Client) error { _, err := c.TravelClaim(ctx, "acc1", credential, 123); return err }},
		{"makeup", "POST", "copilot.tencent.com", "/activity/growth/makeup-cards/use", `{"target_date":"2026-10-14"}`, `{}`, func(c *Client) error { return c.GrowthMakeup(ctx, "acc1", credential, "2026-10-14") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != tc.body {
					t.Errorf("body=%s want=%s", body, tc.body)
				}
				if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-User-Id") != "u1" || r.Header.Get("X-Device-Token") != "fixture-device" || r.Header.Get("X-Refresh-Token") != "" {
					t.Error("missing identity/device or unexpected refresh credential")
				}
				if tc.host == "copilot.tencent.com" && r.Header.Get("X-IDE-Type") != productTypeCLI {
					t.Error("chat host missing chat identity")
				}
				_, _ = fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.data)
			}))
			c.http.Transport = growthInspectTransport{next: c.http.Transport, check: func(r *http.Request) {
				if r.URL.Host != tc.host {
					t.Errorf("host=%s want=%s", r.URL.Host, tc.host)
				}
			}}
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestCNGrowthTypedState(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]string{
			pathStreak:         `{"streak":{"days":14},"makeup_cards":{"balance":2},"redemption_status":{"tiers":[{"tier":"7d"},{"tier":"14d"}],"tier_7d_status":"claimed","tier_14d_status":"claimable","tier_28d_status":"locked"}}`,
			pathHeatmap:        `{"cells":[{"date":"2026-10-14","score":0}]}`,
			pathLotterySummary: `{"chances":3}`,
			pathTravelConfig:   `{"locations":[{"id":4,"name":"island"}]}`,
		}[r.URL.Path]
		if data == "" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":%s}`, data)
	}))
	ctx, cred := context.Background(), Credential{Domain: DomainCN}
	state, err := c.GrowthStreakFull(ctx, "acc1", cred)
	if err != nil || state.Streak.Days != 14 || state.MakeupCards.Balance != 2 || state.TierStatus("7d") != "claimed" || state.TierStatus("14d") != "claimable" || state.TierStatus("28d") != "locked" || len(state.RedemptionStatus.Tiers) != 2 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	cells, err := c.GrowthHeatmap(ctx, "acc1", cred)
	if err != nil || len(cells) != 1 || cells[0].Date != "2026-10-14" || cells[0].Score != 0 {
		t.Fatalf("heatmap=%+v err=%v", cells, err)
	}
	chances, err := c.LotteryChances(ctx, "acc1", cred)
	if err != nil || chances != 3 {
		t.Fatalf("chances=%d err=%v", chances, err)
	}
	locations, err := c.TravelConfig(ctx, "acc1", cred)
	if err != nil || len(locations) != 1 || locations[0].ID != 4 {
		t.Fatalf("locations=%+v err=%v", locations, err)
	}
}

func TestCNGrowthRejectsUnconfirmedResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{200, `{}`}, {200, `{"data":{"tasks":[]}}`}, {200, `{"code":0,"data":{}}`}, {200, `{"code":0,"data":null}`},
		{200, `{"code":12153,"msg":"private-token"}`}, {401, `private-token`}, {429, `{"code":9,"msg":"already claimed private-token"}`},
		{500, `{"code":9,"msg":"already claimed private-token"}`}, {403, `private-token already claimed`},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.status, tc.body), func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			_, err := c.ListGrowthTasks(context.Background(), "acc1", Credential{Domain: DomainCN})
			if err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("unsafe or absent error: %v", err)
			}
			status, _ := taskResult(err)
			if status != "error" {
				t.Fatalf("failure became %s", status)
			}
		})
	}
}

func TestCNGrowthAcceptanceBatchesAndStopsAfterRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		calls := 0
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var body struct {
				Codes []string `json:"task_codes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Codes) > 20 {
				t.Fatalf("invalid batch")
			}
			results := make([]map[string]string, 0, len(body.Codes))
			for _, code := range body.Codes {
				results = append(results, map[string]string{"task_code": code, "status": "accepted"})
			}
			if reject {
				results[0]["status"] = "rejected"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"results": results}})
		}))
		codes := make([]string, 41)
		for i := range codes {
			codes[i] = fmt.Sprintf("task_%d", i)
		}
		err := c.AcceptGrowthTasks(context.Background(), "acc1", Credential{Domain: DomainCN}, codes)
		if reject && (err == nil || calls != 1) || !reject && (err != nil || calls != 3) {
			t.Fatalf("reject=%v calls=%d err=%v", reject, calls, err)
		}
	}
}

func TestCNGrowthFailureKindsOverrideBusinessSkipText(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, kind string
	}{
		{200, `{"code":401,"msg":"already claimed private-token"}`, accounts.KindAuth},
		{403, `{"code":403,"msg":"already claimed; token expired private-token"}`, accounts.KindAuth},
		{429, `{"code":9,"msg":"already claimed private-token"}`, accounts.KindRateLimit},
		{503, `{"code":9,"msg":"not eligible private-token"}`, accounts.KindUnavailable},
	} {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, err := c.ClaimGift(context.Background(), "acc1", Credential{Domain: DomainCN})
		var classified *ClassifiedError
		if !errors.As(err, &classified) || classified.Kind != tc.kind || strings.Contains(err.Error(), "private-token") {
			t.Fatalf("status=%d kind=%v err=%v", tc.status, classified, err)
		}
	}
}

func TestCNGrowthRejectsIncompleteStateBeforeActions(t *testing.T) {
	ctx, credential := context.Background(), Credential{Domain: DomainCN}
	for _, tc := range []struct {
		name, data string
		call       func(*Client) error
	}{
		{"heatmap missing score", `{"cells":[{"date":"2026-10-14"}]}`, func(c *Client) error { _, err := c.GrowthHeatmap(ctx, "acc1", credential); return err }},
		{"heatmap invalid date", `{"cells":[{"date":"yesterday","score":0}]}`, func(c *Client) error { _, err := c.GrowthHeatmap(ctx, "acc1", credential); return err }},
		{"streak missing state", `{}`, func(c *Client) error { _, err := c.GrowthStreakFull(ctx, "acc1", credential); return err }},
		{"streak null state", `{"streak":null,"makeup_cards":{},"redemption_status":{}}`, func(c *Client) error { _, err := c.GrowthStreakFull(ctx, "acc1", credential); return err }},
		{"lottery missing chances", `{}`, func(c *Client) error { _, err := c.LotteryChances(ctx, "acc1", credential); return err }},
		{"lottery negative chances", `{"chances":-1}`, func(c *Client) error { _, err := c.LotteryChances(ctx, "acc1", credential); return err }},
		{"travel invalid location", `{"locations":[{"id":0}]}`, func(c *Client) error { _, err := c.TravelConfig(ctx, "acc1", credential); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.data) }))
			if err := tc.call(c); err == nil {
				t.Fatal("incomplete state was accepted")
			}
		})
	}
}

func TestCNGrowthAcceptanceReadbackConfirmsInProgressWithoutClaimingCompletion(t *testing.T) {
	var task GrowthTask
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := `{}`
		if r.URL.Path == pathGrowthTasksV2 {
			data = `{"tasks":[{"task_code":"chat_5","accept_status":"in_progress","progress":{"current":1,"target":5}}]}`
		} else if r.URL.Path != pathGrowthTasksAccept {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":%s}`, data)
	}))
	ctx, credential := context.Background(), Credential{Domain: DomainCN}
	if err := c.AcceptGrowthTasks(ctx, "acc1", credential, []string{"chat_5"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := c.ListGrowthTasks(ctx, "acc1", credential)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	task = tasks[0]
	if task.AcceptStatus != "in_progress" || task.Current != 1 || task.Target != 5 || task.Claimable || task.Claimed {
		t.Fatalf("acceptance status incorrectly implies completion: %+v", task)
	}
}
