package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestHTTPAssetMultipartAndDownloadContract(t *testing.T) {
	t.Parallel()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	project := postJSONWithKey[struct {
		ID string `json:"id"`
	}](t, server.URL+"/api/v1/projects", map[string]any{"name": "HTTP assets", "kind": "personal", "scene": "work"}, "http-assets-project-key")
	folder := postJSONWithKey[struct {
		ID string `json:"id"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/asset-folders", map[string]any{"name": "文档", "visibility": "project"}, "http-assets-folder-key")
	content := []byte("multipart upload remains binary-safe\x00\xff")
	upload := func() (*http.Response, error) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("visibility", "project")
		_ = writer.WriteField("parent_id", folder.ID)
		part, err := writer.CreateFormFile("file", "二进制.dat")
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(content); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/projects/"+project.ID+"/assets", &body)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", writer.FormDataContentType())
		return http.DefaultClient.Do(request)
	}
	response, err := upload()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	var asset struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		ContentHash string `json:"content_hash"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&asset); err != nil {
		t.Fatal(err)
	}
	if asset.ID == "" || asset.Name != "二进制.dat" || asset.ContentHash == "" || asset.SizeBytes != int64(len(content)) {
		t.Fatalf("asset=%#v", asset)
	}
	listed := getJSON[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		VisibleBytes int64 `json:"visible_bytes"`
		QuotaBytes   int64 `json:"quota_bytes"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/assets?parent_id="+folder.ID+"&q=%E4%BA%8C%E8%BF%9B%E5%88%B6&kind=file")
	if len(listed.Items) != 1 || listed.Items[0].ID != asset.ID || listed.VisibleBytes != int64(len(content)) || listed.QuotaBytes != application.ProjectAssetQuota {
		t.Fatalf("listed=%#v", listed)
	}
	download, err := http.Get(server.URL + "/api/v1/assets/" + asset.ID + "/content")
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	downloaded, err := io.ReadAll(download.Body)
	if err != nil || download.StatusCode != http.StatusOK || !bytes.Equal(downloaded, content) || download.Header.Get("X-Content-SHA256") != asset.ContentHash || download.Header.Get("Content-Disposition") == "" {
		t.Fatalf("download status=%d headers=%#v body=%q err=%v", download.StatusCode, download.Header, downloaded, err)
	}
	duplicate, err := upload()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Body.Close()
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(duplicate.Body).Decode(&problem)
	if duplicate.StatusCode != http.StatusConflict || problem.Code != "RESOURCE_CONFLICT" {
		t.Fatalf("duplicate status=%d problem=%#v", duplicate.StatusCode, problem)
	}

	markdown := []byte("# 可直接打开的结果\n\n- 浏览器内预览\n")
	markdownAsset, err := service.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "执行结果.md", Visibility: core.VisibilityProject, MediaType: "text/markdown", SizeHint: int64(len(markdown))}, bytes.NewReader(markdown))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := http.Get(server.URL + "/api/v1/assets/" + markdownAsset.ID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	previewed, readErr := io.ReadAll(preview.Body)
	_ = preview.Body.Close()
	if readErr != nil || preview.StatusCode != http.StatusOK || !bytes.Equal(previewed, markdown) || preview.Header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(preview.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("markdown preview status=%d headers=%#v body=%q err=%v", preview.StatusCode, preview.Header, previewed, readErr)
	}
	authenticatedServer := httptest.NewServer(httpapi.NewHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer authenticatedServer.Close()
	unauthenticated, err := http.Get(authenticatedServer.URL + "/api/v1/assets/" + markdownAsset.ID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("preview bypassed session middleware: status=%d", unauthenticated.StatusCode)
	}
}

func TestProjectAssetsStreamSearchAuthorizeAndPublishArtifact(t *testing.T) {
	t.Parallel()

	provider := newToolMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	ownerService := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := ownerService.CreateProject(context.Background(), core.CreateProjectInput{Name: "Asset project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	folder, err := ownerService.CreateAssetFolder(context.Background(), project.ID, core.CreateAssetFolderInput{Name: "资料", Visibility: core.VisibilityProject}, "asset-folder-key")
	if err != nil || folder.Kind != core.AssetKindFolder {
		t.Fatalf("folder=%#v err=%v", folder, err)
	}
	content := []byte("asset content survives a streamed round trip\n")
	privateAsset, err := ownerService.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "说明.txt", ParentID: folder.ID, Visibility: core.VisibilityPrivate, MediaType: "text/plain; charset=utf-8", SizeHint: int64(len(content))}, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if privateAsset.Kind != core.AssetKindFile || privateAsset.ContentHash == "" || privateAsset.SizeBytes != int64(len(content)) {
		t.Fatalf("asset metadata=%#v", privateAsset)
	}
	opened, reader, err := ownerService.OpenAssetContent(context.Background(), privateAsset.ID)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !bytes.Equal(roundTrip, content) || opened.ContentHash != privateAsset.ContentHash {
		t.Fatalf("download=%q opened=%#v err=%v", roundTrip, opened, readErr)
	}
	if _, err := ownerService.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "说明.txt", ParentID: folder.ID, Visibility: core.VisibilityPrivate, MediaType: "text/plain", SizeHint: int64(len(content))}, bytes.NewReader(content)); err == nil {
		t.Fatal("duplicate asset name unexpectedly succeeded")
	} else {
		var conflict *core.ResourceConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("duplicate error=%T %v, want ResourceConflictError", err, err)
		}
	}
	insideFolder, err := ownerService.ListAssets(context.Background(), project.ID, folder.ID, "", "")
	if err != nil || len(insideFolder.Items) != 1 || insideFolder.Items[0].ID != privateAsset.ID || insideFolder.VisibleBytes != int64(len(content)) || insideFolder.QuotaBytes != application.ProjectAssetQuota {
		t.Fatalf("folder collection=%#v err=%v", insideFolder, err)
	}
	search, err := ownerService.ListAssets(context.Background(), project.ID, "", "说明", core.AssetKindFile)
	if err != nil || len(search.Items) != 1 || search.Items[0].ID != privateAsset.ID {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	if _, err := ownerService.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "too-large.bin", Visibility: core.VisibilityPrivate, SizeHint: application.MaxAssetFileBytes + 1}, bytes.NewReader(nil)); err == nil {
		t.Fatal("oversized upload hint unexpectedly succeeded")
	}
	sharedContent := []byte("visible to active project members")
	sharedAsset, err := ownerService.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "共享.txt", Visibility: core.VisibilityProject, MediaType: "text/plain", SizeHint: int64(len(sharedContent))}, bytes.NewReader(sharedContent))
	if err != nil {
		t.Fatal(err)
	}

	memberID := "00000000-0000-7000-8000-000000000171"
	if err := repository.Bootstrap(context.Background(), core.Actor{ID: memberID, DisplayName: "Member"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, project.ID, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	memberService := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: memberID, DisplayName: "Member"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	memberSearch, err := memberService.ListAssets(context.Background(), project.ID, "", "说明", "")
	if err != nil || len(memberSearch.Items) != 0 {
		t.Fatalf("private asset leaked through search: %#v err=%v", memberSearch, err)
	}
	if _, reader, err := memberService.OpenAssetContent(context.Background(), privateAsset.ID); !errors.Is(err, sql.ErrNoRows) || reader != nil {
		t.Fatalf("private content access err=%v reader=%v", err, reader)
	}
	_, sharedReader, err := memberService.OpenAssetContent(context.Background(), sharedAsset.ID)
	if err != nil {
		t.Fatalf("project asset not visible to member: %v", err)
	}
	_ = sharedReader.Close()

	task, err := ownerService.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Create a result artifact", Acceptance: "Result is persisted", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: core.PermissionModeFullAccess}, "00000000-0000-7000-8000-000000000172", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("asset-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	for attempt := 0; attempt < 6; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil {
			t.Fatalf("worker attempt %d err=%v", attempt, runErr)
		} else if !worked {
			break
		}
	}
	artifacts, err := ownerService.ListRunArtifacts(context.Background(), task.RunID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	projectArtifacts, err := ownerService.ListProjectArtifacts(context.Background(), project.ID, application.ProjectArtifactQuery{RunID: task.RunID})
	if err != nil || len(projectArtifacts) != 1 || projectArtifacts[0].ID != artifacts[0].ID {
		t.Fatalf("project artifacts=%#v err=%v", projectArtifacts, err)
	}
	memberArtifacts, err := memberService.ListProjectArtifacts(context.Background(), project.ID, application.ProjectArtifactQuery{RunID: task.RunID})
	if err != nil || len(memberArtifacts) != 0 {
		t.Fatalf("private project artifact leaked to member: %#v err=%v", memberArtifacts, err)
	}
	published, err := ownerService.PublishArtifact(context.Background(), artifacts[0].ID, core.PublishArtifactInput{Name: "任务结果.md", Visibility: core.VisibilityProject}, "publish-artifact-key")
	if err != nil {
		t.Fatal(err)
	}
	if published.SourceType != core.AssetSourceArtifact || published.SourceArtifactID != artifacts[0].ID || published.ContentHash != artifacts[0].ContentHash {
		t.Fatalf("published asset=%#v artifact=%#v", published, artifacts[0])
	}
}
