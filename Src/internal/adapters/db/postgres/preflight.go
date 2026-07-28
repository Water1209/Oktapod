package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
)

// Preflight performs a read-only Team upgrade check. It requires a role that
// can see every organization under FORCE RLS; otherwise returning "safe" would
// be a false negative because hidden active Runs/approvals could be missed.
func Preflight(ctx context.Context, dsn string) (sqlite.UpgradePreflight, error) {
	report := sqlite.UpgradePreflight{BinarySchemaVersion: sqlite.CurrentSchemaVersion, Safe: true, Reasons: []string{}}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return report, fmt.Errorf("parse postgres preflight DSN: %w", err)
	}
	report.DatabasePath = safeDatabaseLabel(config)
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return report, fmt.Errorf("connect postgres preflight target: %w", err)
	}
	defer conn.Close(ctx)
	var bypassRLS bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypassRLS); err != nil {
		return report, fmt.Errorf("inspect postgres maintenance role: %w", err)
	}
	if !bypassRLS {
		report.Safe = false
		report.Reasons = append(report.Reasons, "maintenance role must have BYPASSRLS or be superuser")
		return report, nil
	}
	if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&report.StoredSchemaVersion); err != nil {
		return report, fmt.Errorf("read postgres schema version: %w", err)
	}
	if report.StoredSchemaVersion > sqlite.CurrentSchemaVersion {
		report.Safe = false
		report.Reasons = append(report.Reasons, "database schema is newer than this binary")
	} else if report.StoredSchemaVersion < sqlite.CurrentSchemaVersion {
		report.PendingMigrationCount = sqlite.CurrentSchemaVersion - report.StoredSchemaVersion
		report.Reasons = append(report.Reasons, "pending migrations will be applied on the next start")
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM runs WHERE status NOT IN ('succeeded', 'failed', 'canceled')`).Scan(&report.ActiveRunCount); err != nil {
		return report, fmt.Errorf("count postgres active runs: %w", err)
	}
	if report.ActiveRunCount > 0 {
		report.Safe = false
		report.Reasons = append(report.Reasons, "active or resumable runs exist")
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM approval_requests WHERE status = 'pending'`).Scan(&report.PendingApprovalCount); err != nil {
		return report, fmt.Errorf("count postgres pending approvals: %w", err)
	}
	if report.PendingApprovalCount > 0 {
		report.Safe = false
		report.Reasons = append(report.Reasons, "pending approvals exist")
	}
	return report, nil
}

func safeDatabaseLabel(config *pgx.ConnConfig) string {
	if config == nil {
		return "postgresql"
	}
	label := "postgresql://"
	if config.User != "" {
		label += config.User + "@"
	}
	if config.Host != "" {
		label += config.Host
	}
	if config.Port != 0 {
		label += fmt.Sprintf(":%d", config.Port)
	}
	if config.Database != "" {
		label += "/" + config.Database
	}
	return label
}
