package sqlite

import (
	"context"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) RuntimeDiagnostics(ctx context.Context, organizationID string, now time.Time) (core.RuntimeDiagnostics, error) {
	var result core.RuntimeDiagnostics
	err := s.db.QueryRowContext(ctx, `
SELECT
    COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0)
FROM runs
WHERE organization_id = ?`, organizationID).Scan(&result.QueuedRuns, &result.RunningRuns)
	if err != nil {
		return core.RuntimeDiagnostics{}, err
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM approval_requests
WHERE organization_id = ? AND status = 'pending'`, organizationID).Scan(&result.WaitingApprovals)
	if err != nil {
		return core.RuntimeDiagnostics{}, err
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM steps
WHERE organization_id = ? AND status = 'running' AND lease_until >= ?`,
		organizationID, now.Format(time.RFC3339Nano)).Scan(&result.ActiveLeases)
	if err != nil {
		return core.RuntimeDiagnostics{}, err
	}
	return result, nil
}
