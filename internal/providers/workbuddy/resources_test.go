package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestResourcePagesAndDeductionExpiry(t *testing.T) {
	calls := 0
	now := time.Now()
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ PageNumber int }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		calls++
		var packs []map[string]any
		if input.PageNumber == 1 {
			for i := 0; i < 100; i++ {
				packs = append(packs, map[string]any{
					"ResourceId": i + 1, "CapacityRemain": 2, "CapacitySize": 2,
					"CycleEndTime":     now.Add(-24 * time.Hour).In(cycleEndTimeZone).Format("2006-01-02 15:04:05"),
					"DeductionEndTime": now.Add(365 * 24 * time.Hour).UnixMilli(),
				})
			}
		} else {
			packs = []map[string]any{
				{"ResourceId": 101, "CapacityRemain": 7, "CapacitySize": 7, "DeductionEndTime": now.Add(-time.Hour).UnixMilli()},
				{"ResourceId": 102, "CapacityRemain": 3, "CapacitySize": 3, "DeductionEndTime": now.Add(24 * time.Hour).UnixMilli(), "AccountAttributes": []map[string]string{{"Key": "grantReason", "Value": "checkin"}}},
				{"ResourceId": 103, "CapacityRemain": 50, "CapacitySize": 50, "Status": 9},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"Response": map[string]any{"Data": map[string]any{"TotalCount": 103, "Accounts": packs}}}})
	}))
	remaining, _, total, packs, err := client.UserResource(context.Background(), "acc1", Credential{AccessToken: "at", UID: "u1", Domain: DomainCN})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(packs) != 103 || remaining != 203 || total != 203 {
		t.Fatalf("calls=%d packs=%d remain=%d total=%d", calls, len(packs), remaining, total)
	}
	if packs[0].ID != "1" || packs[0].ExpirySource != "deduction" || packs[0].Status != "active" || packs[100].Status != "expired" || packs[101].Source != "checkin" || packs[102].Status != "inactive" {
		t.Fatalf("package metadata incorrect")
	}
	end, amount := soonestExpiry(packs)
	if end != now.Add(24*time.Hour).Unix() || amount != 3 {
		t.Fatalf("expiry=%d amount=%v", end, amount)
	}
}

func TestRepeatedResourcePageDoesNotReturnPartialBalance(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		packs := make([]map[string]any, 100)
		for i := range packs {
			packs[i] = map[string]any{"ResourceId": i + 1, "CapacityRemain": 1, "CapacitySize": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"Response": map[string]any{"Data": map[string]any{"Accounts": packs}}}})
	}))
	r, _, _, packs, err := client.UserResource(context.Background(), "acc1", Credential{AccessToken: "at", UID: "u1", Domain: DomainCN})
	if err == nil || r != 0 || packs != nil {
		t.Fatalf("partial snapshot returned: remain=%d err=%v", r, err)
	}
}
