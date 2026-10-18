package accounts

import (
	"context"
	"testing"
)

type quotaSettingsStub struct {
	value string
	found bool
	err   error
}

func (s quotaSettingsStub) GetSecret(context.Context, string) (string, bool, error) {
	return s.value, s.found, s.err
}

func TestQuotaRefreshIntervalMinutesDefaultsAndValidates(t *testing.T) {
	ctx := context.Background()
	got, err := QuotaRefreshIntervalMinutes(ctx, quotaSettingsStub{})
	if err != nil || got != DefaultQuotaRefreshIntervalMinutes {
		t.Fatalf("default interval = %d, err=%v", got, err)
	}
	got, err = QuotaRefreshIntervalMinutes(ctx, quotaSettingsStub{value: "45", found: true})
	if err != nil || got != 45 {
		t.Fatalf("configured interval = %d, err=%v", got, err)
	}
	for _, value := range []string{"0", "4", "1441", "not-a-number"} {
		if _, err := QuotaRefreshIntervalMinutes(ctx, quotaSettingsStub{value: value, found: true}); err == nil {
			t.Errorf("interval %q accepted, want validation error", value)
		}
	}
}
