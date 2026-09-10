package sqlite

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// SetLocalOwner is deliberately absent from the HTTP application repository.
// Deployment operators select an existing login-capable active account.
func (s *Store) SetLocalOwner(ctx context.Context, actor core.Scope, username string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE organizations SET name = name WHERE id = ?`, actor.OrganizationID); err != nil {
		return err
	}
	var userID string
	if err := tx.QueryRowContext(ctx, `SELECT u.id FROM users u JOIN memberships m ON m.user_id = u.id WHERE m.organization_id = ? AND lower(u.username) = lower(?) AND u.password_hash <> '' AND m.status = 'active'`, actor.OrganizationID, username).Scan(&userID); err != nil {
		return err
	}
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE memberships SET role = 'owner', updated_at = ? WHERE organization_id = ? AND user_id = ? AND status = 'active'`, now, actor.OrganizationID, userID); err != nil {
		return err
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, '', ?, 'user.owner_assigned', 'user', ?, 'succeeded', '{"entry":"local_cli"}', ?, ?)`, id, actor.OrganizationID, actor.UserID, userID, id, now); err != nil {
		return err
	}
	return tx.Commit()
}
