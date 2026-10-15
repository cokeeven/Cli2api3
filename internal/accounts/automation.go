package accounts

import (
	"context"
	"fmt"
	"strings"
)

const AutomationMaintenanceEnabledSecret = "automation_maintenance_enabled"

// AutomationMaintenanceEnabled defaults to the pre-switch behavior only when
// the setting is absent. Unreadable or corrupt settings stop automatic work.
func AutomationMaintenanceEnabled(ctx context.Context, store CheckinSettingsReader) (bool, error) {
	value, found, err := store.GetSecret(ctx, AutomationMaintenanceEnabledSecret)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid automatic maintenance setting")
	}
}
