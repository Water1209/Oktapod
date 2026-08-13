package sqlite

import (
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestReconcileTerminalTaskResultRejectsContradictorySuccessSummary(t *testing.T) {
	summary, errorCode := reconcileTerminalTaskResult(core.StatusFailed, "工具执行完成，并通过确定性校验。", "")
	if errorCode != "TASK_EXECUTION_FAILED" {
		t.Fatalf("errorCode=%q want TASK_EXECUTION_FAILED", errorCode)
	}
	if summary == "" || summary == "工具执行完成，并通过确定性校验。" {
		t.Fatalf("summary=%q must describe the terminal failure", summary)
	}
}

func TestReconcileTerminalTaskResultPreservesSuccessfulTask(t *testing.T) {
	summary, errorCode := reconcileTerminalTaskResult(core.StatusSucceeded, "工具执行完成，并通过确定性校验。", "")
	if summary != "工具执行完成，并通过确定性校验。" || errorCode != "" {
		t.Fatalf("summary=%q errorCode=%q must stay unchanged", summary, errorCode)
	}
}
