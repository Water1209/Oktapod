package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlitegen "github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite/sqlc"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) ClaimNextStep(ctx context.Context, workerID string, now, leaseUntil time.Time) (core.ClaimedStep, error) {
	// Keep the common idle poll read-only. The UPDATE ... RETURNING below is
	// still the atomic claim/CAS, but it is attempted only when a candidate
	// exists so four idle Workers do not continuously contend with HTTP writes
	// and active checkpoint transactions for SQLite's single writer.
	const findBatchAwareCandidate = `
		SELECT candidate.id
		FROM steps AS candidate
		JOIN runs AS candidate_run ON candidate_run.id = candidate.run_id
		WHERE (candidate.status = 'queued'
		   OR (candidate.status = 'running' AND candidate.lease_until <> '' AND candidate.lease_until < ?))
		  AND candidate_run.status IN ('queued', 'running')
		  AND (
		      EXISTS (
		          SELECT 1 FROM steps same_run
		          WHERE same_run.run_id = candidate.run_id
		            AND same_run.status = 'running'
		            AND same_run.lease_until >= ?
		      )
		      OR (
		          SELECT COUNT(DISTINCT active.run_id)
		          FROM steps active
		          WHERE active.status = 'running'
		            AND active.lease_until >= ?
		      ) < 4
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM steps AS earlier
		      WHERE earlier.run_id = candidate.run_id
		        AND earlier.position < candidate.position
		        AND earlier.status IN ('queued', 'running', 'waiting_approval')
		        AND NOT (
		            candidate.tool_batch_id <> ''
		            AND earlier.tool_batch_id = candidate.tool_batch_id
		            AND (
		                (
		                    NOT EXISTS (SELECT 1 FROM tool_batch_schedulers scheduler WHERE scheduler.batch_id = candidate.tool_batch_id)
		                    AND EXISTS (
		                        SELECT 1 FROM tool_batches batch
		                        WHERE batch.id = candidate.tool_batch_id
		                          AND batch.execution_mode = 'parallel'
		                    )
		                )
		                OR EXISTS (
		                    SELECT 1
		                    FROM tool_call_barriers candidate_barrier
		                    JOIN tool_call_barriers earlier_barrier
		                      ON earlier_barrier.step_id = earlier.id
		                    WHERE candidate_barrier.step_id = candidate.id
		                      AND candidate_barrier.barrier_group = earlier_barrier.barrier_group
		                )
		            )
		        )
		  )
		  AND (
		      candidate.tool_batch_id = ''
		      OR NOT EXISTS (
		          SELECT 1 FROM steps batch_failure
		          WHERE batch_failure.tool_batch_id = candidate.tool_batch_id
		            AND batch_failure.status IN ('blocked', 'failed', 'canceled')
		      )
		  )
		ORDER BY candidate.created_at ASC, candidate.id ASC
		LIMIT 1`
	nowText := formatTime(now)
	var candidateID string
	if err := s.db.QueryRowContext(ctx, findBatchAwareCandidate, nowText, nowText, nowText).Scan(&candidateID); errors.Is(err, sql.ErrNoRows) {
		recovered, recoverErr := s.recoverCompletedToolBatchWithoutRunnableStep(ctx, now)
		if recoverErr != nil {
			return core.ClaimedStep{}, fmt.Errorf("recover completed tool batch: %w", recoverErr)
		}
		if recovered {
			return s.ClaimNextStep(ctx, workerID, now, leaseUntil)
		}
		healed, healErr := s.recoverFailedSequentialToolBatch(ctx, now)
		if healErr != nil {
			return core.ClaimedStep{}, fmt.Errorf("recover failed sequential tool batch: %w", healErr)
		}
		if healed {
			return s.ClaimNextStep(ctx, workerID, now, leaseUntil)
		}
		return core.ClaimedStep{}, core.ErrNoWork
	} else if err != nil {
		return core.ClaimedStep{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ClaimedStep{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	const claimNextBatchAwareStep = `
		UPDATE steps
		SET status = 'running',
		    owner_id = ?,
		    lease_until = ?,
		    fence_token = fence_token + 1,
		    attempt_count = attempt_count + 1,
		    updated_at = ?
		WHERE id = (
		    SELECT candidate.id
		    FROM steps AS candidate
		    JOIN runs AS candidate_run ON candidate_run.id = candidate.run_id
		    WHERE (candidate.status = 'queued'
		       OR (candidate.status = 'running' AND candidate.lease_until <> '' AND candidate.lease_until < ?))
		      AND candidate_run.status IN ('queued', 'running')
		      AND (
		          EXISTS (
		              SELECT 1 FROM steps same_run
		              WHERE same_run.run_id = candidate.run_id
		                AND same_run.status = 'running'
		                AND same_run.lease_until >= ?
		          )
		          OR (
		              SELECT COUNT(DISTINCT active.run_id)
		              FROM steps active
		              WHERE active.status = 'running'
		                AND active.lease_until >= ?
		          ) < 4
		      )
		      AND NOT EXISTS (
		          SELECT 1
		          FROM steps AS earlier
		          WHERE earlier.run_id = candidate.run_id
		            AND earlier.position < candidate.position
		            AND earlier.status IN ('queued', 'running', 'waiting_approval')
		            AND NOT (
		                candidate.tool_batch_id <> ''
		                AND earlier.tool_batch_id = candidate.tool_batch_id
		                AND (
		                    (
		                        NOT EXISTS (SELECT 1 FROM tool_batch_schedulers scheduler WHERE scheduler.batch_id = candidate.tool_batch_id)
		                        AND EXISTS (
		                            SELECT 1 FROM tool_batches batch
		                            WHERE batch.id = candidate.tool_batch_id
		                              AND batch.execution_mode = 'parallel'
		                        )
		                    )
		                    OR EXISTS (
		                        SELECT 1
		                        FROM tool_call_barriers candidate_barrier
		                        JOIN tool_call_barriers earlier_barrier
		                          ON earlier_barrier.step_id = earlier.id
		                        WHERE candidate_barrier.step_id = candidate.id
		                          AND candidate_barrier.barrier_group = earlier_barrier.barrier_group
		                    )
		                )
		            )
		      )
		      AND (
		          candidate.tool_batch_id = ''
		          OR NOT EXISTS (
		              SELECT 1 FROM steps batch_failure
		              WHERE batch_failure.tool_batch_id = candidate.tool_batch_id
		                AND batch_failure.status IN ('blocked', 'failed', 'canceled')
		          )
		      )
		    ORDER BY candidate.created_at ASC, candidate.id ASC
		    LIMIT 1
		)
		RETURNING id, organization_id, project_id, run_id, step_type, position, status,
		          input_json, output_json, attempt_count, owner_id, lease_until,
		          fence_token, error_code, created_at, updated_at, agent_id`
	var row sqlitegen.Step
	err = tx.QueryRowContext(ctx, claimNextBatchAwareStep, workerID, formatTime(leaseUntil), nowText, nowText, nowText, nowText).Scan(
		&row.ID,
		&row.OrganizationID,
		&row.ProjectID,
		&row.RunID,
		&row.StepType,
		&row.Position,
		&row.Status,
		&row.InputJson,
		&row.OutputJson,
		&row.AttemptCount,
		&row.OwnerID,
		&row.LeaseUntil,
		&row.FenceToken,
		&row.ErrorCode,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.AgentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ClaimedStep{}, core.ErrNoWork
	}
	if err != nil {
		return core.ClaimedStep{}, err
	}
	if row.AttemptCount > 1 {
		if err := q.LosePreviousStepAttempts(ctx, sqlitegen.LosePreviousStepAttemptsParams{FinishedAt: formatTime(now), StepID: row.ID}); err != nil {
			return core.ClaimedStep{}, err
		}
	}
	attemptID, err := newID()
	if err != nil {
		return core.ClaimedStep{}, err
	}
	attemptKind := "initial"
	if row.AttemptCount > 1 {
		attemptKind = "recovery"
	}
	if err := q.CreateStepAttempt(ctx, sqlitegen.CreateStepAttemptParams{
		ID:             attemptID,
		OrganizationID: row.OrganizationID,
		ProjectID:      row.ProjectID,
		RunID:          row.RunID,
		StepID:         row.ID,
		AttemptNumber:  row.AttemptCount,
		AttemptKind:    attemptKind,
		OwnerID:        row.OwnerID,
		FenceToken:     row.FenceToken,
		StartedAt:      formatTime(now),
	}); err != nil {
		return core.ClaimedStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ClaimedStep{}, err
	}
	return core.ClaimedStep{ID: row.ID, AttemptID: attemptID, AttemptNumber: row.AttemptCount, Recovery: row.AttemptCount > 1, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, RunID: row.RunID, StepType: row.StepType, AgentID: row.AgentID, InputJSON: row.InputJson, OwnerID: row.OwnerID, FenceToken: row.FenceToken}, nil
}

func (s *Store) HeartbeatStep(ctx context.Context, step core.ClaimedStep, now, leaseUntil time.Time) (bool, error) {
	count, err := s.queries.HeartbeatStep(ctx, sqlitegen.HeartbeatStepParams{LeaseUntil: formatTime(leaseUntil), UpdatedAt: formatTime(now), ID: step.ID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	return count == 1, err
}

func (s *Store) GetRunContext(ctx context.Context, runID string) (core.RunContext, error) {
	row, err := s.queries.GetRunContext(ctx, runID)
	if err != nil {
		return core.RunContext{}, err
	}
	var configSnapshot string
	var requestedProviderID, requestedModelID, effectiveProviderID, effectiveModelID string
	var thinking, fallbackReason, catalogVersion string
	if err := s.db.QueryRowContext(ctx, `
SELECT config_snapshot_json, requested_provider_id, requested_model_id,
       effective_provider_id, effective_model_id, thinking_level,
       model_fallback_reason, model_catalog_version
FROM runs WHERE id = ?`, runID).Scan(
		&configSnapshot, &requestedProviderID, &requestedModelID,
		&effectiveProviderID, &effectiveModelID, &thinking,
		&fallbackReason, &catalogVersion,
	); err != nil {
		return core.RunContext{}, err
	}
	return core.RunContext{
		RunID: row.RunID, TaskID: row.TaskID, ProjectID: row.ProjectID,
		OrganizationID: row.OrganizationID, OwnerUserID: row.OwnerUserID,
		ConversationID: row.ConversationID, Title: row.Title, Goal: row.Goal,
		Acceptance: row.Acceptance, Mode: row.Mode, Visibility: row.Visibility,
		ConfigSnapshotJSON: configSnapshot,
		RequestedModel:     core.ModelRef{ProviderID: requestedProviderID, ModelID: requestedModelID},
		EffectiveModel:     core.ModelRef{ProviderID: effectiveProviderID, ModelID: effectiveModelID},
		Thinking:           thinking, ModelFallbackReason: fallbackReason, ModelCatalogVersion: catalogVersion,
	}, nil
}

func (s *Store) MarkStepStarted(ctx context.Context, step core.ClaimedStep, run core.RunContext, correlationID string) error {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if step.AgentID != "" {
		if _, err := q.UpdateAgentTeamMemberStatus(ctx, sqlitegen.UpdateAgentTeamMemberStatusParams{Status: "working", CurrentStepID: step.ID, Model: "", UpdatedAt: now, ID: step.AgentID, RunID: run.RunID}); err != nil {
			return err
		}
	}
	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.TaskID}); err != nil {
		return err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.RunID}); err != nil {
		return err
	}
	if step.Recovery {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "step.recovered", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber, "fence_token": step.FenceToken}}); err != nil {
			return err
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "step.started", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitCheckpoint(ctx context.Context, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any, correlationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM steps
		WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running'`,
		step.ID, step.OwnerID, step.FenceToken,
	).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return fmt.Errorf("LEASE_LOST: cannot checkpoint step %s fence %d", step.ID, step.FenceToken)
	}
	q := s.queries.WithTx(tx)
	checkpointID, contentHash, err := createCheckpoint(ctx, q, step, run, kind, state)
	if err != nil {
		return err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "checkpoint.committed", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"checkpoint_id": checkpointID, "checkpoint_kind": kind, "content_hash": contentHash, "step_id": step.ID, "attempt_id": step.AttemptID, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginToolExecution(ctx context.Context, step core.ClaimedStep, run core.RunContext, call core.ToolCall, inputHash string, idempotent bool, correlationID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running'`, step.ID, step.OwnerID, step.FenceToken).Scan(&owned); err != nil {
		return "", err
	}
	if owned != 1 {
		return "", fmt.Errorf("LEASE_LOST: cannot execute tool step %s fence %d", step.ID, step.FenceToken)
	}
	var storedName string
	var storedHash string
	var storedRisk string
	var storedDecision string
	var toolStatus string
	if err := tx.QueryRowContext(ctx, `SELECT tool_name, arguments_hash, risk_level, policy_decision, status FROM tool_calls WHERE id = ? AND step_id = ? AND run_id = ?`, call.ID, step.ID, run.RunID).Scan(&storedName, &storedHash, &storedRisk, &storedDecision, &toolStatus); err != nil {
		return "", err
	}
	planned, policyErr := s.executionPolicy.EvaluateExecution(ctx, application.Principal{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}, application.ExecutionRequest{ProjectID: run.ProjectID, Call: call, ConfigSnapshot: run.ConfigSnapshotJSON})
	if policyErr != nil {
		return "", policyErr
	}
	if strings.HasPrefix(call.Name, "mcp__") {
		risk, plannedIdempotent, reason := application.MCPPolicyFromSnapshot(run.ConfigSnapshotJSON, call.Name)
		planned.RiskLevel = risk
		planned.Idempotent = plannedIdempotent
		if reason != "" {
			planned.Reason = reason
		}
	}
	authorization := core.ExecutionAuthorization{
		ToolIdentity: storedName, InputHash: storedHash,
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		RiskLevel: storedRisk, PolicyDecision: storedDecision, PolicyVersion: core.ToolPolicyVersion,
	}
	if authErr := core.ValidateExecutionAuthorization(planned, authorization); authErr != nil || storedHash != inputHash || (toolStatus != "queued" && toolStatus != "running") {
		if authErr == nil {
			authErr = errors.New("frozen tool input or state no longer matches")
		}
		return "", fmt.Errorf("TOOL_AUTHORIZATION_INVALID: %w", authErr)
	}
	networkClass, matchedRuleID, policyHash, authorizationID := "", "", "", ""
	if call.Name == core.ToolWebFetch {
		networkClass = "public"
		if planned.ApprovalScope == core.ApprovalScopeRunOrigin {
			networkClass, matchedRuleID, policyHash = "private", planned.ApprovalRuleID, planned.ApprovalPolicyHash
			if err := tx.QueryRowContext(ctx, `
				SELECT id FROM run_approval_rules
				WHERE organization_id = ? AND project_id = ? AND run_id = ? AND tool_name = ?
				  AND grant_type = 'run_origin' AND grant_key = ? AND policy_hash = ? AND expires_at > ?
				LIMIT 1`, run.OrganizationID, run.ProjectID, run.RunID, call.Name,
				planned.ApprovalGrantKey, planned.ApprovalPolicyHash, formatTime(time.Now())).Scan(&authorizationID); err != nil {
				return "", fmt.Errorf("TOOL_AUTHORIZATION_INVALID: private web origin approval is missing or expired: %w", err)
			}
		}
	}

	var receiptID string
	var invocationID string
	var receiptStatus string
	var storedIdempotent int64
	queryErr := tx.QueryRowContext(ctx, `SELECT id, invocation_id, status, idempotent FROM side_effect_receipts WHERE tool_call_id = ?`, call.ID).Scan(&receiptID, &invocationID, &receiptStatus, &storedIdempotent)
	unknown := false
	now := formatTime(time.Now())
	if errors.Is(queryErr, sql.ErrNoRows) {
		receiptID, err = newID()
		if err != nil {
			return "", err
		}
		invocationID, err = newID()
		if err != nil {
			return "", err
		}
		idempotentValue := 0
		if idempotent {
			idempotentValue = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO side_effect_receipts (
				id, organization_id, project_id, task_id, run_id, step_id, tool_call_id,
				invocation_id, input_hash, idempotent, status, network_class, matched_rule_id,
				policy_hash, authorization_id, started_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?, ?, ?, ?, ?)`,
			receiptID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, step.ID, call.ID,
			invocationID, inputHash, idempotentValue, networkClass, matchedRuleID, policyHash, authorizationID, now,
		); err != nil {
			return "", err
		}
	} else if queryErr != nil {
		return "", queryErr
	} else if !idempotent || storedIdempotent != 1 {
		unknown = true
		if _, err := tx.ExecContext(ctx, `UPDATE side_effect_receipts SET status = 'unknown', error_code = 'SIDE_EFFECT_STATUS_UNKNOWN', finished_at = ? WHERE id = ?`, now, receiptID); err != nil {
			return "", err
		}
	} else if _, err := tx.ExecContext(ctx, `
			UPDATE side_effect_receipts
			SET status = 'started', exit_code = -1, output_json = '{}',
			    error_code = '', network_class = ?, matched_rule_id = ?, policy_hash = ?,
			    authorization_id = ?, started_at = ?, finished_at = ''
			WHERE id = ?`, networkClass, matchedRuleID, policyHash, authorizationID, now, receiptID); err != nil {
		return "", err
	}
	if unknown {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = 'SIDE_EFFECT_STATUS_UNKNOWN', updated_at = ? WHERE id = ?`, now, call.ID); err != nil {
			return "", err
		}
		var planRevisionID, planItemID string
		_ = tx.QueryRowContext(ctx, `SELECT plan_revision_id, run_plan_item_id FROM steps WHERE id = ?`, step.ID).Scan(&planRevisionID, &planItemID)
		expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
		requestJSON, _ := json.Marshal(map[string]any{"receipt_id": receiptID, "tool_call_id": call.ID, "tool_name": call.Name, "input_hash": inputHash, "invocation_id": invocationID})
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO human_action_requests (
				id, organization_id, project_id, conversation_id, task_id, run_id,
				step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
				risk_level, title, reason, request_json, request_hash, requested_by,
				requested_at, expires_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'side_effect_resolution', 'waiting_user', 'user', 'L3', ?, ?, ?, ?, ?, ?, ?)`,
			receiptID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
			step.ID, planRevisionID, planItemID, call.Name, "系统无法确认外部操作是否已执行，必须人工核查后才能继续。",
			string(requestJSON), inputHash, step.OwnerID, now, expiresAt,
		); err != nil {
			return "", err
		}
		if planItemID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, planItemID); err != nil {
				return "", err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'running', updated_at = ? WHERE id = ?`, now, call.ID); err != nil {
		return "", err
	}
	q := s.queries.WithTx(tx)
	eventType := "tool.execution_started"
	if unknown {
		eventType = "tool.side_effect_unknown"
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: eventType, ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"tool_call_id": call.ID, "step_id": step.ID, "tool_name": call.Name, "input_summary": toolInputSummary(call.Name, call.Arguments), "invocation_id": invocationID, "input_hash": inputHash, "idempotent": idempotent}}); err != nil {
		return "", err
	}
	if unknown {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.requested", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": receiptID, "kind": core.HumanActionSideEffectResolution, "status": core.HumanActionWaitingUser, "title": call.Name}}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if unknown {
		return "", fmt.Errorf("SIDE_EFFECT_STATUS_UNKNOWN: refusing to repeat non-idempotent invocation %s", invocationID)
	}
	return invocationID, nil
}

func (s *Store) FinishStep(ctx context.Context, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, correlationID string) error {
	now := formatTime(time.Now())
	output, err := json.Marshal(outcome.Output)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	// Release the pre-execution reservation before accounting the actual model
	// usage. This keeps the reservation bounded to the in-flight call while the
	// completion transaction remains the source of truth for measured usage.
	var reservedTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT budget_reserved_tokens FROM steps WHERE id = ?`, step.ID).Scan(&reservedTokens); err == nil && reservedTokens > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_budgets
			SET reserved_model_calls = CASE WHEN reserved_model_calls > 0 THEN reserved_model_calls - 1 ELSE 0 END,
			    reserved_tokens = CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END,
			    updated_at = ?
			WHERE run_id = ?`, reservedTokens, reservedTokens, now, run.RunID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET budget_reserved_tokens = 0 WHERE id = ?`, step.ID); err != nil {
			return err
		}
	}
	if outcome.Model != nil {
		var maxCalls, maxTokens int64
		var budgetStatus string
		if err := tx.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens, status FROM run_budgets WHERE run_id = ?`, run.RunID).Scan(&maxCalls, &maxTokens, &budgetStatus); err == nil && budgetStatus == "active" {
			var calls, tokens int64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0) FROM model_calls WHERE run_id = ?`, run.RunID).Scan(&calls, &tokens); err != nil {
				return err
			}
			incoming := outcome.Model.Usage.InputTokens + outcome.Model.Usage.OutputTokens
			if (maxCalls > 0 && calls+1 > maxCalls) || (maxTokens > 0 && tokens+incoming > maxTokens) {
				outcome.Status = core.StatusBlocked
				outcome.ErrorCode = "BUDGET_EXCEEDED"
				outcome.ResultSummary = fmt.Sprintf("Run budget reached (calls %d/%d, tokens %d/%d); choose a smaller scope or approve an exception.", calls+1, maxCalls, tokens+incoming, maxTokens)
				output, err = json.Marshal(outcome.Output)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET status = 'exhausted', updated_at = ? WHERE run_id = ?`, now, run.RunID); err != nil {
					return err
				}
			}
		}
	}
	count, err := q.CompleteStep(ctx, sqlitegen.CompleteStepParams{Status: outcome.Status, OutputJson: string(output), ErrorCode: outcome.ErrorCode, UpdatedAt: now, ID: step.ID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("LEASE_LOST: step %s fence %d is no longer owned", step.ID, step.FenceToken)
	}
	attemptCount, err := q.FinishStepAttempt(ctx, sqlitegen.FinishStepAttemptParams{Status: outcome.Status, ErrorCode: outcome.ErrorCode, FinishedAt: now, ID: step.AttemptID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	if err != nil {
		return err
	}
	if attemptCount != 1 {
		return fmt.Errorf("ATTEMPT_LOST: attempt %s is no longer active", step.AttemptID)
	}
	checkpointID, contentHash, err := createCheckpoint(ctx, q, step, run, "after_execution", map[string]any{"status": outcome.Status, "error_code": outcome.ErrorCode, "output": outcome.Output, "result_summary": outcome.ResultSummary})
	if err != nil {
		return err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "checkpoint.committed", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"checkpoint_id": checkpointID, "checkpoint_kind": "after_execution", "content_hash": contentHash, "step_id": step.ID, "attempt_id": step.AttemptID, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	if outcome.Model != nil {
		if err := persistModelUsage(ctx, tx, q, step, run, *outcome.Model, now); err != nil {
			return err
		}
	}

	taskStatus := outcome.Status
	taskSummary := outcome.ResultSummary
	taskErrorCode := outcome.ErrorCode
	modelRetryStepID := ""
	modelRetryCount := 0
	toolRecoveryStepID := ""
	toolRecoveryCount := 0
	toolRecoveryCallID := ""
	if outcome.ErrorCode == "BUDGET_RESERVATION_EXCEEDED" || outcome.ErrorCode == "BUDGET_EXCEEDED" {
		if err := s.createBudgetHumanAction(ctx, tx, q, step, run, outcome, now, correlationID); err != nil {
			return err
		}
		taskStatus, taskSummary, taskErrorCode = core.StatusWaitingInput, "本次运行预算不足，等待你确认扩展后继续。", ""
	}
	reviewStep := step.StepType == core.StepHumanActionReview
	if reviewStep {
		taskStatus, taskSummary, taskErrorCode, err = s.applyAutoReviewOutcome(ctx, tx, q, step, run, outcome, now, correlationID)
		if err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil && strings.TrimSpace(outcome.ResultSummary) != "" {
		phase := "final"
		if len(outcome.Model.ToolCalls) > 0 || len(lengthTruncatedToolCalls(outcome.Output)) > 0 {
			phase = "progress"
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.message", ActorType: "agent", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "phase": phase, "content": outcome.ResultSummary, "model": outcome.Model.Model, "tool_call_count": len(outcome.Model.ToolCalls), "tool_argument_repairs": outcome.Model.ToolArgumentRepairs}}); err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil && len(outcome.Model.ToolCalls) > 0 {
		taskStatus, taskErrorCode, err = s.planToolSteps(ctx, tx, q, step, run, outcome.Model.ToolCalls, now, correlationID)
		if err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil {
		if calls := lengthTruncatedToolCalls(outcome.Output); len(calls) > 0 {
			if err := s.recordLengthTruncatedToolCalls(ctx, tx, q, step, run, calls, now, correlationID); err != nil {
				return err
			}
			continuationStepID, queueErr := s.queueLengthTruncatedModelContinuation(ctx, tx, q, step, run, now, correlationID)
			if queueErr != nil {
				return queueErr
			}
			if continuationStepID == "" {
				return fmt.Errorf("MODEL_CONTINUATION_NOT_QUEUED: length-truncated tool calls have no recovery step")
			}
			taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
		}
	}
	if run.Mode == core.ModeSingleAgent && outcome.Status == core.StatusFailed && outcome.Model != nil && isRetryableModelInteractionError(outcome.ErrorCode) {
		var queued bool
		modelRetryStepID, modelRetryCount, queued, err = s.queueModelInteractionRetry(ctx, tx, step, run, outcome.ErrorCode, now)
		if err != nil {
			return err
		}
		if queued {
			taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
		}
	}
	if outcome.ToolExecution != nil {
		if err := s.finishToolExecution(ctx, tx, q, step, run, *outcome.ToolExecution, now, correlationID); err != nil {
			return err
		}
		if outcome.Status == core.StatusSucceeded {
			taskStatus, taskErrorCode, err = deriveRunStatus(ctx, tx, run.RunID)
			if err != nil {
				return err
			}
			if taskStatus == core.StatusSucceeded {
				batchReady, continuationClaimed, batchID, batchErr := s.completeToolBatchState(ctx, tx, q, step, run, now, correlationID)
				if batchErr != nil {
					return batchErr
				}
				if !batchReady {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else if !continuationClaimed {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else {
					queued := false
					var queueErr error
					var queuedStepID string
					if step.StepType == core.ToolSkillInvoke {
						queued, queueErr = s.queueSkillResume(ctx, tx, q, step, run, outcome.Output, now, correlationID)
					} else {
						forceSynthesis := false
						if step.StepType == core.ToolWebSearch {
							forceSynthesis, queueErr = webSearchSynthesisThresholdReached(ctx, tx, run.RunID)
						}
						if queueErr == nil && forceSynthesis {
							queued, queueErr = s.queueWebSearchSynthesis(ctx, tx, q, step, run, now, correlationID)
						} else if queueErr == nil {
							var continuationStepID string
							continuationStepID, queueErr = s.queueModelContinuation(ctx, tx, q, step, run, now, correlationID)
							queued = continuationStepID != ""
							queuedStepID = continuationStepID
						}
					}
					if queueErr != nil {
						return queueErr
					}
					if queued {
						if batchID != "" && queuedStepID != "" {
							if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET continuation_step_id = ?, updated_at = ? WHERE id = ?`, queuedStepID, now, batchID); err != nil {
								return err
							}
						}
						taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
					} else if step.StepType != core.ToolSkillInvoke {
						return fmt.Errorf("MODEL_CONTINUATION_NOT_QUEUED: completed tool batch has no next model step")
					}
				}
			}
		} else {
			batchReady, recoveryClaimed, _, batchErr := s.completeToolBatchState(ctx, tx, q, step, run, now, correlationID)
			if batchErr != nil {
				return batchErr
			}
			if !batchReady || !recoveryClaimed {
				taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
			} else if outcome.Status == core.StatusFailed && isRecoverableToolExecutionFailure(outcome.ErrorCode) {
				var queued bool
				toolRecoveryStepID, toolRecoveryCount, toolRecoveryCallID, queued, err = s.queueToolFailureRecovery(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID)
				if err != nil {
					return err
				}
				if queued {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else {
					synthesized, synthErr := s.queueWebSearchSynthesis(ctx, tx, q, step, run, now, correlationID)
					if synthErr != nil {
						return synthErr
					}
					if synthesized {
						taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
					} else if actionErr := s.createToolRecoveryHumanAction(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID); actionErr != nil {
						return actionErr
					} else {
						taskStatus, taskSummary, taskErrorCode = core.StatusWaitingInput, "自动恢复已用尽，需要你选择下一步。", ""
					}
				}
			}
		}
	} else if step.StepType == core.ToolWorkspaceWriteFile || step.StepType == core.ToolWorkspaceRunCommand {
		if outcome.Status == core.StatusBlocked {
			if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = ?, updated_at = ? WHERE step_id = ?`, outcome.ErrorCode, now, step.ID); err != nil {
				return err
			}
		} else if outcome.Status == core.StatusFailed {
			if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'failed', error_code = ?, updated_at = ? WHERE step_id = ?`, outcome.ErrorCode, now, step.ID); err != nil {
				return err
			}
			if isRecoverableToolExecutionFailure(outcome.ErrorCode) {
				var queued bool
				toolRecoveryStepID, toolRecoveryCount, toolRecoveryCallID, queued, err = s.queueToolFailureRecovery(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID)
				if err != nil {
					return err
				}
				if queued {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				}
			}
		}
	}
	if run.Mode == core.ModeAgentTeam {
		taskStatus, taskSummary, taskErrorCode, err = s.advanceAgentTeam(ctx, tx, q, step, run, outcome, now, correlationID)
		if err != nil {
			return err
		}
	}
	if run.Mode == core.ModeSingleAgent && taskStatus == core.StatusSucceeded && outcome.Model != nil && len(outcome.Model.ToolCalls) == 0 {
		validation, invariantErr := s.completionInvariantFailure(ctx, tx, run)
		if invariantErr != nil {
			return invariantErr
		}
		if validation.Reason != "" {
			if modelStageFromInput(step.InputJSON) == "completion_recovery" {
				taskStatus, taskErrorCode = core.StatusBlocked, validation.Reason
				if validation.Reason == "ATTACHMENT_NOT_READ" {
					taskSummary = "任务包含二进制附件，但当前 Run 没有成功的 asset_stage 收据，不能在未读取附件时标记为完成。"
				} else if validation.Reason == "WEB_EVIDENCE_MISSING" {
					taskSummary = "任务要求联网检索，但当前 Run 没有成功的互联网检索收据，不能标记为完成。"
				} else if validation.Reason == "DELIVERABLE_MISSING" && validation.DeliverableFormat == "pdf" {
					taskSummary = "任务要求 PDF 交付物，但当前 Run 没有新的、签名与 MIME 校验通过的 PDF Artifact，不能标记为完成。"
				} else if validation.Reason == "DELIVERABLE_TOO_SHORT" {
					taskSummary = fmt.Sprintf("报告实际为 %d 字，未达到不少于 %d 字的验收要求，不能标记为完成。", validation.ActualCharacters, validation.RequiredCharacters)
				} else if validation.Reason == "WORKSPACE_MUTATION_MISSING" {
					taskSummary = "任务要求修改工作区，但当前 Run 没有成功的文件写入或编辑收据，不能标记为完成。"
				} else if validation.Reason == "VERIFICATION_MISSING" {
					taskSummary = "任务要求完成后验证结果，但当前 Run 没有变更后的验证收据，不能标记为完成。"
				} else {
					taskSummary = "任务要求生成文档交付物，但当前 Run 没有成功写入任何工作区文件，不能标记为完成。"
				}
			} else {
				history, historyErr := s.collectContinuationHistory(ctx, tx, run)
				if historyErr != nil {
					return historyErr
				}
				nextStepID, queueErr := insertQueuedModelStep(ctx, tx, run, map[string]any{
					"history": history, "stage": "completion_recovery",
					"source_step_id": step.ID, "completion_recovery_reason": validation.Reason,
					"completion_deliverable_format":  validation.DeliverableFormat,
					"completion_required_characters": validation.RequiredCharacters,
					"completion_actual_characters":   validation.ActualCharacters,
				}, now)
				if queueErr != nil {
					return queueErr
				}
				planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
				if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, planRevisionID, planItemID, nextStepID); err != nil {
					return err
				}
				if err := s.appendEvent(ctx, q, eventInput{
					OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
					EventType: "completion.validation_failed", ActorType: "validator", ActorID: "runtime",
					CorrelationID: correlationID, Visibility: run.Visibility,
					Payload: map[string]any{"source_step_id": step.ID, "recovery_step_id": nextStepID, "reason": validation.Reason, "deliverable_format": validation.DeliverableFormat, "required_characters": validation.RequiredCharacters, "actual_characters": validation.ActualCharacters},
				}); err != nil {
					return err
				}
				taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
			}
		}
	}
	taskSummary, taskErrorCode = reconcileTerminalTaskResult(taskStatus, taskSummary, taskErrorCode)
	if err := s.syncRunPlanState(ctx, tx, q, run, taskStatus, taskSummary, now, correlationID); err != nil {
		return err
	}
	if snapshotStringFromJSON(run.ConfigSnapshotJSON, "turn_router_version") == "auto_agent_v2" &&
		taskStatus == core.StatusSucceeded && outcome.Model != nil && len(outcome.Model.ToolCalls) == 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_presentations
			SET presentation_kind = 'answer', resolved_at = CASE WHEN resolved_at = '' THEN ? ELSE resolved_at END, updated_at = ?
			WHERE task_id = ? AND presentation_kind = 'pending_auto'`, now, now, run.TaskID); err != nil {
			return err
		}
	}

	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: taskStatus, ResultSummary: taskSummary, ErrorCode: taskErrorCode, UpdatedAt: now, ID: run.TaskID}); err != nil {
		return err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: taskStatus, UpdatedAt: now, ID: run.RunID}); err != nil {
		return err
	}

	if taskStatus == core.StatusSucceeded || taskStatus == core.StatusFailed || taskStatus == core.StatusBlocked {
		messageContent := strings.TrimSpace(taskSummary)
		if messageContent == "" && taskErrorCode != "" {
			messageContent = "任务未完成，错误代码：" + taskErrorCode
		}
		if messageContent != "" {
			if err := s.appendConversationMessage(ctx, tx, q, run, "assistant", messageContent, now); err != nil {
				return err
			}
		}
	}
	eventType := "step." + outcome.Status
	eventPayload := map[string]any{
		"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber,
		"task_id": run.TaskID, "conversation_id": run.ConversationID,
		"status": outcome.Status, "error_code": outcome.ErrorCode, "result_summary": outcome.ResultSummary,
	}
	if outcome.Model != nil {
		eventPayload["model"] = outcome.Model.Model
		eventPayload["provider"] = outcome.Model.Provider
		eventPayload["routing_reason"] = outcome.Model.RoutingReason
		eventPayload["input_tokens"] = outcome.Model.Usage.InputTokens
		eventPayload["output_tokens"] = outcome.Model.Usage.OutputTokens
		eventPayload["usage_estimated"] = outcome.Model.Usage.Estimated
		eventPayload["tool_argument_repairs"] = outcome.Model.ToolArgumentRepairs
	}
	if contextPlan, ok := outcome.Output["context_plan"].(map[string]any); ok {
		// Keep the deterministic context decision visible in the committed event
		// so UI/audit consumers do not need to read the full Step output blob.
		eventPayload["context_plan"] = contextPlan
		if skills, ok := contextPlan["skills"].(map[string]any); ok {
			if strategy, _ := skills["strategy"].(string); strategy == "explicit_skill_invocation_v1" {
				if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "skill.loaded", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "skill_ids": skills["loaded_ids"], "skill_names": skills["loaded_names"], "allowed_tools": skills["allowed_tools"], "status": "loaded"}}); err != nil {
					return err
				}
			}
		}
	}
	if modelFailure, ok := outcome.Output["model_failure"].(map[string]any); ok {
		eventPayload["model_failure"] = modelFailure
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: eventType, ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: eventPayload}); err != nil {
		return err
	}
	if modelRetryStepID != "" {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.retry_queued", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": modelRetryStepID, "source_step_id": step.ID, "retry_count": modelRetryCount, "max_retries": core.ModelRetryLimit(outcome.ErrorCode), "error_code": outcome.ErrorCode}}); err != nil {
			return err
		}
	}
	if toolRecoveryStepID != "" {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.recovery_queued", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": toolRecoveryStepID, "source_step_id": step.ID, "tool_call_id": toolRecoveryCallID, "retry_count": toolRecoveryCount, "max_retries": core.DefaultToolRetryLimit, "error_code": outcome.ErrorCode}}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reconcileTerminalTaskResult is a final consistency barrier: a failed Task
// must never persist the success summary of the last tool that happened to
// finish. Individual tool receipts remain the source for per-action success.
