package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestDailySummaryIsDeterministicAndPersistedWithoutModel(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { t.Fatal("daily summary must not request a model key"); return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Memory project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := service.CreateMemory(context.Background(), project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Review rule", Content: "Keep private events private", Visibility: core.VisibilityPrivate}, "memory-key-1")
	if err != nil || manual.SourceHash == "" {
		t.Fatalf("manual=%#v err=%v", manual, err)
	}
	daily, err := service.CreateDailySummary(context.Background(), project.ID, "daily-key-1")
	if err != nil {
		t.Fatalf("daily summary: %v", err)
	}
	if daily.Kind != "daily_summary" || daily.Content == "" || daily.SourceHash == "" {
		t.Fatalf("daily=%#v", daily)
	}
	replay, err := service.CreateDailySummary(context.Background(), project.ID, "daily-key-1")
	if err != nil || replay.ID != daily.ID {
		t.Fatalf("daily replay=%#v err=%v", replay, err)
	}
	backfill, err := service.CreateDailySummaryForDate(context.Background(), project.ID, "2026-07-17", "daily-backfill-1")
	if err != nil {
		t.Fatalf("daily backfill: %v", err)
	}
	backfillReplay, err := service.CreateDailySummaryForDate(context.Background(), project.ID, "2026-07-17", "daily-backfill-2")
	if err != nil || backfillReplay.ID != backfill.ID {
		t.Fatalf("daily date uniqueness replay=%#v first=%#v err=%v", backfillReplay, backfill, err)
	}
	items, err := service.ListMemories(context.Background(), project.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	matched, err := service.ListMemories(context.Background(), project.ID, "private")
	if err != nil || len(matched) != 1 || matched[0].Title != "Review rule" {
		t.Fatalf("keyword search=%#v err=%v", matched, err)
	}
}

func TestMemoryRejectsSensitiveMaterialBeforePersistence(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Sensitive memory", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateMemory(context.Background(), project.ID, core.CreateMemoryInput{Kind: "manual", Title: "secret", Content: "api_key=should-never-be-stored", Visibility: core.VisibilityPrivate}, "sensitive-memory")
	var validation *core.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestMemoryCanBeUpdatedAndDeletedWithAuthorizationAndIdempotency(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Memory lifecycle", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(context.Background(), project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Before", Content: "old content", Visibility: core.VisibilityPrivate, SourceRefsJSON: `["event-1"]`}, "memory-lifecycle-create")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateMemory(context.Background(), memory.ID, core.UpdateMemoryInput{Title: "After", Content: "new content", Visibility: core.VisibilityProject}, "memory-lifecycle-update")
	if err != nil || updated.Title != "After" || updated.Content != "new content" || updated.Visibility != core.VisibilityProject || updated.SourceRefsJSON != `[]` || updated.SourceHash == memory.SourceHash {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	replay, err := service.UpdateMemory(context.Background(), memory.ID, core.UpdateMemoryInput{Title: "After", Content: "new content", Visibility: core.VisibilityProject}, "memory-lifecycle-update")
	if err != nil || replay.ID != updated.ID || replay.SourceHash != updated.SourceHash {
		t.Fatalf("update replay=%#v err=%v", replay, err)
	}
	if _, err := service.UpdateMemory(context.Background(), memory.ID, core.UpdateMemoryInput{Title: "bad", Content: "api_key=must-not-store", Visibility: core.VisibilityPrivate}, "memory-lifecycle-sensitive"); err == nil {
		t.Fatal("sensitive update unexpectedly succeeded")
	}
	inactive, err := service.SetMemoryStatus(context.Background(), memory.ID, core.MemoryStatusInactive, "memory-lifecycle-inactive")
	if err != nil || inactive.Status != core.MemoryStatusInactive {
		t.Fatalf("inactive=%#v err=%v", inactive, err)
	}
	items, err := service.ListMemories(context.Background(), project.ID)
	if err != nil || len(items) != 1 || items[0].Status != core.MemoryStatusInactive {
		t.Fatalf("inactive owner list=%#v err=%v", items, err)
	}
	if _, err := service.PromoteMemoryToSkill(context.Background(), memory.ID, "memory-lifecycle-promote-inactive"); err == nil {
		t.Fatal("inactive memory unexpectedly promoted")
	}
	if _, err := service.SetMemoryStatus(context.Background(), memory.ID, core.MemoryStatusActive, "memory-lifecycle-active"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteMemory(context.Background(), memory.ID, "memory-delete-correlation", "memory-lifecycle-delete"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteMemory(context.Background(), memory.ID, "memory-delete-correlation", "memory-lifecycle-delete"); err != nil {
		t.Fatalf("delete replay=%v", err)
	}
	items, err = service.ListMemories(context.Background(), project.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("items after delete=%#v err=%v", items, err)
	}
}

func TestMemoryCanPromoteToQuarantinedSkillDraft(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Promotion project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(context.Background(), project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Release checklist", Content: "Always run tests and attach the evidence artifact.", Visibility: core.VisibilityPrivate}, "promotion-memory")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := service.PromoteMemoryToSkill(context.Background(), memory.ID, "promotion-skill")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Kind != core.CapabilityKindSkill || skill.Status != core.CapabilityStatusQuarantined || skill.ContentHash == "" || !strings.Contains(skill.ManifestJSON, memory.ID) {
		t.Fatalf("skill draft=%#v", skill)
	}
	replay, err := service.PromoteMemoryToSkill(context.Background(), memory.ID, "promotion-skill")
	if err != nil || replay.ID != skill.ID {
		t.Fatalf("promotion replay=%#v err=%v", replay, err)
	}
}
