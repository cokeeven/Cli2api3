package runtime

import (
	"testing"
	"time"
)

func TestCheckin9074AndMissingCredentialsDoNotAutomaticallyRetry(t *testing.T) {
	now := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	for _, kind := range []string{"upstream_rejected_9074", "credential_missing"} {
		a := Account{ID: "test", Enabled: true, AutoCheckin: true, LastCheckinStatus: "error", LastCheckinMsg: "outcome=" + kind + "; phase=status; attempts=1", LastCheckinAt: now.Add(-3 * time.Hour).Format(time.RFC3339Nano)}
		if checkinDue(a, "09:00", now) {
			t.Fatalf("%s retried", kind)
		}
	}
}
