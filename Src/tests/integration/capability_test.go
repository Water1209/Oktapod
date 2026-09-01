package integration_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestCapabilityManifestSignaturePolicy(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"description": "signed skill"}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"description": "signed skill",
		"_oktapod_signature": map[string]any{
			"algorithm": "ed25519",
			"key_id":    "release-1",
			"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical)),
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithCapabilityTrustedKeys(map[string]ed25519.PublicKey{"release-1": publicKey})
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Signed capability project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "signed", ManifestJSON: string(manifestJSON)}, "capability-signed")
	if err != nil {
		t.Fatalf("create signed capability: %v", err)
	}
	if signed.Status != core.CapabilityStatusEnabled || signed.RiskLevel != "low" {
		t.Fatalf("trusted signed capability=%#v", signed)
	}
	unknownService := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	unknown, err := unknownService.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "unknown", ManifestJSON: string(manifestJSON)}, "capability-unknown")
	if err != nil {
		t.Fatalf("create unknown-key capability: %v", err)
	}
	if unknown.Status != core.CapabilityStatusQuarantined || unknown.RiskLevel != "untrusted-signature" {
		t.Fatalf("unknown-key capability=%#v", unknown)
	}
	manifest["description"] = "tampered"
	tamperedJSON, _ := json.Marshal(manifest)
	if _, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "tampered", ManifestJSON: string(tamperedJSON)}, "capability-tampered"); err == nil {
		t.Fatal("tampered capability signature was accepted")
	}
}

func TestCapabilityManifestPreflightQuarantinesRiskyDeclarations(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Capability project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "shell-like", ManifestJSON: `{"description":"write workspace files","instructions":"Create the requested file.","permissions":["workspace.write"]}`}, "capability-1")
	if err != nil {
		t.Fatalf("create capability: %v", err)
	}
	if capability.Status != core.CapabilityStatusQuarantined || capability.ContentHash == "" || capability.RiskLevel != "review" {
		t.Fatalf("capability=%#v", capability)
	}
	if len(capability.PermissionDiff) != 1 || capability.PermissionDiff[0] != "permissions:workspace.write" {
		t.Fatalf("permission diff=%#v", capability.PermissionDiff)
	}
	if _, err := service.SetCapabilityStatus(context.Background(), capability.ID, core.CapabilityStatusEnabled, "capability-bypass"); err == nil {
		t.Fatal("quarantined capability must not be enabled through the generic status endpoint")
	}
	approved, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "reviewed workspace permission"}, "capability-review", "capability-review-key")
	if err != nil {
		t.Fatalf("review capability: %v", err)
	}
	if approved.Status != core.CapabilityStatusEnabled {
		t.Fatalf("approved capability=%#v", approved)
	}
	if _, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved}, "capability-review-replay", "capability-review-replay"); err == nil {
		t.Fatal("reviewing an already decided capability should conflict")
	}
	rejected, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "rejected", ManifestJSON: `{"description":"read workspace files","permissions":["workspace.read"]}`}, "capability-rejected")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err = service.ReviewCapability(context.Background(), rejected.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewRejected, Note: "not approved for this project"}, "capability-reject-review", "capability-reject-review")
	if err != nil || rejected.Status != core.CapabilityStatusDisabled {
		t.Fatalf("rejected capability=%#v err=%v", rejected, err)
	}
	if _, err := service.SetCapabilityStatus(context.Background(), rejected.ID, core.CapabilityStatusEnabled, "capability-rejected-bypass"); err == nil {
		t.Fatal("rejected capability must not be re-enabled without a new reviewed version")
	}
	if _, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindPlugin, Name: "unsafe", ManifestJSON: `{"exec":"rm -rf"}`}, "capability-2"); err == nil {
		t.Fatal("unsafe manifest should be rejected")
	}
	items, err := service.ListCapabilities(context.Background(), project.ID)
	items = customCapabilities(items)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}
