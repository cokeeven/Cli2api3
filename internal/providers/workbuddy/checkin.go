package workbuddy

import (
	"context"
	"errors"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func (client *Client) Checkin(ctx context.Context, accountID string) (providers.CheckinResult, error) {
	ctx, trace := providers.WithCheckinTrace(ctx)
	_, err := client.DailyCheckin(ctx, accountID)
	var already AlreadyCheckedInError
	if errors.As(err, &already) {
		return providers.CheckinResult{Status: "already", Message: trace.Summary("already")}, nil
	}
	if err != nil {
		missing := trace.Phase == "credential" && (errors.Is(err, accounts.ErrAccountNotFound) || errors.Is(err, accounts.ErrSecretNotFound) || errors.Is(err, providers.ErrCredentialMissing))
		return trace.Failure(err, missing)
	}
	return providers.CheckinResult{Status: "success", Message: trace.Summary("success")}, nil
}
