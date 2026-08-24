package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/databaseconfigured"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/agentloop"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

// TestLiveAskTaskModelDiagnostic is opt-in because it calls the configured
// real provider. It compares a minimal Ask with the exact failed Task request
// under the current versioned action-first policy. Credentials stay inside
// SecretStore and are never logged by this test.
func TestLiveAskTaskModelDiagnostic(t *testing.T) {
	dataRoot := strings.TrimSpace(os.Getenv("OKTAPOD_LIVE_DATA_ROOT"))
	projectID := strings.TrimSpace(os.Getenv("OKTAPOD_LIVE_PROJECT_ID"))
	if dataRoot == "" || projectID == "" {
		t.Skip("set OKTAPOD_LIVE_DATA_ROOT and OKTAPOD_LIVE_PROJECT_ID to run the real-provider diagnostic")
	}

	ctx := t.Context()
	databasePath := filepath.Join(dataRoot, "oktapod.db")
	store, err := sqlite.Open(ctx, databasePath, filepath.Join(dataRoot, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	runID, step, err := latestFailedModelStep(ctx, databasePath, projectID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	assembly, failure := okruntime.NewAgentTurnAssembler(store).Assemble(ctx, step, run)
	if failure != nil {
		t.Fatalf("assemble failed: %s %s", failure.ErrorCode, failure.ResultSummary)
	}
	var versionedSnapshot map[string]any
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &versionedSnapshot); err != nil {
		t.Fatal(err)
	}
	versionedSnapshot["thinking_policy_version"] = core.ThinkingPolicyTaskActionV1
	encodedSnapshot, err := json.Marshal(versionedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	versionedRun := run
	versionedRun.ConfigSnapshotJSON = string(encodedSnapshot)
	versionedAssembly, failure := okruntime.NewAgentTurnAssembler(store).Assemble(ctx, step, versionedRun)
	if failure != nil {
		t.Fatalf("versioned assemble failed: %s %s", failure.ErrorCode, failure.ResultSummary)
	}

	keys, err := secretstore.NewVaultKeyManager(ctx, store, run.OrganizationID, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	credentials := secretstore.NewDatabase(store, keys, run.OrganizationID)
	model := databaseconfigured.New(store, credentials, run.OrganizationID)

	minimal := okruntime.ModelRequest{
		SystemPrompt: "You are a concise assistant.",
		Messages: []core.ModelMessage{{Role: core.MessageRoleUser, Content: []core.ContentBlock{{
			Type: core.ContentBlockText, Text: "只回复 OK",
		}}}},
		RequestedModel:  assembly.Request.RequestedModel,
		Model:           assembly.Request.Model,
		FallbackModel:   assembly.Request.FallbackModel,
		ThinkingLevel:   assembly.Request.ThinkingLevel,
		MaxOutputTokens: assembly.Request.MaxOutputTokens,
	}
	messagesJSON, err := json.Marshal(assembly.Request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("frozen_request run=%s model=%s thinking=%s max_output=%d system_bytes=%d messages_bytes=%d tools=%d",
		runID, assembly.Request.Model, assembly.Request.ThinkingLevel, assembly.Request.MaxOutputTokens,
		len(assembly.Request.SystemPrompt), len(messagesJSON), len(assembly.Request.ToolDefinitions))
	if versionedAssembly.Request.ThinkingLevel != "auto" || versionedAssembly.Request.ContextPlan["action_first"] != true {
		t.Fatalf("versioned auto contract thinking=%q context=%#v", versionedAssembly.Request.ThinkingLevel, versionedAssembly.Request.ContextPlan)
	}

	for _, item := range []struct {
		name    string
		request okruntime.ModelRequest
	}{
		{name: "minimal_ask", request: minimal},
		{name: "versioned_auto_tool_task", request: versionedAssembly.Request},
	} {
		t.Run(item.name, func(t *testing.T) {
			callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
			defer cancel()
			started := time.Now()
			firstDeltaAt := time.Duration(0)
			deltaBytes := 0
			result, callErr := model.Stream(callCtx, item.request, func(delta string) error {
				if delta != "" && firstDeltaAt == 0 {
					firstDeltaAt = time.Since(started)
				}
				deltaBytes += len(delta)
				return nil
			})
			payload, marshalErr := json.Marshal(item.request)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Logf("request_bytes=%d tool_definitions=%d elapsed=%s first_delta=%s delta_bytes=%d content_bytes=%d tool_calls=%d stop_reason=%s error_code=%s error=%v",
				len(payload), len(item.request.ToolDefinitions), time.Since(started).Round(time.Millisecond), firstDeltaAt.Round(time.Millisecond), deltaBytes,
				len(result.Content), len(result.ToolCalls), result.StopReason, result.ErrorCode, callErr)
		})
	}
}

func latestFailedModelStep(ctx context.Context, databasePath, projectID string) (string, core.ClaimedStep, error) {
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=ro")
	if err != nil {
		return "", core.ClaimedStep{}, err
	}
	defer database.Close()
	var runID string
	if err := database.QueryRowContext(ctx, `
SELECT r.id
FROM tasks AS t
JOIN runs AS r ON r.task_id = t.id
WHERE t.project_id = ? AND t.status = 'failed'
ORDER BY t.updated_at DESC, r.created_at DESC, r.id DESC
LIMIT 1`, projectID).Scan(&runID); err != nil {
		return "", core.ClaimedStep{}, err
	}
	var step core.ClaimedStep
	if err := database.QueryRowContext(ctx, `
SELECT id, organization_id, project_id, run_id, step_type, input_json, attempt_count
FROM steps
WHERE run_id = ? AND step_type LIKE 'model_%'
ORDER BY position DESC, id DESC
LIMIT 1`, runID).Scan(&step.ID, &step.OrganizationID, &step.ProjectID, &step.RunID, &step.StepType, &step.InputJSON, &step.AttemptNumber); err != nil {
		return "", core.ClaimedStep{}, err
	}
	return runID, step, nil
}

var _ agentloop.ModelGateway = (*databaseconfigured.Client)(nil)
