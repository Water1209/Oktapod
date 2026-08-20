package runtime

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestGlobalSkillInvocableFromOtherProject(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000401", DisplayName: "Skill User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000402", Name: "Skill Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	projectA, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Install project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := service.CreateCapability(ctx, projectA.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "global-check", Version: "1.0.0", ManifestJSON: `{"description":"check","instructions":"run check."}`, ScopeType: core.CapabilityScopeGlobal}, "skill-create")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Use project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a conversation slash-command turn invoking the global skill from project B.
	_, err = service.SubmitProjectTurn(ctx, projectB.ID, core.ProjectTurnInput{Content: "/global-check production", Visibility: core.VisibilityPrivate, SkillID: skill.ID, SkillArgs: "production"}, "", "")
	if err != nil {
		t.Fatalf("global skill invocation failed: %v", err)
	}
}
