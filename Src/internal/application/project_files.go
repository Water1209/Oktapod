package application

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const MaxWorkspaceFilePage = 200

type WorkspaceFile struct {
	Path          string
	Kind          string
	LifecycleRole string
	SizeBytes     int64
	ModifiedAt    time.Time
	GitStatus     string
}

const (
	WorkspaceLifecycleSource          = "source"
	WorkspaceLifecycleInput           = "input"
	WorkspaceLifecycleWorking         = "working"
	WorkspaceLifecycleTemporary       = "temporary"
	WorkspaceLifecycleCandidateOutput = "candidate_output"
)

type WorkspaceFilePage struct {
	Items      []WorkspaceFile
	NextCursor int
	HasMore    bool
}

type WorkspaceFileContent struct {
	Path      string
	MediaType string
	SizeBytes int64
	Body      io.ReadCloser
}

type ProjectArtifactQuery struct {
	RunID          string
	ConversationID string
	Kind           string
	Search         string
	Limit          int
	IncludeHistory bool
}

type ProjectArtifactRepository interface {
	ListProjectArtifacts(context.Context, core.Scope, string, ProjectArtifactQuery) ([]core.Artifact, error)
}

type CaptureWorkspaceArtifactInput struct {
	RunID string
	Path  string
	Kind  string
}

type WorkspaceArtifactRepository interface {
	RegisterWorkspaceArtifact(context.Context, core.Scope, string, string, ArtifactCaptureResult, string, core.Idempotency) (core.Artifact, error)
}

type bufferedWorkspaceFile struct {
	*bufio.Reader
	closer io.Closer
}

func (f *bufferedWorkspaceFile) Close() error { return f.closer.Close() }

func (s *Service) ListWorkspaceFiles(ctx context.Context, projectID string, cursor, limit int) (WorkspaceFilePage, error) {
	if s.workspaceReader == nil {
		return WorkspaceFilePage{}, &core.ResourceConflictError{Resource: "workspace files", Reason: "workspace reader is unavailable"}
	}
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID); err != nil {
		return WorkspaceFilePage{}, err
	}
	if cursor < 0 {
		return WorkspaceFilePage{}, &core.ValidationError{Field: "cursor", Message: "must not be negative"}
	}
	if limit <= 0 {
		limit = MaxWorkspaceFilePage
	}
	if limit > MaxWorkspaceFilePage {
		limit = MaxWorkspaceFilePage
	}
	// The adapter scans the project root under its own path and symlink guard.
	// Internal runtime files are filtered here so they never become public API.
	execution := s.workspaceReader.Execute(ctx, projectID, core.ToolCall{ID: "workspace-files", Name: core.ToolWorkspaceListFiles, Arguments: map[string]any{"path": "", "recursive": true, "cursor": float64(cursor), "limit": float64(limit)}})
	if execution.Status != core.StatusSucceeded {
		return WorkspaceFilePage{}, fmt.Errorf("list workspace files: %s", execution.ErrorCode)
	}
	gitStatuses := map[string]string{}
	statusExecution := s.workspaceReader.Execute(ctx, projectID, core.ToolCall{ID: "workspace-files-git", Name: core.ToolGitStatus})
	if statusExecution.Status == core.StatusSucceeded {
		gitStatuses = parseGitPorcelain(stringValueFromOutput(statusExecution.Output, "status"))
	}

	page := WorkspaceFilePage{Items: make([]WorkspaceFile, 0, limit)}
	items, _ := execution.Output["items"].([]map[string]any)
	for _, raw := range items {
		path := filepath.ToSlash(strings.TrimSpace(stringValueFromOutput(raw, "path")))
		if path == "" || isInternalWorkspacePath(path) {
			continue
		}
		kind := stringValueFromOutput(raw, "kind")
		item := WorkspaceFile{Path: path, Kind: kind, LifecycleRole: workspaceLifecycleRole(path, kind), SizeBytes: int64ValueFromOutput(raw, "size_bytes"), GitStatus: gitStatuses[path]}
		if value := stringValueFromOutput(raw, "modified_at"); value != "" {
			item.ModifiedAt, _ = time.Parse(time.RFC3339Nano, value)
		}
		page.Items = append(page.Items, item)
		if len(page.Items) == limit {
			break
		}
	}
	page.NextCursor = cursor + len(items)
	_, page.HasMore = execution.Output["next_cursor"]
	if len(page.Items) == limit && len(items) > len(page.Items) {
		page.HasMore = true
	}
	if !page.HasMore {
		page.NextCursor = 0
	}
	return page, nil
}

// workspaceLifecycleRole is a presentation contract, not a storage router.
// Explicit managed directories win; conservative legacy-name inference keeps
// existing workspaces understandable without moving or deleting user files.
func workspaceLifecycleRole(path, kind string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	lower := strings.ToLower(clean)
	parts := strings.Split(lower, "/")
	first, base := parts[0], filepath.Base(lower)
	for _, segment := range parts {
		switch segment {
		case "cache", ".cache", "node_modules", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".venv", "venv", ".tox", ".nox", "coverage", ".coverage":
			return WorkspaceLifecycleTemporary
		}
	}
	switch first {
	case "input", "inputs", "download", "downloads", "import", "imports", "reference", "references", "source-materials":
		return WorkspaceLifecycleInput
	case "output", "outputs", "deliverable", "deliverables", "export", "exports":
		return WorkspaceLifecycleCandidateOutput
	case "work", "working", "research", ".research", "img", "images", "chart", "charts", "figure", "figures":
		return WorkspaceLifecycleWorking
	case "tmp", "temp", ".tmp", ".temp", "pip":
		return WorkspaceLifecycleTemporary
	}
	if strings.HasPrefix(base, ".extract_") || strings.HasPrefix(base, ".generate_") || strings.HasPrefix(base, ".render_") || strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".log") {
		return WorkspaceLifecycleTemporary
	}
	if strings.HasPrefix(base, "extract_") || strings.HasPrefix(base, "pdf_extract") || strings.HasPrefix(base, "pdf_text") || strings.HasPrefix(base, "generate_") || strings.HasPrefix(base, "render_") || strings.HasPrefix(base, "convert_") {
		return WorkspaceLifecycleWorking
	}
	if kind == "file" {
		extension := strings.ToLower(filepath.Ext(base))
		switch extension {
		case ".pdf", ".docx", ".pptx", ".xlsx":
			return WorkspaceLifecycleCandidateOutput
		case ".html":
			if strings.Contains(base, "demo") || strings.Contains(base, "report") || strings.Contains(base, "报告") {
				return WorkspaceLifecycleCandidateOutput
			}
		case ".md":
			if strings.Contains(base, "report") || strings.Contains(base, "brief") || strings.Contains(base, "报告") || strings.Contains(base, "方案") || strings.Contains(base, "总结") || strings.Contains(base, "结果") || strings.Contains(base, "摘要") {
				return WorkspaceLifecycleCandidateOutput
			}
		}
	}
	return WorkspaceLifecycleSource
}

func (s *Service) OpenWorkspaceFile(ctx context.Context, projectID, path string) (WorkspaceFileContent, error) {
	if s.workspaceFiles == nil {
		return WorkspaceFileContent{}, &core.ResourceConflictError{Resource: "workspace files", Reason: "workspace file reader is unavailable"}
	}
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID); err != nil {
		return WorkspaceFileContent{}, err
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." || clean == "" || isInternalWorkspacePath(clean) {
		return WorkspaceFileContent{}, &core.ValidationError{Field: "path", Message: "must name one public project-relative file"}
	}
	source, err := s.workspaceFiles.OpenCaptureFile(ctx, projectID, clean, MaxArtifactCaptureBytes)
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	reader := bufio.NewReaderSize(source, 4096)
	prefix, _ := reader.Peek(512)
	mediaType, detectErr := DetectArtifactMediaType(source.RelativePath(), prefix)
	if detectErr != nil {
		mediaType = "application/octet-stream"
	}
	return WorkspaceFileContent{Path: source.RelativePath(), MediaType: mediaType, SizeBytes: source.SizeBytes(), Body: &bufferedWorkspaceFile{Reader: reader, closer: source}}, nil
}

func (s *Service) ListProjectArtifacts(ctx context.Context, projectID string, query ProjectArtifactQuery) ([]core.Artifact, error) {
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID); err != nil {
		return nil, err
	}
	query.RunID = strings.TrimSpace(query.RunID)
	query.ConversationID = strings.TrimSpace(query.ConversationID)
	query.Kind = strings.ToLower(strings.TrimSpace(query.Kind))
	query.Search = strings.TrimSpace(query.Search)
	if len([]rune(query.Search)) > 200 {
		return nil, &core.ValidationError{Field: "q", Message: "must not exceed 200 characters"}
	}
	if len([]rune(query.Kind)) > 100 {
		return nil, &core.ValidationError{Field: "kind", Message: "must not exceed 100 characters"}
	}
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 200
	}
	repository, ok := s.repository.(ProjectArtifactRepository)
	if !ok {
		return nil, &core.ResourceConflictError{Resource: "project artifacts", Reason: "artifact query is unavailable"}
	}
	return repository.ListProjectArtifacts(ctx, s.ScopeFor(ctx), projectID, query)
}

// CaptureWorkspaceArtifact is an explicit repair path for files that exist in
// the mutable Workspace but were not registered by their originating Run. The
// caller must name the Run, so Oktapod never guesses historical ownership.
func (s *Service) CaptureWorkspaceArtifact(ctx context.Context, projectID string, input CaptureWorkspaceArtifactInput, correlationID, idempotencyKey string) (core.Artifact, error) {
	input.RunID = strings.TrimSpace(input.RunID)
	input.Path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(input.Path)))
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	if input.RunID == "" {
		return core.Artifact{}, &core.ValidationError{Field: "run_id", Message: "is required"}
	}
	if input.Path == "." || input.Path == "" || isInternalWorkspacePath(input.Path) {
		return core.Artifact{}, &core.ValidationError{Field: "path", Message: "must name one public project-relative file"}
	}
	if input.Kind != "deliverable" && input.Kind != "evidence" {
		return core.Artifact{}, &core.ValidationError{Field: "kind", Message: "must be deliverable or evidence"}
	}
	if s.artifactCapture == nil {
		return core.Artifact{}, &core.ResourceConflictError{Resource: "artifact capture", Reason: "capture service is unavailable"}
	}
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID); err != nil {
		return core.Artifact{}, err
	}
	repository, ok := s.repository.(WorkspaceArtifactRepository)
	if !ok {
		return core.Artifact{}, &core.ResourceConflictError{Resource: "artifact capture", Reason: "artifact repository is unavailable"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Artifact{}, err
	}
	result, err := s.artifactCapture.Capture(ctx, projectID, input.Path, input.Kind)
	if err != nil {
		return core.Artifact{}, err
	}
	request := struct {
		ProjectID string                        `json:"project_id"`
		Input     CaptureWorkspaceArtifactInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return repository.RegisterWorkspaceArtifact(ctx, s.ScopeFor(ctx), projectID, input.RunID, result, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func parseGitPorcelain(status string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(status, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "##") || len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		path = filepath.ToSlash(strings.Trim(path, `"`))
		if path != "" {
			result[path] = code
		}
	}
	return result
}

func stringValueFromOutput(output map[string]any, key string) string {
	value, _ := output[key].(string)
	return value
}

func int64ValueFromOutput(output map[string]any, key string) int64 {
	switch value := output[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	default:
		return 0
	}
}
