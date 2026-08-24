package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	localblob "github.com/oktapod/oktapod/Src/internal/adapters/blob/localfs"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestWorkPDFConversionRequiresCaptureAcrossPermissionModes(t *testing.T) {
	for _, permissionMode := range []string{core.PermissionModeAsk, core.PermissionModeFullAccess} {
		t.Run(permissionMode, func(t *testing.T) {
			modelCalls := 0
			writeArguments, _ := json.Marshal(map[string]any{
				"path":    "makepdf.go",
				"content": "package main\nimport \"os\"\nfunc main(){ _ = os.WriteFile(\"output.pdf\", []byte(\"%PDF-1.7\\n1 0 obj\\n<<>>\\nendobj\\n%%EOF\\n\"), 0600) }\n",
			})
			commandArguments, _ := json.Marshal(map[string]any{"program": "go", "args": []string{"run", "makepdf.go"}})
			captureArguments, _ := json.Marshal(map[string]any{"path": "output.pdf", "kind": "deliverable"})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "pdf-work-model", "tool_calling": true}}})
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				body, _ := io.ReadAll(r.Body)
				if modelCalls == 0 && (!strings.Contains(string(body), core.ToolWorkspaceRunCommand) || !strings.Contains(string(body), core.ToolArtifactCapture)) {
					t.Fatalf("work scene did not expose governed command and capture tools: %s", body)
				}
				modelCalls++
				message := map[string]any{"role": "assistant", "content": "PDF 已生成并登记。"}
				switch modelCalls {
				case 1:
					message = map[string]any{"role": "assistant", "content": "先创建转换脚本。", "tool_calls": []map[string]any{
						transitionPlanToolCall("pdf-plan", "生成并交付 PDF", "完成 PDF 生成、验证和不可变交付登记。"),
						{"id": "write-pdf-script", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(writeArguments)}},
					}}
				case 2:
					message = map[string]any{"role": "assistant", "content": "执行转换脚本。", "tool_calls": []map[string]any{{"id": "run-pdf-script", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceRunCommand, "arguments": string(commandArguments)}}}}
				case 3:
					message = map[string]any{"role": "assistant", "content": "登记脚本生成的 PDF。", "tool_calls": []map[string]any{{"id": "capture-pdf", "type": "function", "function": map[string]any{"name": core.ToolArtifactCapture, "arguments": string(captureArguments)}}}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("pdf-model-%d", modelCalls), "choices": []map[string]any{{"message": message}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
			}))
			defer provider.Close()

			repository, databasePath := newRepository(t)
			defer repository.Close()
			bootstrapRepository(t, repository)
			service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "PDF User"}, core.Organization{ID: testOrgID, Name: "PDF Org"}, func() string { return "fake-pdf-key" })
			project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Work PDF", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
			if err != nil {
				t.Fatal(err)
			}
			asset, err := service.UploadAsset(t.Context(), project.ID, core.UploadAssetInput{Name: "source.md", MediaType: "text/markdown", Visibility: core.VisibilityPrivate}, strings.NewReader("# Source\n"))
			if err != nil {
				t.Fatal(err)
			}
			task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "把附件 Markdown 转 PDF 并导出 output.pdf", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: permissionMode, ContextAssetIDs: []string{asset.ID}}, "pdf-task-"+permissionMode, "")
			if err != nil {
				t.Fatal(err)
			}
			executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
			if err != nil {
				t.Fatal(err)
			}
			worker := okruntime.NewWorker("pdf-worker-"+permissionMode, repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-pdf-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
			approvalSeen := false
			for attempt := 0; attempt < 16; attempt++ {
				worked, runErr := worker.RunOnce(t.Context())
				if runErr != nil {
					t.Fatalf("worker attempt %d: %v", attempt, runErr)
				}
				approvals, listErr := service.ListApprovals(t.Context(), core.ApprovalPending)
				if listErr != nil {
					t.Fatal(listErr)
				}
				if len(approvals) > 0 {
					approvalSeen = true
					if permissionMode == core.PermissionModeFullAccess {
						t.Fatalf("full access created L2 approval: %#v", approvals)
					}
					if _, err := service.DecideApproval(t.Context(), approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeOnce}, "pdf-approval", ""); err != nil {
						t.Fatal(err)
					}
				}
				current, getErr := service.GetTask(t.Context(), task.ID)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if current.Status == core.StatusSucceeded || current.Status == core.StatusFailed || current.Status == core.StatusBlocked {
					task = current
					break
				}
				if !worked && len(approvals) == 0 {
					t.Fatalf("worker became idle before terminal state: %#v", current)
				}
			}
			if task.Status != core.StatusSucceeded {
				events, _ := service.ListRunEvents(t.Context(), task.RunID, 0)
				artifacts, _ := service.ListRunArtifacts(t.Context(), task.RunID)
				eventTypes := make([]string, 0, len(events))
				for _, event := range events {
					eventTypes = append(eventTypes, event.EventType)
				}
				workspacePDF, workspaceErr := os.ReadFile(filepath.Join(filepath.Dir(databasePath), "workspaces", project.ID, "output.pdf"))
				t.Fatalf("pdf status=%s error=%s model_calls=%d artifacts=%#v workspace_pdf_bytes=%d workspace_err=%v event_types=%v", task.Status, task.ErrorCode, modelCalls, artifacts, len(workspacePDF), workspaceErr, eventTypes)
			}
			if (permissionMode == core.PermissionModeAsk) != approvalSeen {
				t.Fatalf("permission=%s approval_seen=%v", permissionMode, approvalSeen)
			}
			artifacts, err := service.ListProjectArtifacts(t.Context(), project.ID, application.ProjectArtifactQuery{RunID: task.RunID})
			if err != nil {
				t.Fatal(err)
			}
			pdfIndex := slices.IndexFunc(artifacts, func(artifact core.Artifact) bool {
				return artifact.Name == "output.pdf" && artifact.MediaType == "application/pdf" && artifact.Kind == "deliverable"
			})
			if pdfIndex < 0 {
				t.Fatalf("verified PDF artifact missing: %#v", artifacts)
			}
			artifact, content, err := service.OpenArtifactContent(context.Background(), artifacts[pdfIndex].ID)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(content)
			_ = content.Close()
			actualHash := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
			if readErr != nil || !strings.HasPrefix(string(body), "%PDF-") || actualHash != artifact.ContentHash {
				t.Fatalf("pdf body=%q hash=%s artifact=%#v err=%v", body, actualHash, artifact, readErr)
			}
			events, err := service.ListRunEvents(t.Context(), task.RunID, 0)
			if err != nil || !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "artifact.created" }) || !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "validation.completed" }) {
				t.Fatalf("pdf events=%#v err=%v", events, err)
			}
			receipts, err := service.ListSideEffectReceipts(t.Context(), core.SideEffectReceiptSucceeded)
			if err != nil || len(receipts) == 0 {
				t.Fatalf("command receipt missing: %#v err=%v", receipts, err)
			}
		})
	}
}

func TestExplicitWorkspacePDFCaptureRepairsHistoricalRunOwnership(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "", core.Actor{ID: testUserID, DisplayName: "PDF User"}, core.Organization{ID: testOrgID, Name: "PDF Org"}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Historical PDF", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "生成 historical.pdf", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: core.PermissionModeFullAccess}, "historical-pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(filepath.Dir(databasePath), "workspaces")
	executor, err := workspace.NewLocal(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := localblob.New(filepath.Join(filepath.Dir(databasePath), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := application.NewArtifactCaptureService(executor, blobs)
	if err != nil {
		t.Fatal(err)
	}
	service.WithChatToolExecutor(executor, workspaceRoot).WithArtifactCapture(capture)
	write := executor.Execute(t.Context(), project.ID, core.ToolCall{ID: "historical-pdf-write", Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "historical.pdf", "content": "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n"}})
	if write.Status != core.StatusSucceeded {
		t.Fatalf("write historical PDF: %#v", write)
	}
	artifact, err := service.CaptureWorkspaceArtifact(t.Context(), project.ID, application.CaptureWorkspaceArtifactInput{RunID: task.RunID, Path: "historical.pdf", Kind: "deliverable"}, "manual-capture", "capture-historical-pdf")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.RunID != task.RunID || artifact.Name != "historical.pdf" || artifact.MediaType != "application/pdf" || artifact.SourceType != "manual_workspace_capture" {
		t.Fatalf("unexpected manual Artifact: %#v", artifact)
	}
	replayed, err := service.CaptureWorkspaceArtifact(t.Context(), project.ID, application.CaptureWorkspaceArtifactInput{RunID: task.RunID, Path: "historical.pdf", Kind: "deliverable"}, "manual-capture-replay", "capture-historical-pdf-replay")
	if err != nil || replayed.ID != artifact.ID {
		t.Fatalf("capture must deduplicate the same Run/path/hash: replay=%#v err=%v", replayed, err)
	}
	artifacts, err := service.ListProjectArtifacts(t.Context(), project.ID, application.ProjectArtifactQuery{RunID: task.RunID, IncludeHistory: true})
	if err != nil || !slices.ContainsFunc(artifacts, func(item core.Artifact) bool { return item.ID == artifact.ID }) {
		t.Fatalf("captured PDF missing from task outputs: %#v err=%v", artifacts, err)
	}
	events, err := service.ListRunEvents(t.Context(), task.RunID, 0)
	if err != nil || !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "artifact.created" && event.Payload["source_type"] == "manual_workspace_capture"
	}) {
		t.Fatalf("manual capture audit event missing: %#v err=%v", events, err)
	}
	receipts, err := service.ListSideEffectReceipts(t.Context(), core.SideEffectReceiptSucceeded)
	if err != nil || !slices.ContainsFunc(receipts, func(receipt core.SideEffectReceipt) bool {
		return receipt.RunID == task.RunID && receipt.InputHash != ""
	}) {
		t.Fatalf("manual capture receipt missing: %#v err=%v", receipts, err)
	}
}
