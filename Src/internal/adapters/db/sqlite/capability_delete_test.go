package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestDeleteCredentialFreeCapabilityIgnoresHistoricalBlockedRun(t *testing.T) {
	ctx := context.Background()
	store, service, _, projectID, _ := setupWebFetchApprovalStore(t, ctx)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "blocked-delete-fixture", "version": "1.0.0"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{"name": "snapshot", "inputSchema": map[string]any{"type": "object"}}}}})
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()

	capability, err := service.CreateCapability(ctx, projectID, core.CreateCapabilityInput{Kind: core.CapabilityKindMCP, Name: "blocked-delete", ManifestJSON: `{"transport":"streamable_http","url":"` + server.URL + `"}`}, "blocked-delete-capability")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(ctx, capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "test fixture"}, "blocked-delete-review", "blocked-delete-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConnectMCP(ctx, capability.ID, "blocked-delete-connect"); err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, projectID, core.CreateTaskInput{Goal: "freeze the MCP capability", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "blocked-delete-task", "blocked-delete-task")
	if err != nil {
		t.Fatal(err)
	}
	var snapshotCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_tool_snapshots WHERE run_id = ? AND capability_id = ?`, task.RunID, capability.ID).Scan(&snapshotCount); err != nil || snapshotCount == 0 {
		t.Fatalf("snapshot count=%d err=%v", snapshotCount, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE runs SET status = 'blocked' WHERE id = ?`, task.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET status = 'blocked' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE capabilities SET credential_ref = 'secret:blocked-delete' WHERE id = ?`, capability.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteCapability(ctx, capability.ID, "blocked-delete-protected"); err == nil {
		t.Fatal("credential-bearing capability was deleted while a blocked Run could still resume")
	} else {
		var conflict *core.StateConflictError
		if !errors.As(err, &conflict) || conflict.Status != "active_run" {
			t.Fatalf("credential-bearing blocked delete error=%T %v", err, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE capabilities SET credential_ref = '' WHERE id = ?`, capability.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteCapability(ctx, capability.ID, "blocked-delete-final"); err != nil {
		t.Fatalf("delete capability referenced only by a historical blocked Run: %v", err)
	}
}
