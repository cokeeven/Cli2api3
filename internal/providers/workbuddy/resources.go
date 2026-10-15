package workbuddy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

func resourceScalar(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func resourceDeductionEnd(raw json.RawMessage) int64 {
	value, err := strconv.ParseFloat(resourceScalar(raw), 64)
	if err != nil || value <= 0 {
		return 0
	}
	if value > 1e11 {
		value /= 1000
	}
	if value > 253402300799 {
		return 0
	}
	return int64(value)
}

// Bound pagination and detect repeated full pages. An incomplete snapshot must
// not replace the last known balance or weaken an enabled reserve guard.
func (c *Client) userResourcePages(ctx context.Context, accountID string, credential Credential) (remain, used, total int64, packages []providers.QuotaPackage, err error) {
	now := time.Now().In(cycleEndTimeZone)
	var all []resourcePackage
	seenPages := map[[32]byte]bool{}
	seenIDs := map[string]bool{}
	for page := 1; page <= 20; page++ {
		payload, _ := json.Marshal(map[string]any{
			"PageNumber": page, "PageSize": 100, "ProductCode": "p_tcaca", "Status": []int{0, 3},
			"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
			"PackageEndTimeRangeEnd":   now.AddDate(101, 0, 0).Format("2006-01-02 15:04:05"),
		})
		body, status, reqErr := c.do(ctx, accountID, http.MethodPost, credential.BillingBase()+pathUserResource, payload,
			func(h http.Header) { SetBillingHeaders(h, credential) })
		if reqErr != nil {
			return 0, 0, 0, nil, reqErr
		}
		if status >= 300 {
			return 0, 0, 0, nil, fmt.Errorf("user-resource status=%d", status)
		}
		var env struct {
			Code int `json:"code"`
			Data struct {
				Response struct {
					Data struct {
						TotalCount *int              `json:"TotalCount"`
						Accounts   []resourcePackage `json:"Accounts"`
					} `json:"Data"`
				} `json:"Response"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &env) != nil {
			return 0, 0, 0, nil, fmt.Errorf("user-resource invalid response")
		}
		if env.Code != 0 {
			return 0, 0, 0, nil, fmt.Errorf("user-resource rejected code=%d", env.Code)
		}
		data := env.Data.Response.Data
		if data.Accounts == nil {
			return 0, 0, 0, nil, fmt.Errorf("user-resource missing accounts")
		}
		encoded, _ := json.Marshal(data.Accounts)
		digest := sha256.Sum256(encoded)
		if len(data.Accounts) > 0 && seenPages[digest] {
			return 0, 0, 0, nil, fmt.Errorf("user-resource repeated page")
		}
		seenPages[digest] = true
		for _, pkg := range data.Accounts {
			id := resourceScalar(pkg.ResourceID)
			if id != "" && seenIDs[id] {
				continue
			}
			if id != "" {
				seenIDs[id] = true
			}
			all = append(all, pkg)
		}
		if len(data.Accounts) < 100 && (data.TotalCount == nil || *data.TotalCount <= len(all)) || data.TotalCount != nil && *data.TotalCount > 0 && len(all) >= *data.TotalCount {
			packages = quotaPackages(all)
			for _, pkg := range packages {
				if pkg.Status != "active" && pkg.Status != "exhausted" {
					continue
				}
				remain += int64(pkg.Remain)
				used += int64(pkg.Used)
				total += int64(pkg.Size)
			}
			return remain, used, total, packages, nil
		}
	}
	return 0, 0, 0, nil, fmt.Errorf("user-resource pagination limit reached")
}
