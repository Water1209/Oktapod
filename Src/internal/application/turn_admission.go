package application

import (
	"context"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type turnReceiptRepository interface {
	ReplayProjectTurn(context.Context, core.Scope, string, core.Idempotency) (core.ProjectTurnResult, bool, error)
}

func (s *Service) submitProjectTurn(ctx context.Context, projectID string, input core.ProjectTurnInput, correlationID, key string, onRoute func(string) error, onDelta func(string) error) (core.ProjectTurnResult, error) {
	if err := s.authorize(ctx, "write", "project", projectID); err != nil {
		return core.ProjectTurnResult{}, err
	}
	key, err := normalizeIdempotencyKey(key)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	input.Admission = core.Idempotency{Key: key, RequestHash: requestHash(struct {
		ProjectID string
		Input     core.ProjectTurnInput
	}{projectID, input})}
	scope := s.ScopeFor(ctx)
	work := func() (core.ProjectTurnResult, error) {
		// Admission belongs to the durable request, not the lifetime of its
		// streaming HTTP connection. A disconnected client can replay it.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		if receipts, ok := s.repository.(turnReceiptRepository); ok {
			result, found, err := receipts.ReplayProjectTurn(workCtx, scope, projectID, input.Admission)
			if err != nil || found {
				return result, err
			}
		}
		route := func(value string) error {
			if onRoute != nil && ctx.Err() == nil {
				_ = onRoute(value)
			}
			return nil
		}
		delta := func(value string) error {
			if onDelta != nil && ctx.Err() == nil {
				_ = onDelta(value)
			}
			return nil
		}
		if onDelta == nil {
			delta = nil
		}
		return s.submitProjectTurnOnce(workCtx, projectID, input, correlationID, key, route, delta)
	}
	if key == "" {
		return work()
	}
	type receipt struct {
		Hash   string
		Result core.ProjectTurnResult
	}
	flightKey := scope.OrganizationID + ":" + scope.UserID + ":" + projectID + ":" + key
	result := s.turnFlights.DoChan(flightKey, func() (any, error) { value, err := work(); return receipt{input.Admission.RequestHash, value}, err })
	select {
	case <-ctx.Done():
		return core.ProjectTurnResult{}, ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			return core.ProjectTurnResult{}, completed.Err
		}
		value := completed.Val.(receipt)
		if value.Hash != input.Admission.RequestHash {
			return core.ProjectTurnResult{}, &core.IdempotencyConflictError{}
		}
		return value.Result, nil
	}
}
