package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
)

var ErrDailyTasksBusy = errors.New("daily tasks are already running for this account")
var ErrDailyTasksNotDue = errors.New("daily tasks are not due")
var ErrDailyTasksLeaseLost = errors.New("daily task execution lease expired or was replaced")

type DailyTaskRun struct {
	Kind        string                     `json:"kind"`
	ID          string                     `json:"id"`
	AccountID   string                     `json:"account_id"`
	RunDate     string                     `json:"run_date"`
	Trigger     string                     `json:"trigger"`
	Status      string                     `json:"status"`
	Attempt     int                        `json:"attempt"`
	StartedAt   time.Time                  `json:"started_at"`
	FinishedAt  *time.Time                 `json:"finished_at,omitempty"`
	NextRetryAt *time.Time                 `json:"next_retry_at,omitempty"`
	Report      providers.DailyTasksReport `json:"report"`
}

// DailyTaskStore is separate from AccountStore to keep existing stores narrow.
// Claims coordinate processes sharing this database, never a global file/path.
type DailyTaskStore interface {
	ClaimDailyTaskRun(context.Context, string, string, string, time.Time, time.Duration) (DailyTaskRun, error)
	FinishDailyTaskRun(context.Context, DailyTaskRun) error
	ListDailyTaskRuns(context.Context, string, int) ([]DailyTaskRun, error)
}
