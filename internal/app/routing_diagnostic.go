package app

import (
	"context"
	"sort"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// Disabled accounts are intentionally absent from the execution pool. Add
// their configured status at the app boundary without admitting them to it.
func (a *App) recordRoutingDiagnostic(d accounts.RequestRoutingDiagnostic) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	configured, err := a.Manager.Store().List(ctx)
	if err == nil {
		seen := map[string]bool{}
		for _, row := range d.Accounts {
			seen[row.AccountID] = true
		}
		for _, account := range configured {
			if seen[account.ID] {
				continue
			}
			reason := "not_in_runtime_pool"
			if !account.Enabled {
				reason = "disabled"
			}
			d.Accounts = append(d.Accounts, accounts.RouteAccountDiagnostic{AccountID: account.ID, Provider: account.Provider, Region: account.ProviderRegion, Reason: reason})
		}
		d.AccountsComplete = true
	}
	sort.Slice(d.Accounts, func(i, j int) bool { return d.Accounts[i].AccountID < d.Accounts[j].AccountID })
	a.Recorder.RoutingDiagnostic(d)
}
