package trae

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

// AlreadyCheckedInError marks a claim business state that must still be
// confirmed by a status readback before returning "already".
type AlreadyCheckedInError struct {
	Msg string
}

func (e AlreadyCheckedInError) Error() string {
	if strings.TrimSpace(e.Msg) == "" {
		return "trae already checked in"
	}
	return e.Msg
}

func (AlreadyCheckedInError) AlreadyCheckedIn() bool { return true }

// Checkin claims the Trae daily credit grant via the ug checkin_credits API.
// Flow: refresh credential if needed → status probe → claim when eligible →
// re-probe so success is only reported when the account actually flipped.
// Account completion and device-only limits are distinct. Free-form upstream
// messages never establish success or eligibility.
func (client *Client) Checkin(ctx context.Context, accountID string) (providers.CheckinResult, error) {
	credential, err := client.credential(ctx, accountID)
	if err != nil {
		return providers.CheckinResult{}, err
	}
	st, err := client.checkinStatus(ctx, accountID, credential)
	if err != nil {
		return providers.CheckinResult{}, err
	}
	if st.checkedIn {
		return providers.CheckinResult{
			Status:  "already",
			Message: "already checked in; no new reward claimed",
		}, nil
	}
	if st.deviceCheckedIn {
		return providers.CheckinResult{Status: "skipped", Message: "device already checked in; no new reward claimed"}, nil
	}
	if !st.enable {
		return providers.CheckinResult{Status: "skipped", Message: "checkin disabled"}, nil
	}
	claimErr := client.checkinClaim(ctx, accountID, credential)
	var already AlreadyCheckedInError
	claimAlready := errors.As(claimErr, &already)
	if claimErr != nil && !claimAlready {
		return providers.CheckinResult{}, claimErr
	}
	// A successful HTTP response or accepted claim alone is not evidence that
	// today's grant registered. Confirm the state with the same credential.
	after, err := client.checkinStatus(ctx, accountID, credential)
	if err != nil {
		return providers.CheckinResult{}, err
	}
	if !after.checkedIn {
		return providers.CheckinResult{}, fmt.Errorf("trae checkin did not register; reward not confirmed")
	}
	if claimAlready {
		return providers.CheckinResult{Status: "already", Message: "already checked in; no new reward claimed"}, nil
	}
	return providers.CheckinResult{
		Status:        "success",
		Message:       "checkin claimed",
		RewardCredits: float64(after.credits + after.extraCredits),
	}, nil
}

type checkinState struct {
	checkedIn       bool
	deviceCheckedIn bool
	credits         int64
	extraCredits    int64
	enable          bool
}

// checkinStatus requires explicit upstream state. Missing fields and business
// failures must not become zero-value booleans or an already-claimed result.
func (client *Client) checkinStatus(ctx context.Context, accountID string, credential Credential) (checkinState, error) {
	body, err := client.CheckinStatus(ctx, accountID, credential)
	if err != nil {
		return checkinState{}, err
	}
	text := strings.TrimSpace(string(body))
	classified := Classify(200, text)
	if classified.Kind == accounts.KindAuth {
		return checkinState{}, fmt.Errorf("trae checkin session dead: re-login required")
	}
	if err := checkinBusinessCode(body, false); err != nil {
		return checkinState{}, err
	}
	var env struct {
		CheckedIn       *bool `json:"checked_in"`
		DeviceCheckedIn *bool `json:"did_checked_in"`
		Credits         int64 `json:"credits"`
		ExtraCredits    int64 `json:"extra_credits"`
		Enable          *bool `json:"enable"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return checkinState{}, fmt.Errorf("checkin status parse: %w", err)
	}
	if env.CheckedIn == nil || (!*env.CheckedIn && env.Enable == nil) {
		return checkinState{}, fmt.Errorf("trae checkin status incomplete; eligibility not confirmed")
	}
	if env.Credits < 0 || env.ExtraCredits < 0 || env.Credits > (1<<53)-1-env.ExtraCredits {
		return checkinState{}, fmt.Errorf("trae checkin status has invalid reward credits")
	}
	return checkinState{checkedIn: *env.CheckedIn, deviceCheckedIn: env.DeviceCheckedIn != nil && *env.DeviceCheckedIn, credits: env.Credits, extraCredits: env.ExtraCredits, enable: env.Enable != nil && *env.Enable}, nil
}

// checkinClaim posts the ug checkin_credits/claim request. The response is
// HTTP 200 with a business body {"code":0} on success (idempotent), or a
// non-zero code when the claim is refused. Never infer success or already from
// free text; accepted claims still require a subsequent status confirmation.
func (client *Client) checkinClaim(ctx context.Context, accountID string, credential Credential) error {
	body, err := client.CheckinClaim(ctx, accountID, credential)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(body))
	classified := Classify(200, text)
	if classified.Kind == accounts.KindAuth {
		return fmt.Errorf("trae checkin session dead: re-login required")
	}
	return checkinBusinessCode(body, true)
}

// Every response must explicitly acknowledge its business code. The claim
// endpoint's documented already code still requires a separate state readback.
func checkinBusinessCode(body []byte, claim bool) error {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil || env == nil {
		return fmt.Errorf("trae checkin response is not a JSON object")
	}
	raw, exists := env["code"]
	if !exists || string(raw) == "null" {
		return fmt.Errorf("trae checkin response missing business code")
	}
	var code int64
	if err := json.Unmarshal(raw, &code); err != nil {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return fmt.Errorf("trae checkin response has invalid business code")
		}
		parsed, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			return fmt.Errorf("trae checkin response has invalid business code")
		}
		code = parsed
	}
	if code != 0 {
		if claim && code == 9095 {
			return AlreadyCheckedInError{Msg: "already checked in; upstream state confirmation required"}
		}
		// Do not persist upstream bodies/messages, which can contain credentials.
		return fmt.Errorf("trae checkin rejected (code %d); reward not confirmed", code)
	}
	return nil
}

func checkinHTTPError(status int, body []byte) error {
	classified := Classify(status, string(body))
	if classified.Kind == "" {
		classified.Kind = accounts.KindUnavailable
	}
	return &providers.Error{
		Kind: classified.Kind, Status: status,
		Message: fmt.Sprintf("trae checkin request failed (HTTP %d); reward not confirmed", status),
	}
}
