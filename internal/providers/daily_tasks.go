package providers

import (
	"context"
	"fmt"
	"strings"
)

// DailyTaskStep carries provider facts, without HTTP or scheduling policy.
type DailyTaskStep struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	OK      bool   `json:"ok"`
	Reused  bool   `json:"reused,omitempty"`
	Message string `json:"message,omitempty"`
}

type DailyTasksReport struct {
	AccountID string          `json:"account_id"`
	RanAt     string          `json:"ran_at"`
	Steps     []DailyTaskStep `json:"steps"`
}

func (r *DailyTasksReport) Add(step, status, message string) {
	r.Steps = append(r.Steps, DailyTaskStep{Step: step, Status: status, OK: status == "success" || status == "already", Message: message})
}

func (r *DailyTasksReport) Reuse(step DailyTaskStep) {
	step.Status = "already"
	step.OK = true
	step.Reused = true
	step.Message = "completed earlier today"
	r.Steps = append(r.Steps, step)
}

func (r *DailyTasksReport) Status() string {
	good, bad := 0, 0
	for _, step := range r.Steps {
		switch step.Status {
		case "success", "already":
			good++
		case "skipped":
		default:
			bad++
		}
	}
	if bad > 0 {
		if good > 0 {
			return "partial"
		}
		return "error"
	}
	if good == 0 {
		return "skipped"
	}
	return "success"
}

func (r *DailyTasksReport) Succeeded() int {
	n := 0
	for _, step := range r.Steps {
		if step.OK {
			n++
		}
	}
	return n
}

func (r *DailyTasksReport) Summary() string {
	parts := make([]string, 0, len(r.Steps))
	for _, step := range r.Steps {
		parts = append(parts, fmt.Sprintf("%s: %s", step.Step, step.Status))
	}
	return strings.Join(parts, "; ")
}

// Previous contains confirmed steps for this account and local date only.
type DailyTasksRunner interface {
	RunDailyTasksReport(context.Context, string, []DailyTaskStep) (*DailyTasksReport, error)
}
