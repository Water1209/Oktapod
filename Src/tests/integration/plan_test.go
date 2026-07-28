package integration_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	_ "modernc.org/sqlite"
)

func TestPlanChangesWriteScopedAuditEvents(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Plan audit", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreatePlanItem(context.Background(), project.ID, core.CreatePlanItemInput{Title: "Audited plan", Visibility: core.VisibilityPrivate}, "plan-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.MovePlanItem(context.Background(), item.ID, core.MovePlanItemInput{Status: core.PlanStatusInProgress}, "plan-move"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT action, detail_json FROM audit_events WHERE project_id = ? AND target_id = ? ORDER BY occurred_at`, project.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action+":"+detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || !strings.HasPrefix(actions[0], "plan_item.created:") || !strings.HasPrefix(actions[1], "plan_item.status_changed:") {
		t.Fatalf("plan audit events=%v", actions)
	}
}
