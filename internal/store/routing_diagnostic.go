package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func (s *Store) UpdateRequestRoutingDiagnostic(ctx context.Context, diagnostic accounts.RequestRoutingDiagnostic) error {
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		return err
	}
	result, err := execSQLiteBusyRetry(ctx, s.db, `UPDATE request_logs SET routing_diagnostic_json = ? WHERE id = ?`, string(encoded), diagnostic.RequestID)
	if err != nil {
		return fmt.Errorf("update routing diagnostic: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return accounts.ErrRequestLogNotFound
	}
	return nil
}

func (s *Store) requestRoutingDiagnostic(ctx context.Context, requestID string) (*accounts.RequestRoutingDiagnostic, error) {
	var raw sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT routing_diagnostic_json FROM request_logs WHERE id = ?`, requestID).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var diagnostic accounts.RequestRoutingDiagnostic
	if err := json.Unmarshal([]byte(raw.String), &diagnostic); err != nil {
		return nil, fmt.Errorf("decode routing diagnostic: %w", err)
	}
	return &diagnostic, nil
}
