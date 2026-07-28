package integration_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestHTTPMVPExtensionsScheduleCapabilityMemory(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	project := postJSON[struct {
		ID string `json:"id"`
	}](t, server.URL+"/api/v1/projects", map[string]any{"name": "Extensions", "kind": "personal", "scene": "work"})
	schedule := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/schedules", map[string]any{"name": "Digest", "cron_expression": "0 9 * * *", "timezone": "UTC", "goal": "Summarize", "mode": "answer", "visibility": "private"}, "schedule-http-1")
	if schedule.ID == "" {
		t.Fatal("schedule id missing")
	}
	capability := postJSONWithKey[struct {
		ID             string   `json:"id"`
		Status         string   `json:"status"`
		RiskLevel      string   `json:"risk_level"`
		PermissionDiff []string `json:"permission_diff"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/capabilities", map[string]any{"kind": "skill", "name": "review", "manifest_json": `{"description":"review evidence","instructions":"Review the evidence.","permissions":["read"]}`}, "capability-http-1")
	if capability.ID == "" || capability.Status != "quarantined" || capability.RiskLevel != "review" || len(capability.PermissionDiff) != 1 || capability.PermissionDiff[0] != "permissions:read" {
		t.Fatalf("capability=%#v", capability)
	}
	reviewed := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/capabilities/"+capability.ID+"/review", map[string]any{"decision": "approved", "note": "approved in HTTP review flow"}, "capability-review-http-1")
	if reviewed.ID != capability.ID || reviewed.Status != "enabled" {
		t.Fatalf("reviewed capability=%#v", reviewed)
	}
	memory := postJSONWithKey[struct {
		ID         string `json:"id"`
		SourceHash string `json:"source_hash"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/memories", map[string]any{"kind": "manual", "title": "Rule", "content": "Keep scope", "visibility": "private", "source_refs": []string{"event-1"}}, "memory-http-1")
	if memory.ID == "" || memory.SourceHash == "" {
		t.Fatalf("memory=%#v", memory)
	}
	updatedMemory := patchJSONWithKey[struct {
		ID         string `json:"id"`
		Content    string `json:"content"`
		SourceHash string `json:"source_hash"`
	}](t, server.URL+"/api/v1/memories/"+memory.ID, map[string]any{"title": "Updated Rule", "content": "Keep scope and verify changes", "visibility": "private", "source_refs": []string{"event-2"}}, "memory-update-http-1")
	if updatedMemory.ID != memory.ID || updatedMemory.Content != "Keep scope and verify changes" || updatedMemory.SourceHash == memory.SourceHash {
		t.Fatalf("updated memory=%#v", updatedMemory)
	}
	inactiveMemory := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/memories/"+memory.ID+"/status", map[string]any{"status": "inactive"}, "memory-status-http-1")
	if inactiveMemory.ID != memory.ID || inactiveMemory.Status != "inactive" {
		t.Fatalf("inactive memory=%#v", inactiveMemory)
	}
	activeMemory := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/memories/"+memory.ID+"/status", map[string]any{"status": "active"}, "memory-status-http-2")
	if activeMemory.Status != "active" {
		t.Fatalf("active memory=%#v", activeMemory)
	}
	skillDraft := postJSONWithKey[struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/memories/"+memory.ID+"/promote", nil, "memory-promote-http-1")
	if skillDraft.ID == "" || skillDraft.Kind != "skill" || skillDraft.Status != "quarantined" {
		t.Fatalf("skill draft=%#v", skillDraft)
	}
	daily := postJSONWithKey[struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/memories/daily-summary", nil, "daily-http-1")
	if daily.ID == "" || daily.Kind != "daily_summary" {
		t.Fatalf("daily=%#v", daily)
	}
	deleteWithKey(t, server.URL+"/api/v1/memories/"+memory.ID, "memory-delete-http-1")
	receipts := getJSON[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/side-effect-receipts?status=unknown")
	if len(receipts.Items) != 0 {
		t.Fatalf("fresh project unexpectedly has unknown side effects: %#v", receipts)
	}
}
