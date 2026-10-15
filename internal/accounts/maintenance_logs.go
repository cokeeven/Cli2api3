package accounts

import (
	"context"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

type maintenanceTriggerKey struct{}

// WithMaintenanceTrigger carries operation provenance without changing account settings.
func WithMaintenanceTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, maintenanceTriggerKey{}, trigger)
}

func MaintenanceTrigger(ctx context.Context) string {
	trigger, _ := ctx.Value(maintenanceTriggerKey{}).(string)
	switch trigger {
	case "manual", "scheduled", "retry":
		return trigger
	default:
		return "unknown"
	}
}

type MaintenanceLogFilter struct {
	AccountID string
	Provider  string
	Region    string
	Kind      string
	Trigger   string
	Status    string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

type MaintenanceLog struct {
	ID          string                    `json:"id"`
	AccountID   string                    `json:"account_id"`
	AccountName string                    `json:"account_name"`
	Provider    string                    `json:"provider"`
	Region      string                    `json:"region"`
	Kind        string                    `json:"kind"`
	Trigger     string                    `json:"trigger"`
	Status      string                    `json:"status"`
	Summary     string                    `json:"summary"`
	CreatedAt   time.Time                 `json:"created_at"`
	FinishedAt  *time.Time                `json:"finished_at,omitempty"`
	NextRetryAt *time.Time                `json:"next_retry_at,omitempty"`
	Attempt     int                       `json:"attempt"`
	Steps       []providers.DailyTaskStep `json:"steps"`
}

type MaintenanceLogPage struct {
	Items  []MaintenanceLog `json:"items"`
	Total  int              `json:"total"`
	Count  int              `json:"count"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

type MaintenanceLogStore interface {
	ListMaintenanceLogs(context.Context, MaintenanceLogFilter) (MaintenanceLogPage, error)
}

// CheckinAttemptStore appends a local no-op result without replacing the last
// actual check-in or changing any account configuration.
type CheckinAttemptStore interface {
	RecordCheckinAttempt(context.Context, string, string, string, time.Time) error
}
