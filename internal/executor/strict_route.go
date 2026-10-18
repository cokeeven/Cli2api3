package executor

import (
	"context"
	"strings"
)

type strictAccountContextKey struct{}

// WithStrictAccount restricts a single gateway request to the named account.
// It is intended for an explicitly selected diagnostic request, never for
// ordinary routed traffic.
func WithStrictAccount(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, strictAccountContextKey{}, strings.TrimSpace(accountID))
}

func strictAccountFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(strictAccountContextKey{}).(string)
	return strings.TrimSpace(value)
}
