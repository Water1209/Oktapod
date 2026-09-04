package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestUpdateProjectSettingsPersistsNameAndDescription(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000801", DisplayName: "Project Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000802", Name: "Project Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Before Rename", Description: "before", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.UpdateProjectSettings(ctx, project.ID, core.UpdateProjectSettingsInput{
		Name:        stringPointer("After Rename"),
		Description: stringPointer("after"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "After Rename" || updated.Description != "after" {
		t.Fatalf("updated project=%#v", updated)
	}

	persisted, err := service.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Name != "After Rename" || persisted.Description != "after" {
		t.Fatalf("rename was not persisted: %#v", persisted)
	}
}

func stringPointer(value string) *string { return &value }
