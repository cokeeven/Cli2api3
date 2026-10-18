package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const MaintenanceScheduleSecret = "maintenance_schedule"

type MaintenanceSchedule struct {
	CredentialEnabled         bool   `json:"credential_enabled"`
	CredentialIntervalMinutes int    `json:"credential_interval_minutes"`
	QuotaEnabled              bool   `json:"quota_enabled"`
	DailyEnabled              bool   `json:"daily_enabled"`
	DailyTime                 string `json:"daily_time"`
}

func DefaultMaintenanceSchedule() MaintenanceSchedule {
	return MaintenanceSchedule{true, 15, true, true, "22:00"}
}
func (s MaintenanceSchedule) Validate() error {
	if s.CredentialIntervalMinutes < 5 || s.CredentialIntervalMinutes > 1440 {
		return fmt.Errorf("credential interval must be between 5 and 1440 minutes")
	}
	if _, err := time.Parse("15:04", s.DailyTime); err != nil {
		return fmt.Errorf("daily time must be HH:mm")
	}
	return nil
}
func LoadMaintenanceSchedule(ctx context.Context, reader CheckinSettingsReader) (MaintenanceSchedule, error) {
	s := DefaultMaintenanceSchedule()
	raw, found, err := reader.GetSecret(ctx, MaintenanceScheduleSecret)
	if err != nil {
		return s, err
	}
	if found {
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return s, fmt.Errorf("invalid maintenance schedule")
		}
	}
	return s, s.Validate()
}

// MaintenanceTaskStore extends the existing account-wide daily-task lease and
// history. Periodic jobs do not resume reward/check-in steps from other kinds.
type MaintenanceTaskStore interface {
	ClaimMaintenanceTaskRun(context.Context, string, string, string, time.Time, time.Duration) (DailyTaskRun, error)
	FinishDailyTaskRun(context.Context, DailyTaskRun) error
}
