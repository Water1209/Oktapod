package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestRetryOnBusyRetriesLocalDatabaseDeadline(t *testing.T) {
	attempts := 0
	err := retryOnBusy(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestToolReceiptBeginFailureDistinguishesUnknownSideEffectFromPersistenceFailure(t *testing.T) {
	unknown := toolReceiptBeginFailure(errors.New("SIDE_EFFECT_STATUS_UNKNOWN: receipt is incomplete"), "unknown")
	if unknown.Status != core.StatusBlocked || unknown.ErrorCode != "SIDE_EFFECT_STATUS_UNKNOWN" || unknown.Output["retryable"] != false {
		t.Fatalf("unknown side effect outcome=%+v", unknown)
	}

	transient := toolReceiptBeginFailure(errors.New("database is locked (SQLITE_BUSY)"), "must not be used")
	if transient.Status != core.StatusFailed || transient.ErrorCode != "TOOL_RECEIPT_BEGIN_FAILED" || transient.Output["retryable"] != true {
		t.Fatalf("transient receipt failure outcome=%+v", transient)
	}
}

func TestModelOverrideWinsOverAgentModel(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"model_override":"project-model","fallback_model_override":"project-fallback","agent":{"model":"agent-model","fallback_model":"agent-fallback"}}`)
	if model != "project-model" || fallback != "project-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestAgentModelRemainsDefaultWhenNoOverride(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"agent":{"model":"agent-model","fallback_model":"agent-fallback"}}`)
	if model != "agent-model" || fallback != "agent-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestProjectModelRemainsDefaultWhenNoTaskOrAgentOverride(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"project_default_model":"project-model","project_fallback_model":"project-fallback"}`)
	if model != "project-model" || fallback != "project-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestModelCompatibilityRetryRaisesOutputLimit(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{input: `{}`, want: 2048},
		{input: `{"model_compat_retry_count":1}`, want: 4096},
		{input: `{"model_compat_retry_count":2}`, want: 8192},
		{input: `{"model_compat_retry_count":99}`, want: 8192},
	}
	for _, test := range tests {
		if got := modelMaxOutputTokens(test.input); got != test.want {
			t.Fatalf("modelMaxOutputTokens(%s)=%d, want %d", test.input, got, test.want)
		}
	}
}

func TestSecondTruncationRetryForcesChunkedWrites(t *testing.T) {
	hint := modelCompatibilityRetryHint(`{"model_compat_retry_count":2,"model_compat_retry_reason":"PROVIDER_TOOL_ARGUMENTS_TRUNCATED"}`)
	if hint == "" || !containsAll(hint, "禁止在单个工具参数中生成整篇长文", "逐段补全") {
		t.Fatalf("unexpected retry hint: %q", hint)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
