package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type CheckinPolicy struct {
	Timezone string `json:"timezone"`
}

type CheckinResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	// Nil means the amount actually credited is unknown. A non-nil pointer
	// represents a confirmed amount, including an explicitly confirmed zero.
	RewardCredits *float64 `json:"reward_credits,omitempty"`
}

func (result CheckinResult) Valid() bool {
	return result.Status == "success" || result.Status == "already" || result.Status == "skipped"
}

type AccountCheckiner interface {
	Checkin(ctx context.Context, accountID string) (CheckinResult, error)
}

func CheckinFor(providerID, regionID string) (*CheckinPolicy, bool) {
	_, region, err := Resolve(providerID, regionID)
	return region.Checkin, err == nil && region.Checkin != nil
}

func (descriptor ProviderDescriptor) SupportsCheckin() bool {
	for _, region := range descriptor.Regions {
		if region.Checkin != nil {
			return true
		}
	}
	return false
}

// CheckinTrace stores only allowlisted numeric/enum evidence for one run.
type CheckinTrace struct {
	Phase        string
	HTTPStatus   int
	BusinessCode *int64
	Attempts     int
	Network      bool
}
type checkinTraceKey struct{}

func WithCheckinTrace(ctx context.Context) (context.Context, *CheckinTrace) {
	t := &CheckinTrace{Phase: "credential"}
	return context.WithValue(ctx, checkinTraceKey{}, t), t
}
func RecordCheckinResponse(ctx context.Context, phase string, status int, body []byte, err error) {
	t, _ := ctx.Value(checkinTraceKey{}).(*CheckinTrace)
	if t == nil {
		return
	}
	t.Phase = phase
	t.Attempts++
	t.HTTPStatus = status
	t.BusinessCode = nil
	var e net.Error
	t.Network = errors.As(err, &e) || errors.Is(err, context.DeadlineExceeded)
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) == nil {
		raw := obj["code"]
		var code int64
		if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &code) == nil {
			t.BusinessCode = &code
		} else {
			var str string
			if json.Unmarshal(raw, &str) == nil {
				if v, e := strconv.ParseInt(str, 10, 64); e == nil {
					t.BusinessCode = &v
				}
			}
		}
	}
}
func (t *CheckinTrace) Summary(outcome string) string {
	fields := []string{"outcome=" + outcome, "phase=" + t.Phase, fmt.Sprintf("attempts=%d", t.Attempts)}
	if t.HTTPStatus > 0 {
		fields = append(fields, fmt.Sprintf("http=%d", t.HTTPStatus))
	}
	if t.BusinessCode != nil {
		fields = append(fields, fmt.Sprintf("code=%d", *t.BusinessCode))
	}
	fields = append(fields, "reward=unconfirmed")
	return strings.Join(fields, "; ")
}

// CheckinFailure keeps a safe public description while retaining the cause for
// cancellation and scheduling decisions.
type CheckinFailure struct {
	Summary string
	Cause   error
}

func (e *CheckinFailure) Error() string { return e.Summary }
func (e *CheckinFailure) Unwrap() error { return e.Cause }
func (t *CheckinTrace) Failure(err error, missing bool) (CheckinResult, error) {
	outcome := "unconfirmed"
	switch {
	case t.BusinessCode != nil && *t.BusinessCode == 9074:
		outcome = "upstream_rejected_9074"
	case missing:
		outcome = "credential_missing"
	case t.Network:
		outcome = "network_error"
	case t.HTTPStatus >= 400 || (t.BusinessCode != nil && *t.BusinessCode != 0):
		outcome = "upstream_rejected"
	}
	msg := t.Summary(outcome)
	return CheckinResult{Status: "error", Message: msg}, &CheckinFailure{Summary: msg, Cause: err}
}
