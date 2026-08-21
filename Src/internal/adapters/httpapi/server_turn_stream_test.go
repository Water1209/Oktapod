package httpapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTurnStreamErrorDetailPreservesDeterministicFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   string
		detail string
	}{
		{name: "wrapped model outage", err: fmt.Errorf("submit turn: %w", &application.ModelTurnUnavailableError{Err: errors.New("connection reset")}), code: "MODEL_TURN_UNAVAILABLE", detail: "模型响应中断，请重试"},
		{name: "validation", err: &core.ValidationError{Field: "goal", Message: "is required"}, code: "VALIDATION_FAILED", detail: "goal: is required"},
		{name: "forbidden", err: &core.ForbiddenError{Message: "project is outside scope"}, code: "FORBIDDEN", detail: "project is outside scope"},
		{name: "state conflict", err: &core.StateConflictError{Status: core.StatusRunning, Command: "resume"}, code: "STATE_CONFLICT", detail: "cannot resume a task in running state"},
		{name: "unknown", err: errors.New("database unavailable"), code: "TURN_STREAM_ERROR", detail: "会话处理失败，请重试"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, detail := turnStreamErrorDetail(test.err)
			if code != test.code || detail != test.detail {
				t.Fatalf("turnStreamErrorDetail() = (%q, %q), want (%q, %q)", code, detail, test.code, test.detail)
			}
		})
	}
}
