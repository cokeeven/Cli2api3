package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlitedriver "modernc.org/sqlite"
)

const (
	sqliteWriteRetryBudget = 5 * time.Second
	sqliteBusyWaitPerTry   = 25 * time.Millisecond
	sqliteRetryMaxAttempts = 20
)

type sqliteContextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// execSQLiteBusyRetry is limited to idempotent single-statement writes or
// statements within an already-open transaction. It never retries arbitrary
// errors or replays a whole transaction.
func execSQLiteBusyRetry(ctx context.Context, execer sqliteContextExecer, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := retrySQLiteBusy(ctx, func(attemptCtx context.Context) error {
		var err error
		result, err = execer.ExecContext(attemptCtx, query, args...)
		return err
	})
	return result, err
}

func retrySQLiteBusy(ctx context.Context, operation func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	deadline := time.Now().Add(sqliteWriteRetryBudget)
	delay := 10 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= sqliteRetryMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx := context.WithValue(ctx, sqliteBusyWaitLimitKey{}, sqliteBusyWaitPerTry)
		lastErr = operation(attemptCtx)
		if lastErr == nil || !isSQLiteBusyLocked(lastErr) {
			return lastErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if attempt == sqliteRetryMaxAttempts || remaining <= 0 {
			return fmt.Errorf("sqlite busy/locked retry exhausted after %s (%d attempts; budget %s): %w", time.Since(started).Round(time.Millisecond), attempt, sqliteWriteRetryBudget, lastErr)
		}
		if delay > remaining {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 250*time.Millisecond {
			delay *= 2
			if delay > 250*time.Millisecond {
				delay = 250 * time.Millisecond
			}
		}
	}
	return fmt.Errorf("sqlite busy/locked retry exhausted after %s (budget %s): %w", time.Since(started).Round(time.Millisecond), sqliteWriteRetryBudget, lastErr)
}

func isSQLiteBusyLocked(err error) bool {
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	// SQLite extended result codes retain the primary result in the low byte.
	switch sqliteErr.Code() & 0xff {
	case 5: // SQLITE_BUSY
		return true
	case 6: // SQLITE_LOCKED
		return true
	default:
		return false
	}
}
