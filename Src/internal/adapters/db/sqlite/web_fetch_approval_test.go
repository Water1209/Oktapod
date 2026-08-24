package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestPrivateWebFetchApprovalIsRunOriginScoped(t *testing.T) {
	ctx := context.Background()
	store, service, scope, projectID, policy := setupWebFetchApprovalStore(t, ctx)
	task := createWebFetchApprovalTask(t, ctx, service, projectID, core.PermissionModeAsk)

	status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3000/docs", "first")
	if status != core.StatusWaitingApproval {
		t.Fatalf("first private fetch status=%s", status)
	}
	approvals, err := store.ListApprovals(ctx, scope, core.ApprovalPending, 10)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	approval := approvals[0]
	if approval.ApprovalScope != core.ApprovalScopeRunOrigin || approval.TargetOrigin != "http://127.0.0.1:3000" || approval.MatchedRuleID != "preview" || approval.PolicyHash == "" {
		t.Fatalf("private approval=%#v", approval)
	}
	if _, err := store.DecideApproval(ctx, scope, approval.ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeOnce}, "approve-once", core.Idempotency{}); err == nil {
		t.Fatal("private fetch accepted one-time approval instead of run_origin")
	}
	if _, err := store.DecideApproval(ctx, scope, approval.ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeRunOrigin}, "approve-origin", core.Idempotency{}); err != nil {
		t.Fatal(err)
	}
	var rules int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_approval_rules WHERE run_id = ?`, task.RunID).Scan(&rules); err != nil || rules != 1 {
		t.Fatalf("run rules=%d err=%v", rules, err)
	}
	assertWebFetchReceiptAuthority(t, ctx, store, task.RunID, "http://127.0.0.1:3000/docs")

	if status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3000/other", "same-origin"); status != core.StatusQueued {
		t.Fatalf("same origin required another approval: %s", status)
	}
	if status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3001/other", "different-port"); status != core.StatusWaitingApproval {
		t.Fatalf("different port reused origin approval: %s", status)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM approval_requests WHERE run_id = ?`, task.RunID).Scan(&rules); err != nil || rules != 2 {
		t.Fatalf("approval request count=%d err=%v", rules, err)
	}

	// A new Run freezes the same service policy but never inherits the prior
	// Run's durable authorization.
	service.WithWebAccessPolicy(policy)
	second := createWebFetchApprovalTask(t, ctx, service, projectID, core.PermissionModeAsk)
	if status := planWebFetchForTest(t, ctx, store, second.RunID, "http://127.0.0.1:3000/docs", "new-run"); status != core.StatusWaitingApproval {
		t.Fatalf("new Run inherited private origin authority: %s", status)
	}
}

func TestPrivateWebFetchIgnoresFullAccessAndAutoReviewer(t *testing.T) {
	for _, permission := range []string{core.PermissionModeFullAccess, core.PermissionModeAutoReview} {
		t.Run(permission, func(t *testing.T) {
			ctx := context.Background()
			store, service, _, projectID, _ := setupWebFetchApprovalStore(t, ctx)
			task := createWebFetchApprovalTask(t, ctx, service, projectID, permission)
			if status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3000/docs", permission); status != core.StatusWaitingApproval {
				t.Fatalf("permission %s bypassed human approval: %s", permission, status)
			}
			var status, reviewer string
			if err := store.db.QueryRow(`SELECT status, reviewer FROM human_action_requests WHERE run_id = ? AND kind = 'tool_approval'`, task.RunID).Scan(&status, &reviewer); err != nil {
				t.Fatal(err)
			}
			if status != core.HumanActionWaitingUser || reviewer != core.HumanActionReviewerUser {
				t.Fatalf("permission=%s action status=%s reviewer=%s", permission, status, reviewer)
			}
		})
	}
}

func TestPrivateWebFetchPolicyChangeInvalidatesGrantWithoutExpandingRun(t *testing.T) {
	ctx := context.Background()
	store, service, scope, projectID, policy := setupWebFetchApprovalStore(t, ctx)
	task := createWebFetchApprovalTask(t, ctx, service, projectID, core.PermissionModeAsk)
	if status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3000/first", "before-policy-change"); status != core.StatusWaitingApproval {
		t.Fatalf("initial fetch status=%s", status)
	}
	approvals, err := store.ListApprovals(ctx, scope, core.ApprovalPending, 10)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	oldHash := approvals[0].PolicyHash
	if _, err := store.DecideApproval(ctx, scope, approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeRunOrigin}, "approve-before-change", core.Idempotency{}); err != nil {
		t.Fatal(err)
	}

	expanded, err := (core.WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: append(append([]core.WebPrivateOrigin(nil), policy.AllowedOrigins...), core.WebPrivateOrigin{ID: "new-service", Origin: "http://127.0.0.1:3002"})}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	store.SetExecutionPolicy(application.StaticExecutionPolicy{WebAccess: expanded})
	if status := planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3000/after-change", "changed-policy"); status != core.StatusWaitingApproval {
		t.Fatalf("old origin did not require a fresh approval after policy change: %s", status)
	}
	approvals, err = store.ListApprovals(ctx, scope, core.ApprovalPending, 10)
	if err != nil || len(approvals) != 1 || approvals[0].PolicyHash == oldHash {
		t.Fatalf("fresh policy approval=%#v err=%v", approvals, err)
	}
	_ = planWebFetchForTest(t, ctx, store, task.RunID, "http://127.0.0.1:3002/new", "new-origin-old-run")
	var toolStatus, policyDecision string
	if err := store.db.QueryRow(`SELECT status, policy_decision FROM tool_calls WHERE run_id = ? ORDER BY position DESC LIMIT 1`, task.RunID).Scan(&toolStatus, &policyDecision); err != nil {
		t.Fatal(err)
	}
	if toolStatus != "rejected" || policyDecision != core.PolicyDeny {
		t.Fatalf("new origin expanded an existing Run: tool_status=%s decision=%s", toolStatus, policyDecision)
	}
}

func TestWebAccessConfigurationOriginsAreOwnerOrAdminOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner := core.Actor{ID: "00000000-0000-7000-8000-000000000711", DisplayName: "Configuration Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000712", Name: "Configuration Org"}
	if err := store.Bootstrap(ctx, owner, organization); err != nil {
		t.Fatal(err)
	}
	manager := &webConfigurationManager{configuration: core.BootstrapConfiguration{WebAccess: core.WebAccessConfiguration{PrivateFetch: core.PrivateFetchConfiguration{
		Enabled: true, AllowedOrigins: []core.WebPrivateOrigin{{ID: "wiki", Origin: "http://10.0.0.10:8080"}}, AllowedOriginCount: 1,
	}}}}
	service := application.NewService(store, core.ProfileTeam, "https://provider.invalid/v1", owner, organization, func() string { return "" }).WithBootstrapConfiguration(manager)

	ownerView, err := service.Configuration(ctx)
	if err != nil || len(ownerView.WebAccess.PrivateFetch.AllowedOrigins) != 1 || ownerView.WebAccess.PrivateFetch.OriginsRedacted {
		t.Fatalf("owner view=%#v err=%v", ownerView.WebAccess.PrivateFetch, err)
	}
	memberID := "00000000-0000-7000-8000-000000000713"
	now := formatTime(time.Now())
	if _, err := store.db.Exec(`INSERT INTO users (id, display_name, created_at, updated_at) VALUES (?, 'Member', ?, ?)`, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, organization.ID, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	memberCtx := application.WithPrincipal(ctx, application.Principal{OrganizationID: organization.ID, UserID: memberID, AuthenticationMethod: application.AuthenticationSession})
	memberView, err := service.Configuration(memberCtx)
	if err != nil || len(memberView.WebAccess.PrivateFetch.AllowedOrigins) != 0 || !memberView.WebAccess.PrivateFetch.OriginsRedacted || memberView.WebAccess.PrivateFetch.AllowedOriginCount != 1 {
		t.Fatalf("member view=%#v err=%v", memberView.WebAccess.PrivateFetch, err)
	}
	if _, err := service.UpdateBootstrapConfiguration(memberCtx, core.UpdateBootstrapConfiguration{}); err == nil {
		t.Fatal("ordinary member modified bootstrap configuration")
	}
	adminCtx := application.WithPrincipal(ctx, application.Principal{OrganizationID: organization.ID, UserID: memberID, AuthenticationMethod: application.AuthenticationAdminToken, Administrator: true})
	adminView, err := service.Configuration(adminCtx)
	if err != nil || len(adminView.WebAccess.PrivateFetch.AllowedOrigins) != 1 || adminView.WebAccess.PrivateFetch.OriginsRedacted {
		t.Fatalf("admin view=%#v err=%v", adminView.WebAccess.PrivateFetch, err)
	}
}

type webConfigurationManager struct {
	configuration core.BootstrapConfiguration
}

func (manager *webConfigurationManager) Snapshot() core.BootstrapConfiguration {
	return manager.configuration
}

func (manager *webConfigurationManager) Update(input core.UpdateBootstrapConfiguration) (core.BootstrapConfiguration, error) {
	return manager.configuration, nil
}

func setupWebFetchApprovalStore(t *testing.T, ctx context.Context) (*Store, *application.Service, core.Scope, string, core.WebAccessPolicy) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000701", DisplayName: "Web Approval Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000702", Name: "Web Approval Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	policy, err := (core.WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []core.WebPrivateOrigin{
		{ID: "preview", Origin: "http://127.0.0.1:3000"}, {ID: "preview-alt", Origin: "http://127.0.0.1:3001"},
	}}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	store.SetExecutionPolicy(application.StaticExecutionPolicy{WebAccess: policy})
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" }).WithWebAccessPolicy(policy)
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Web Approval", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	return store, service, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, project.ID, policy
}

func createWebFetchApprovalTask(t *testing.T, ctx context.Context, service *application.Service, projectID, permission string) core.Task {
	t.Helper()
	task, err := service.CreateTask(ctx, projectID, core.CreateTaskInput{Goal: "read a governed web source", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: permission}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func planWebFetchForTest(t *testing.T, ctx context.Context, store *Store, runID, rawURL, label string) string {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	run, err := getRunContextTx(ctx, tx, runID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	now := formatTime(time.Now().UTC())
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, runID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'model_response', ?, 'succeeded', '{}', ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, runID, position, now, now); err != nil {
		t.Fatal(err)
	}
	modelStep := core.ClaimedStep{ID: stepID, RunID: runID, StepType: "model_response", InputJSON: "{}", OwnerID: "web-planner-" + label}
	calls := []core.ToolCall{{Name: core.ToolWebFetch, Arguments: map[string]any{"url": rawURL}}}
	if planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, runID); planRevisionID == "" || planItemID == "" {
		calls = append([]core.ToolCall{{
			Name: core.ToolReplacePlan,
			Arguments: map[string]any{"items": []any{
				map[string]any{"title": "Read the governed web source", "description": "Fetch the requested private origin under the frozen Run policy."},
			}},
		}}, calls...)
	}
	status, _, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, calls, now, "web-plan-"+label)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return status
}

func assertWebFetchReceiptAuthority(t *testing.T, ctx context.Context, store *Store, runID, rawURL string) {
	t.Helper()
	var stepID, toolCallID, inputHash string
	if err := store.db.QueryRow(`
		SELECT step_id, id, arguments_hash FROM tool_calls
		WHERE run_id = ? AND tool_name = ? AND status = 'queued'
		ORDER BY position LIMIT 1`, runID, core.ToolWebFetch).Scan(&stepID, &toolCallID, &inputHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE steps SET status = 'running', owner_id = 'web-worker', fence_token = 1 WHERE id = ?`, stepID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, runID)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	step := core.ClaimedStep{ID: stepID, RunID: runID, StepType: core.ToolWebFetch, OwnerID: "web-worker", FenceToken: 1}
	if _, err := store.BeginToolExecution(ctx, step, run, core.ToolCall{ID: toolCallID, Name: core.ToolWebFetch, Arguments: map[string]any{"url": rawURL}}, inputHash, true, "web-begin"); err != nil {
		t.Fatal(err)
	}
	var networkClass, matchedRuleID, policyHash, authorizationID string
	if err := store.db.QueryRow(`SELECT network_class, matched_rule_id, policy_hash, authorization_id FROM side_effect_receipts WHERE tool_call_id = ?`, toolCallID).Scan(&networkClass, &matchedRuleID, &policyHash, &authorizationID); err != nil {
		t.Fatal(err)
	}
	if networkClass != "private" || matchedRuleID != "preview" || policyHash == "" || authorizationID == "" {
		t.Fatalf("receipt authority: class=%s rule=%s policy=%s authorization=%s", networkClass, matchedRuleID, policyHash, authorizationID)
	}
}
