package control

import (
	"context"
	"fmt"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type Usage struct{ store accounts.UsagePolicyStore }

func NewUsage(store accounts.AccountStore) *Usage {
	u, ok := store.(accounts.UsagePolicyStore)
	if !ok {
		return nil
	}
	return &Usage{store: u}
}
func (u *Usage) Policy(ctx context.Context, id string) (accounts.UsagePolicy, error) {
	if u == nil {
		return accounts.UsagePolicy{}, fmt.Errorf("usage policies unavailable")
	}
	return u.store.GetUsagePolicy(ctx, id)
}
func (u *Usage) SetPolicy(ctx context.Context, id string, p accounts.UsagePolicy) error {
	if u == nil {
		return fmt.Errorf("usage policies unavailable")
	}
	return u.store.SetUsagePolicy(ctx, id, p)
}
func (u *Usage) Analysis(ctx context.Context, id string, days int) (accounts.UsageAnalysis, error) {
	if u == nil {
		return accounts.UsageAnalysis{}, fmt.Errorf("usage analysis unavailable")
	}
	return u.store.GetUsageAnalysis(ctx, id, days)
}

func (u *Usage) ReconcileBalance(ctx context.Context, id, snapshot string) (int64, error) {
	if u == nil {
		return 0, fmt.Errorf("usage unavailable")
	}
	return u.store.ReconcileUsageBalance(ctx, id, snapshot)
}
