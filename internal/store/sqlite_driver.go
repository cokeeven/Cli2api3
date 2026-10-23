package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const sqliteDefaultBusyTimeout = 5000

type sqliteBusyWaitLimitKey struct{}

// sqliteDataSource applies connection-local settings for every physical
// connection. PRAGMA statements issued once through *sql.DB only affect the
// first connection and are lost when database/sql replaces it.
func sqliteDataSource(path string) (string, error) {
	base := ""
	if path == ":memory:" {
		base = "file::memory:"
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve sqlite path: %w", err)
		}
		p := filepath.ToSlash(absolute)
		u := url.URL{Scheme: "file"}
		if strings.HasPrefix(p, "//") {
			parts := strings.SplitN(strings.TrimPrefix(p, "//"), "/", 2)
			if len(parts) != 2 || parts[0] == "" {
				return "", fmt.Errorf("invalid sqlite UNC path")
			}
			u.Host = parts[0]
			u.Path = "/" + parts[1]
		} else {
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			u.Path = p
		}
		base = u.String()
	}
	query := url.Values{}
	query.Set("_busy_timeout", strconv.Itoa(sqliteDefaultBusyTimeout))
	query.Add("_pragma", "foreign_keys=ON")
	return base + "?" + query.Encode(), nil
}

type contextSQLiteConnector struct {
	driver.Connector
}

func (c contextSQLiteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &contextSQLiteConn{Conn: conn, busyTimeout: sqliteDefaultBusyTimeout}, nil
}

type contextSQLiteConn struct {
	driver.Conn
	busyTimeout int
}

func (c *contextSQLiteConn) setBusyTimeout(milliseconds int) error {
	if c.busyTimeout == milliseconds {
		return nil
	}
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return fmt.Errorf("sqlite driver connection does not support contextual execution")
	}
	_, err := execer.ExecContext(context.Background(), "PRAGMA busy_timeout = "+strconv.Itoa(milliseconds), nil)
	if err == nil {
		c.busyTimeout = milliseconds
	}
	return err
}

func sqliteBusyTimeoutForContext(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	limit := sqliteDefaultBusyTimeout
	if waitLimit, ok := ctx.Value(sqliteBusyWaitLimitKey{}).(time.Duration); ok && waitLimit > 0 {
		milliseconds := int((waitLimit + time.Millisecond - 1) / time.Millisecond)
		if milliseconds < limit {
			limit = milliseconds
		}
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, context.DeadlineExceeded
		}
		milliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
		if milliseconds < limit {
			limit = milliseconds
		}
	}
	if limit < 1 {
		limit = 1
	}
	return limit, nil
}

func (c *contextSQLiteConn) withContextBusyTimeout(ctx context.Context, run func() error) error {
	milliseconds, err := sqliteBusyTimeoutForContext(ctx)
	if err != nil {
		return err
	}
	if err := c.setBusyTimeout(milliseconds); err != nil {
		return err
	}
	runErr := run()
	if c.busyTimeout != sqliteDefaultBusyTimeout {
		if restoreErr := c.setBusyTimeout(sqliteDefaultBusyTimeout); runErr == nil && restoreErr != nil {
			runErr = restoreErr
		}
	}
	if runErr != nil && ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return runErr
}

func (c *contextSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, errors.New("sqlite driver connection does not support contextual execution")
	}
	var result driver.Result
	err := c.withContextBusyTimeout(ctx, func() error {
		var err error
		result, err = execer.ExecContext(ctx, query, args)
		return err
	})
	return result, err
}

func (c *contextSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("sqlite driver connection does not support contextual queries")
	}
	var rows driver.Rows
	err := c.withContextBusyTimeout(ctx, func() error {
		var err error
		rows, err = queryer.QueryContext(ctx, query, args)
		return err
	})
	return rows, err
}

func (c *contextSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("sqlite driver connection does not support contextual transactions")
	}
	var tx driver.Tx
	err := c.withContextBusyTimeout(ctx, func() error {
		var err error
		tx, err = beginner.BeginTx(ctx, opts)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &contextSQLiteTx{Tx: tx, conn: c, ctx: ctx}, nil
}

func (c *contextSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &contextSQLiteStmt{Stmt: stmt, conn: c}, nil
}

func (c *contextSQLiteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var stmt driver.Stmt
	err := c.withContextBusyTimeout(ctx, func() error {
		if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
			var err error
			stmt, err = preparer.PrepareContext(ctx, query)
			return err
		}
		var err error
		stmt, err = c.Conn.Prepare(query)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &contextSQLiteStmt{Stmt: stmt, conn: c}, nil
}

func (c *contextSQLiteConn) CheckNamedValue(value *driver.NamedValue) error {
	checker, ok := c.Conn.(driver.NamedValueChecker)
	if !ok {
		return driver.ErrSkip
	}
	return checker.CheckNamedValue(value)
}

func (c *contextSQLiteConn) Ping(ctx context.Context) error {
	pinger, ok := c.Conn.(driver.Pinger)
	if !ok {
		return nil
	}
	return c.withContextBusyTimeout(ctx, func() error { return pinger.Ping(ctx) })
}

func (c *contextSQLiteConn) ResetSession(ctx context.Context) error {
	resetter, ok := c.Conn.(driver.SessionResetter)
	if !ok {
		return nil
	}
	return resetter.ResetSession(ctx)
}

func (c *contextSQLiteConn) IsValid() bool {
	validator, ok := c.Conn.(driver.Validator)
	return !ok || validator.IsValid()
}

type contextSQLiteTx struct {
	driver.Tx
	conn *contextSQLiteConn
	ctx  context.Context
}

func (tx *contextSQLiteTx) Commit() error {
	if tx.ctx != nil && tx.ctx.Err() != nil {
		_ = tx.Tx.Rollback()
		return tx.ctx.Err()
	}
	return tx.conn.withContextBusyTimeout(tx.ctx, tx.Tx.Commit)
}

func (tx *contextSQLiteTx) Rollback() error {
	return tx.Tx.Rollback()
}

type contextSQLiteStmt struct {
	driver.Stmt
	conn *contextSQLiteConn
}

func (s *contextSQLiteStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return s.ExecContext(context.Background(), named)
}

func (s *contextSQLiteStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return s.QueryContext(context.Background(), named)
}

func (s *contextSQLiteStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		var result driver.Result
		err := s.conn.withContextBusyTimeout(ctx, func() error {
			var err error
			result, err = execer.ExecContext(ctx, args)
			return err
		})
		return result, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	var result driver.Result
	err := s.conn.withContextBusyTimeout(ctx, func() error {
		var err error
		result, err = s.Stmt.Exec(values)
		return err
	})
	return result, err
}

func (s *contextSQLiteStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		var rows driver.Rows
		err := s.conn.withContextBusyTimeout(ctx, func() error {
			var err error
			rows, err = queryer.QueryContext(ctx, args)
			return err
		})
		return rows, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	var rows driver.Rows
	err := s.conn.withContextBusyTimeout(ctx, func() error {
		var err error
		rows, err = s.Stmt.Query(values)
		return err
	})
	return rows, err
}

func (s *contextSQLiteStmt) CheckNamedValue(value *driver.NamedValue) error {
	checker, ok := s.Stmt.(driver.NamedValueChecker)
	if !ok {
		return driver.ErrSkip
	}
	return checker.CheckNamedValue(value)
}
