package control

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func (a *Accounts) ListMaintenanceLogs(ctx context.Context, filter accounts.MaintenanceLogFilter) (accounts.MaintenanceLogPage, error) {
	store, ok := a.store().(accounts.MaintenanceLogStore)
	if !ok {
		return accounts.MaintenanceLogPage{}, operationError("logs_unavailable", "maintenance logs unavailable")
	}
	page, err := store.ListMaintenanceLogs(ctx, filter)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		item := &page.Items[i]
		item.Summary = safeMaintenanceMessage(item.Summary)
		for j := range item.Steps {
			item.Steps[j].Message = safeMaintenanceMessage(item.Steps[j].Message)
		}
	}
	return page, nil
}

var maintenanceSensitiveText = regexp.MustCompile(`(?i)(authorization|bearer\s|cookie|token|api[\s_-]?key|auth[\s_-]?blob|password|secret|https?://|<html|eyJ[A-Za-z0-9_-]{10,}|sk-[A-Za-z0-9_-]{8,})`)

// Older records may contain provider errors with request URLs, raw response
// bodies, or credentials. Preserve them in storage but publish a safe message.
func safeMaintenanceMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	if len(trimmed) > 1000 || maintenanceSensitiveText.MatchString(trimmed) ||
		(strings.Contains(trimmed, "{") && strings.Contains(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && json.Valid([]byte(trimmed))) {
		return "Sensitive upstream details omitted"
	}
	return message
}
