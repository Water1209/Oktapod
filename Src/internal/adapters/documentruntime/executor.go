package documentruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	parseProfileVersion = "document-extraction-v1:ocr-auto:chi_sim+eng"
	cacheMaxBytes       = int64(1 << 30)
	cacheMaxAge         = 30 * 24 * time.Hour
	filePreviewTTL      = 20 * time.Minute
	filePreviewMax      = 256
)

type Executor struct {
	client    *Client
	state     application.DocumentStateRepository
	sources   application.DocumentSourceRepository
	workspace application.DocumentWorkspace
	capture   application.ArtifactCapture
	previewMu sync.Mutex
	previews  map[string]filePreviewGrant
}

type filePreviewGrant struct {
	scope       core.Scope
	name        string
	format      string
	expiresAt   time.Time
	pageHandles map[string]string
}

func NewExecutor(client *Client, state application.DocumentStateRepository, sources application.DocumentSourceRepository, workspace application.DocumentWorkspace, capture application.ArtifactCapture) (*Executor, error) {
	if client == nil || state == nil || sources == nil || workspace == nil || capture == nil {
		return nil, fmt.Errorf("document runtime client, state, sources, workspace, and capture are required")
	}
	executor := &Executor{client: client, state: state, sources: sources, workspace: workspace, capture: capture, previews: make(map[string]filePreviewGrant)}
	_ = executor.pruneCache(time.Now().UTC())
	return executor, nil
}

func (e *Executor) Health(ctx context.Context) application.DocumentRuntimeHealth {
	return e.client.Health(ctx)
}

func (e *Executor) Execute(ctx context.Context, run core.RunContext, call core.ToolCall) core.ToolExecution {
	planned := core.PlanTool(call)
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "DOCUMENT_RUNTIME_FAILED", ExitCode: -1, InputHash: planned.InputHash, Idempotent: true, Output: map[string]any{}}
	if planned.Decision == core.PolicyDeny {
		result.ErrorCode = "TOOL_POLICY_DENIED"
		result.Output["reason"] = planned.Reason
		return result
	}
	var output map[string]any
	var err error
	switch call.Name {
	case core.ToolDocumentOpen:
		output, err = e.open(ctx, run, call)
	case core.ToolDocumentRead:
		output, err = e.read(ctx, run, call)
	case core.ToolDocumentCreate:
		output, err = e.create(ctx, run, call, planned.InputHash)
	case core.ToolDocumentApply:
		output, err = e.apply(ctx, run, call, planned.InputHash)
	case core.ToolDocumentRender:
		output, err = e.render(ctx, run, call)
	case core.ToolDocumentExport:
		output, err = e.export(ctx, run, call)
	default:
		err = fmt.Errorf("document tool is not registered")
	}
	if err != nil {
		result.ErrorCode = documentErrorCode(err)
		result.Output = map[string]any{"reason": safeDocumentError(err), "retryable": documentErrorRetryable(err)}
		return result
	}
	result.Status, result.ErrorCode, result.ExitCode, result.Output = core.StatusSucceeded, "", 0, output
	return result
}

type authorizedSource struct {
	ref       string
	name      string
	format    string
	hash      string
	sizeBytes int64
	reader    io.ReadCloser
	staged    bool
}

func (e *Executor) authorizeSource(ctx context.Context, run core.RunContext, sourceRef string) (authorizedSource, error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(sourceRef), ":")
	if !ok {
		return authorizedSource{}, fmt.Errorf("DOCUMENT_SOURCE_INVALID: source_ref is malformed")
	}
	scope := core.Scope{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}
	switch kind {
	case "asset":
		frozen, ok := frozenDocumentAsset(run.ConfigSnapshotJSON, value)
		if !ok {
			return authorizedSource{}, fmt.Errorf("ASSET_NOT_FROZEN: asset is not part of the immutable Run context")
		}
		live, reader, err := e.sources.OpenAssetContent(ctx, scope, value)
		if err != nil {
			return authorizedSource{}, err
		}
		if live.ProjectID != run.ProjectID || live.ContentHash != frozen.ContentHash || live.SizeBytes != frozen.SizeBytes || live.Name != frozen.Name || live.MediaType != frozen.MediaType {
			_ = reader.Close()
			return authorizedSource{}, fmt.Errorf("ASSET_SNAPSHOT_MISMATCH: asset metadata no longer matches the frozen Run snapshot")
		}
		format, err := documentFormat(live.Name, live.MediaType)
		if err != nil {
			_ = reader.Close()
			return authorizedSource{}, err
		}
		return authorizedSource{ref: sourceRef, name: live.Name, format: format, hash: live.ContentHash, sizeBytes: live.SizeBytes, reader: reader}, nil
	case "artifact":
		artifact, reader, err := e.sources.OpenArtifactContent(ctx, scope, value)
		if err != nil {
			return authorizedSource{}, err
		}
		if artifact.ProjectID != run.ProjectID {
			_ = reader.Close()
			return authorizedSource{}, fmt.Errorf("ARTIFACT_SCOPE_MISMATCH: artifact is outside the current project")
		}
		format, err := documentFormat(artifact.Name, artifact.MediaType)
		if err != nil {
			_ = reader.Close()
			return authorizedSource{}, err
		}
		return authorizedSource{ref: sourceRef, name: artifact.Name, format: format, hash: artifact.ContentHash, sizeBytes: artifact.SizeBytes, reader: reader}, nil
	case "workspace":
		file, err := e.workspace.OpenCaptureFile(ctx, run.ProjectID, value, application.MaxArtifactCaptureBytes)
		if err != nil {
			return authorizedSource{}, err
		}
		format, err := documentFormat(value, "")
		if err != nil {
			_ = file.Close()
			return authorizedSource{}, err
		}
		return authorizedSource{ref: sourceRef, name: filepath.Base(value), format: format, sizeBytes: file.SizeBytes(), reader: file}, nil
	default:
		return authorizedSource{}, fmt.Errorf("DOCUMENT_SOURCE_INVALID: source_ref must use asset:, artifact:, or workspace:")
	}
}

func (e *Executor) open(ctx context.Context, run core.RunContext, call core.ToolCall) (map[string]any, error) {
	sourceRef, _ := call.Arguments["source_ref"].(string)
	mode, _ := call.Arguments["mode"].(string)
	source, err := e.authorizeSource(ctx, run, sourceRef)
	if err != nil {
		return nil, err
	}
	defer source.reader.Close()
	profileHash := hashStrings(parseProfileVersion, mode)
	if source.hash != "" {
		openKey := hashStrings("open", source.ref, source.hash, mode, e.client.Version(), profileHash)
		if existing, existingErr := e.state.GetDocumentSessionByOpenKey(ctx, runScope(run), run.RunID, openKey); existingErr == nil {
			if path, pathErr := e.client.HandlePath(existing.WorkingHandle); pathErr == nil && fileExists(path) {
				return documentOpenOutput(existing, true, false), nil
			}
		} else if !errors.Is(existingErr, sql.ErrNoRows) {
			return nil, existingErr
		}
	}
	documentID := uuid.NewString()
	handle := revisionHandle(run, documentID, 1, source.format)
	materializedHash, sizeBytes, err := e.materialize(handle, source.reader, source.sizeBytes)
	if err != nil {
		return nil, err
	}
	if source.hash != "" && materializedHash != source.hash {
		return nil, fmt.Errorf("DOCUMENT_SOURCE_HASH_MISMATCH: authorized source bytes changed while opening")
	}
	source.hash, source.sizeBytes = materializedHash, sizeBytes
	openKey := hashStrings("open", source.ref, source.hash, mode, e.client.Version(), profileHash)
	if existing, existingErr := e.state.GetDocumentSessionByOpenKey(ctx, runScope(run), run.RunID, openKey); existingErr == nil {
		return documentOpenOutput(existing, true, strings.HasPrefix(source.ref, "asset:")), nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return nil, existingErr
	}
	parsed, cacheHit, err := e.parse(ctx, run.OrganizationID, handle, source.format, mode, source.hash, profileHash)
	if err != nil {
		return nil, err
	}
	structureJSON, warningsJSON := mustJSON(parsed.Structure), mustJSON(parsed.Warnings)
	session := core.DocumentSession{
		ID: documentID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, OwnerUserID: run.OwnerUserID,
		TaskID: run.TaskID, RunID: run.RunID, OpenKey: openKey, SourceRef: source.ref, SourceContentHash: source.hash,
		Format: source.format, Mode: mode, RuntimeVersion: e.client.Version(), ExtractionProfileHash: profileHash,
		Revision: 1, RevisionHash: parsed.ContentHash, WorkingHandle: handle, StructureJSON: structureJSON,
		WarningsJSON: warningsJSON, Status: "ready",
	}
	created, err := e.state.CreateDocumentSession(ctx, runScope(run), session, core.DocumentRevision{DocumentID: documentID, Revision: 1, RevisionHash: parsed.ContentHash, OperationHash: openKey, WorkingHandle: handle, StructureJSON: structureJSON, WarningsJSON: warningsJSON})
	if err != nil {
		return nil, err
	}
	return documentOpenOutput(created, cacheHit, strings.HasPrefix(source.ref, "asset:")), nil
}

func (e *Executor) read(ctx context.Context, run core.RunContext, call core.ToolCall) (map[string]any, error) {
	session, err := e.session(ctx, run, stringValue(call.Arguments["document_ref"]))
	if err != nil {
		return nil, err
	}
	var structure map[string]any
	if err := json.Unmarshal([]byte(session.StructureJSON), &structure); err != nil {
		return nil, fmt.Errorf("DOCUMENT_STRUCTURE_CORRUPT: %w", err)
	}
	units, _ := structure["units"].([]any)
	selector, _ := call.Arguments["selector"].(string)
	units = selectDocumentUnits(units, strings.TrimSpace(selector))
	cursor := intValue(call.Arguments["cursor"], 0)
	limit := intValue(call.Arguments["limit"], 20)
	if cursor > len(units) {
		cursor = len(units)
	}
	end := cursor + limit
	if end > len(units) {
		end = len(units)
	}
	nextCursor := end
	if end == len(units) {
		nextCursor = -1
	}
	return map[string]any{"document_ref": "document:" + session.ID, "format": session.Format, "revision": session.Revision, "selector": selector, "cursor": cursor, "next_cursor": nextCursor, "items": units[cursor:end], "total": len(units), "warnings": jsonList(session.WarningsJSON)}, nil
}

func (e *Executor) create(ctx context.Context, run core.RunContext, call core.ToolCall, inputHash string) (map[string]any, error) {
	format, _ := call.Arguments["format"].(string)
	templateRef, _ := call.Arguments["template_ref"].(string)
	var template authorizedSource
	var templateHandle, sourceHash string
	if strings.TrimSpace(templateRef) != "" {
		var err error
		template, err = e.authorizeSource(ctx, run, templateRef)
		if err != nil {
			return nil, err
		}
		defer template.reader.Close()
		if template.format != format {
			return nil, fmt.Errorf("TEMPLATE_FORMAT_MISMATCH: template format does not match requested format")
		}
		templateHandle = filepath.ToSlash(filepath.Join("sessions", run.RunID, "templates", strings.TrimPrefix(inputHash, "sha256:")+"."+format))
		sourceHash, _, err = e.materialize(templateHandle, template.reader, template.sizeBytes)
		if err != nil {
			return nil, err
		}
		if template.hash != "" && sourceHash != template.hash {
			return nil, fmt.Errorf("DOCUMENT_SOURCE_HASH_MISMATCH: template bytes changed while opening")
		}
	} else {
		sourceHash = inputHash
	}
	openKey := hashStrings("create", inputHash, sourceHash, e.client.Version())
	if existing, err := e.state.GetDocumentSessionByOpenKey(ctx, runScope(run), run.RunID, openKey); err == nil {
		return documentOpenOutput(existing, true, false), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	documentID := uuid.NewString()
	handle := revisionHandle(run, documentID, 1, format)
	params := map[string]any{"format": format, "output_handle": handle, "specification": call.Arguments["specification"]}
	if templateHandle != "" {
		params["template_handle"] = templateHandle
	}
	var created runtimeFileResult
	if err := e.call(ctx, "create", params, &created); err != nil {
		return nil, err
	}
	profileHash := hashStrings(parseProfileVersion, "full")
	parsed, _, err := e.parse(ctx, run.OrganizationID, handle, format, "full", created.ContentHash, profileHash)
	if err != nil {
		return nil, err
	}
	structureJSON, warningsJSON := mustJSON(parsed.Structure), mustJSON(parsed.Warnings)
	session := core.DocumentSession{ID: documentID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, OwnerUserID: run.OwnerUserID, TaskID: run.TaskID, RunID: run.RunID, OpenKey: openKey, SourceRef: templateRef, SourceContentHash: sourceHash, Format: format, Mode: "create", RuntimeVersion: e.client.Version(), ExtractionProfileHash: profileHash, Revision: 1, RevisionHash: parsed.ContentHash, WorkingHandle: handle, StructureJSON: structureJSON, WarningsJSON: warningsJSON, Status: "ready"}
	createdSession, err := e.state.CreateDocumentSession(ctx, runScope(run), session, core.DocumentRevision{DocumentID: documentID, Revision: 1, RevisionHash: parsed.ContentHash, OperationHash: openKey, WorkingHandle: handle, StructureJSON: structureJSON, WarningsJSON: warningsJSON})
	if err != nil {
		return nil, err
	}
	return documentOpenOutput(createdSession, false, false), nil
}

func (e *Executor) apply(ctx context.Context, run core.RunContext, call core.ToolCall, operationHash string) (map[string]any, error) {
	session, err := e.session(ctx, run, stringValue(call.Arguments["document_ref"]))
	if err != nil {
		return nil, err
	}
	expectedRevision := int64(intValue(call.Arguments["expected_revision"], 0))
	if session.Revision != expectedRevision {
		return nil, fmt.Errorf("DOCUMENT_REVISION_CONFLICT: expected revision %d, current revision %d", expectedRevision, session.Revision)
	}
	nextRevision := expectedRevision + 1
	handle := revisionHandle(run, session.ID, nextRevision, session.Format)
	var applied struct {
		runtimeFileResult
		Applied []map[string]any `json:"applied"`
	}
	if err := e.call(ctx, "apply", map[string]any{"input_handle": session.WorkingHandle, "output_handle": handle, "format": session.Format, "operations": call.Arguments["operations"]}, &applied); err != nil {
		return nil, err
	}
	parsed, _, err := e.parse(ctx, run.OrganizationID, handle, session.Format, "full", applied.ContentHash, session.ExtractionProfileHash)
	if err != nil {
		return nil, err
	}
	next := core.DocumentRevision{DocumentID: session.ID, Revision: nextRevision, RevisionHash: parsed.ContentHash, OperationHash: operationHash, WorkingHandle: handle, StructureJSON: mustJSON(parsed.Structure), WarningsJSON: mustJSON(parsed.Warnings)}
	updated, replayed, err := e.state.AdvanceDocumentRevision(ctx, runScope(run), session.ID, expectedRevision, next)
	if err != nil {
		return nil, err
	}
	return map[string]any{"document_ref": "document:" + updated.ID, "format": updated.Format, "revision": updated.Revision, "revision_hash": updated.RevisionHash, "applied": applied.Applied, "warnings": jsonList(updated.WarningsJSON), "replayed": replayed, "render_required": true}, nil
}

func (e *Executor) render(ctx context.Context, run core.RunContext, call core.ToolCall) (map[string]any, error) {
	session, err := e.session(ctx, run, stringValue(call.Arguments["document_ref"]))
	if err != nil {
		return nil, err
	}
	expectedRevision := int64(intValue(call.Arguments["expected_revision"], 0))
	if session.Revision != expectedRevision {
		return nil, fmt.Errorf("DOCUMENT_REVISION_CONFLICT: expected revision %d, current revision %d", expectedRevision, session.Revision)
	}
	selector, _ := call.Arguments["selector"].(string)
	optionsHash := hashStrings("render", selector, "scale:1.25")
	cacheKey := hashStrings(session.RevisionHash, "LibreOffice+PDFium", optionsHash)
	outputDirHandle := filepath.ToSlash(filepath.Join("cache", "render", strings.TrimPrefix(cacheKey, "sha256:")))
	manifestHandle := filepath.ToSlash(filepath.Join(outputDirHandle, "manifest.json"))
	var rendered runtimeRenderResult
	cacheHit := false
	if manifestPath, pathErr := e.client.HandlePath(manifestHandle); pathErr == nil {
		if body, readErr := os.ReadFile(manifestPath); readErr == nil && json.Unmarshal(body, &rendered) == nil && rendered.Manifest.Validated {
			cacheHit = true
		}
	}
	if !cacheHit {
		// Runtime v1.0.0 used a fixed LibreOffice profile under the render
		// directory. Remove a profile left by an interrupted conversion before
		// invoking that signed runtime; newer packages use a unique profile.
		if profilePath, pathErr := e.client.HandlePath(filepath.ToSlash(filepath.Join(outputDirHandle, "lo-profile"))); pathErr == nil {
			if removeErr := os.RemoveAll(profilePath); removeErr != nil {
				return nil, removeErr
			}
		}
		if err := e.call(ctx, "render", map[string]any{"input_handle": session.WorkingHandle, "output_dir_handle": outputDirHandle, "format": session.Format, "selector": selector}, &rendered); err != nil {
			return nil, err
		}
		if !rendered.Manifest.Validated || rendered.Manifest.PageCount < 1 {
			return nil, fmt.Errorf("RENDER_VALIDATION_FAILED: renderer did not produce validated previews")
		}
		if err := e.writeCacheJSON(manifestHandle, rendered); err != nil {
			return nil, err
		}
	}
	manifestJSON, warningsJSON := mustJSON(rendered.Manifest), mustJSON(rendered.Warnings)
	updated, err := e.state.MarkDocumentRendered(ctx, runScope(run), session.ID, expectedRevision, rendered.RenderHash, manifestJSON, warningsJSON)
	if err != nil {
		return nil, err
	}
	previewURLs := make([]string, 0, len(rendered.Manifest.Previews))
	for _, preview := range rendered.Manifest.Previews {
		previewURLs = append(previewURLs, fmt.Sprintf("/api/v1/runs/%s/documents/%s/previews/%s", run.RunID, updated.ID, filepath.Base(preview)))
	}
	return map[string]any{"document_ref": "document:" + updated.ID, "revision": updated.Revision, "revision_hash": updated.RevisionHash, "render_hash": updated.RenderHash, "validated": true, "page_count": rendered.Manifest.PageCount, "previews": previewURLs, "warnings": rendered.Warnings, "cache_hit": cacheHit}, nil
}

func (e *Executor) export(ctx context.Context, run core.RunContext, call core.ToolCall) (map[string]any, error) {
	session, err := e.session(ctx, run, stringValue(call.Arguments["document_ref"]))
	if err != nil {
		return nil, err
	}
	expectedRevision := int64(intValue(call.Arguments["expected_revision"], 0))
	if session.Revision != expectedRevision {
		return nil, fmt.Errorf("DOCUMENT_REVISION_CONFLICT: expected revision %d, current revision %d", expectedRevision, session.Revision)
	}
	var renderManifest runtimeRenderManifest
	if session.RenderedRevision != session.Revision || session.RenderHash == "" || json.Unmarshal([]byte(session.RenderManifestJSON), &renderManifest) != nil || !renderManifest.Validated {
		return nil, fmt.Errorf("DOCUMENT_RENDER_REQUIRED: final revision must have a successful render before export")
	}
	name, _ := call.Arguments["name"].(string)
	format, _ := call.Arguments["format"].(string)
	kind, _ := call.Arguments["kind"].(string)
	outputHandle := filepath.ToSlash(filepath.Join("sessions", run.RunID, session.ID, "exports", fmt.Sprintf("revision-%06d", session.Revision), name))
	var exported runtimeFileResult
	if err := e.call(ctx, "export", map[string]any{"input_handle": session.WorkingHandle, "output_handle": outputHandle, "source_format": session.Format, "format": format}, &exported); err != nil {
		return nil, err
	}
	path, err := e.client.HandlePath(outputHandle)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	workspacePath, writeErr := e.workspace.WriteDocumentOutput(ctx, run.ProjectID, run.RunID, name, file, application.MaxArtifactCaptureBytes)
	_ = file.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	captured, err := e.capture.Capture(ctx, run.ProjectID, workspacePath, kind)
	if err != nil {
		return nil, err
	}
	if captured.ContentHash != exported.ContentHash || captured.SizeBytes != exported.SizeBytes {
		return nil, fmt.Errorf("DOCUMENT_EXPORT_HASH_MISMATCH: captured Artifact differs from the validated export")
	}
	return map[string]any{"document_ref": "document:" + session.ID, "revision": session.Revision, "revision_hash": session.RevisionHash, "render_hash": session.RenderHash, "path": captured.Path, "kind": captured.Kind, "media_type": captured.MediaType, "content_hash": captured.ContentHash, "storage_key": captured.StorageKey, "size_bytes": captured.SizeBytes, "format": format, "validated": true}, nil
}

func (e *Executor) session(ctx context.Context, run core.RunContext, documentRef string) (core.DocumentSession, error) {
	id := strings.TrimPrefix(strings.TrimSpace(documentRef), "document:")
	if _, err := uuid.Parse(id); err != nil {
		return core.DocumentSession{}, fmt.Errorf("DOCUMENT_REF_INVALID: document_ref is not valid")
	}
	session, err := e.state.GetDocumentSession(ctx, runScope(run), run.RunID, id)
	if err != nil {
		return core.DocumentSession{}, err
	}
	if session.ProjectID != run.ProjectID || session.TaskID != run.TaskID || session.RuntimeVersion != e.client.Version() {
		return core.DocumentSession{}, fmt.Errorf("DOCUMENT_SCOPE_MISMATCH: document does not belong to this frozen Run/runtime")
	}
	path, err := e.client.HandlePath(session.WorkingHandle)
	if err != nil || !fileExists(path) {
		return core.DocumentSession{}, fmt.Errorf("DOCUMENT_WORKING_COPY_MISSING: recover from the last committed revision before continuing")
	}
	return session, nil
}

func (e *Executor) OpenPreview(ctx context.Context, scope core.Scope, runID, documentID, previewName string) (application.DocumentPreview, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_REF_INVALID: run_id is not valid")
	}
	if _, err := uuid.Parse(documentID); err != nil {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_REF_INVALID: document_id is not valid")
	}
	previewName = filepath.Base(strings.TrimSpace(previewName))
	if previewName == "." || previewName == "" || !strings.HasSuffix(strings.ToLower(previewName), ".png") {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_REF_INVALID: preview name is not valid")
	}
	session, err := e.state.GetDocumentSession(ctx, scope, runID, documentID)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	if session.RenderedRevision != session.Revision || session.RenderHash == "" {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_STALE: preview is not for the current revision")
	}
	var manifest runtimeRenderManifest
	if json.Unmarshal([]byte(session.RenderManifestJSON), &manifest) != nil || !manifest.Validated {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_INVALID: render manifest is unavailable")
	}
	var handle string
	for _, candidate := range manifest.Previews {
		if filepath.Base(candidate) == previewName {
			handle = candidate
			break
		}
	}
	if handle == "" {
		return application.DocumentPreview{}, sql.ErrNoRows
	}
	path, err := e.client.HandlePath(handle)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return application.DocumentPreview{}, fmt.Errorf("DOCUMENT_PREVIEW_INVALID: preview is not a regular runtime output")
	}
	contentHash, err := hashFile(path)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	content, err := os.Open(path)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	return application.DocumentPreview{Name: previewName, MediaType: "image/png", SizeBytes: info.Size(), ContentHash: contentHash, Content: content}, nil
}

func (e *Executor) CreateFilePreview(ctx context.Context, scope core.Scope, request application.FilePreviewSource) (application.FilePreviewSession, error) {
	source, verify, err := e.authorizeFilePreviewSource(ctx, scope, request)
	if err != nil {
		return application.FilePreviewSession{}, err
	}
	defer source.reader.Close()

	inputHandle := filepath.ToSlash(filepath.Join("cache", "file-preview-inputs", uuid.NewString()+"."+source.format))
	contentHash, _, err := e.materialize(inputHandle, source.reader, source.sizeBytes)
	if err != nil {
		return application.FilePreviewSession{}, err
	}
	if inputPath, pathErr := e.client.HandlePath(inputHandle); pathErr == nil {
		defer os.Remove(inputPath)
	}
	if source.hash != "" && source.hash != contentHash {
		return application.FilePreviewSession{}, fmt.Errorf("DOCUMENT_SOURCE_HASH_MISMATCH: authorized source bytes changed while previewing")
	}
	if verify != nil {
		if err := verify(contentHash); err != nil {
			return application.FilePreviewSession{}, fmt.Errorf("DOCUMENT_SOURCE_HASH_MISMATCH: %w", err)
		}
	}
	profileHash := hashStrings(parseProfileVersion, "structure")
	parsed, _, err := e.parse(ctx, scope.OrganizationID, inputHandle, source.format, "structure", contentHash, profileHash)
	if err != nil {
		return application.FilePreviewSession{}, err
	}

	// Keep the render path comfortably below legacy Windows MAX_PATH because
	// LibreOffice creates a deeply nested profile beneath this directory. The
	// cache key still contains 160 bits and includes the complete organization
	// identity and content hash; the organization prefix is only for grouping.
	organizationKey := strings.TrimPrefix(hashStrings(scope.OrganizationID), "sha256:")[:12]
	cacheKey := strings.TrimPrefix(hashStrings("file-preview", scope.OrganizationID, contentHash, e.client.Version(), "scale:1.25"), "sha256:")[:40]
	outputDirHandle := filepath.ToSlash(filepath.Join("cache", "file-previews", organizationKey, cacheKey))
	manifestHandle := filepath.ToSlash(filepath.Join(outputDirHandle, "manifest.json"))
	var rendered runtimeRenderResult
	cacheHit := false
	if manifestPath, pathErr := e.client.HandlePath(manifestHandle); pathErr == nil {
		if body, readErr := os.ReadFile(manifestPath); readErr == nil && json.Unmarshal(body, &rendered) == nil && e.validRenderManifest(rendered.Manifest) {
			cacheHit = true
		}
	}
	if !cacheHit {
		// Keep compatibility with the signed v1.0.0 runtime, whose fixed
		// LibreOffice profile can be left unusable after an interrupted render.
		if profilePath, pathErr := e.client.HandlePath(filepath.ToSlash(filepath.Join(outputDirHandle, "lo-profile"))); pathErr == nil {
			if removeErr := os.RemoveAll(profilePath); removeErr != nil {
				return application.FilePreviewSession{}, removeErr
			}
		}
		if err := e.call(ctx, "render", map[string]any{"input_handle": inputHandle, "output_dir_handle": outputDirHandle, "format": source.format, "selector": ""}, &rendered); err != nil {
			return application.FilePreviewSession{}, err
		}
		if !e.validRenderManifest(rendered.Manifest) {
			return application.FilePreviewSession{}, fmt.Errorf("RENDER_VALIDATION_FAILED: renderer did not produce validated previews")
		}
		if err := e.writeCacheJSON(manifestHandle, rendered); err != nil {
			return application.FilePreviewSession{}, err
		}
	}
	rendered.Warnings = append(parsed.Warnings, rendered.Warnings...)

	previewID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(filePreviewTTL)
	pageNames := make([]string, 0, len(rendered.Manifest.Previews))
	pageHandles := make(map[string]string, len(rendered.Manifest.Previews))
	for _, handle := range rendered.Manifest.Previews {
		name := filepath.Base(handle)
		pageNames = append(pageNames, name)
		pageHandles[name] = handle
	}
	e.previewMu.Lock()
	e.pruneFilePreviewGrantsLocked(time.Now().UTC())
	e.previews[previewID] = filePreviewGrant{scope: scope, name: source.name, format: source.format, expiresAt: expiresAt, pageHandles: pageHandles}
	e.previewMu.Unlock()
	return application.FilePreviewSession{ID: previewID, Name: source.name, Format: source.format, PageNames: pageNames, Warnings: rendered.Warnings, ExpiresAt: expiresAt}, nil
}

func (e *Executor) OpenFilePreview(_ context.Context, scope core.Scope, previewID, previewName string) (application.DocumentPreview, error) {
	if _, err := uuid.Parse(previewID); err != nil {
		return application.DocumentPreview{}, fmt.Errorf("FILE_PREVIEW_REF_INVALID: preview_id is not valid")
	}
	previewName = filepath.Base(strings.TrimSpace(previewName))
	if previewName == "." || previewName == "" || !strings.HasSuffix(strings.ToLower(previewName), ".png") {
		return application.DocumentPreview{}, fmt.Errorf("FILE_PREVIEW_REF_INVALID: preview name is not valid")
	}
	now := time.Now().UTC()
	e.previewMu.Lock()
	e.pruneFilePreviewGrantsLocked(now)
	grant, ok := e.previews[previewID]
	e.previewMu.Unlock()
	if !ok || grant.scope != scope || !grant.expiresAt.After(now) {
		return application.DocumentPreview{}, sql.ErrNoRows
	}
	handle := grant.pageHandles[previewName]
	if handle == "" {
		return application.DocumentPreview{}, sql.ErrNoRows
	}
	path, err := e.client.HandlePath(handle)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return application.DocumentPreview{}, fmt.Errorf("FILE_PREVIEW_INVALID: preview is not a regular runtime output")
	}
	contentHash, err := hashFile(path)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	content, err := os.Open(path)
	if err != nil {
		return application.DocumentPreview{}, err
	}
	return application.DocumentPreview{Name: previewName, MediaType: "image/png", SizeBytes: info.Size(), ContentHash: contentHash, Content: content}, nil
}

func (e *Executor) authorizeFilePreviewSource(ctx context.Context, scope core.Scope, request application.FilePreviewSource) (authorizedSource, func(string) error, error) {
	switch request.Kind {
	case "asset":
		if _, err := uuid.Parse(request.Reference); err != nil {
			return authorizedSource{}, nil, fmt.Errorf("FILE_PREVIEW_SOURCE_INVALID: asset reference is not valid")
		}
		asset, content, err := e.sources.OpenAssetContent(ctx, scope, request.Reference)
		if err != nil {
			return authorizedSource{}, nil, err
		}
		if asset.ProjectID != request.ProjectID {
			_ = content.Close()
			return authorizedSource{}, nil, sql.ErrNoRows
		}
		format, err := documentFormat(asset.Name, asset.MediaType)
		if err != nil {
			_ = content.Close()
			return authorizedSource{}, nil, err
		}
		return authorizedSource{name: asset.Name, format: format, hash: asset.ContentHash, sizeBytes: asset.SizeBytes, reader: content}, nil, nil
	case "artifact":
		if _, err := uuid.Parse(request.Reference); err != nil {
			return authorizedSource{}, nil, fmt.Errorf("FILE_PREVIEW_SOURCE_INVALID: artifact reference is not valid")
		}
		artifact, content, err := e.sources.OpenArtifactContent(ctx, scope, request.Reference)
		if err != nil {
			return authorizedSource{}, nil, err
		}
		if artifact.ProjectID != request.ProjectID {
			_ = content.Close()
			return authorizedSource{}, nil, sql.ErrNoRows
		}
		format, err := documentFormat(artifact.Name, artifact.MediaType)
		if err != nil {
			_ = content.Close()
			return authorizedSource{}, nil, err
		}
		return authorizedSource{name: artifact.Name, format: format, hash: artifact.ContentHash, sizeBytes: artifact.SizeBytes, reader: content}, nil, nil
	case "workspace":
		file, err := e.workspace.OpenCaptureFile(ctx, request.ProjectID, request.Reference, application.MaxArtifactCaptureBytes)
		if err != nil {
			return authorizedSource{}, nil, err
		}
		format, err := documentFormat(file.RelativePath(), "")
		if err != nil {
			_ = file.Close()
			return authorizedSource{}, nil, err
		}
		return authorizedSource{name: filepath.Base(file.RelativePath()), format: format, sizeBytes: file.SizeBytes(), reader: file}, file.VerifyUnchanged, nil
	default:
		return authorizedSource{}, nil, fmt.Errorf("FILE_PREVIEW_SOURCE_INVALID: source kind is not supported")
	}
}

func (e *Executor) validRenderManifest(manifest runtimeRenderManifest) bool {
	if !manifest.Validated || manifest.PageCount < 1 || manifest.PageCount != len(manifest.Previews) {
		return false
	}
	for _, handle := range manifest.Previews {
		path, err := e.client.HandlePath(handle)
		if err != nil {
			return false
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func (e *Executor) pruneFilePreviewGrantsLocked(now time.Time) {
	for id, grant := range e.previews {
		if !grant.expiresAt.After(now) {
			delete(e.previews, id)
		}
	}
	if len(e.previews) < filePreviewMax {
		return
	}
	type expiringGrant struct {
		id string
		at time.Time
	}
	grants := make([]expiringGrant, 0, len(e.previews))
	for id, grant := range e.previews {
		grants = append(grants, expiringGrant{id: id, at: grant.expiresAt})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].at.Before(grants[j].at) })
	for index := 0; len(e.previews) >= filePreviewMax && index < len(grants); index++ {
		delete(e.previews, grants[index].id)
	}
}

type runtimeOpenResult struct {
	Format      string         `json:"format"`
	Structure   map[string]any `json:"structure"`
	Warnings    []string       `json:"warnings"`
	ContentHash string         `json:"content_hash"`
	SizeBytes   int64          `json:"size_bytes"`
}

type runtimeFileResult struct {
	OutputHandle string `json:"output_handle"`
	ContentHash  string `json:"content_hash"`
	SizeBytes    int64  `json:"size_bytes"`
}

type runtimeRenderManifest struct {
	Validated bool     `json:"validated"`
	PageCount int      `json:"page_count"`
	Previews  []string `json:"previews"`
	Renderer  string   `json:"renderer"`
}

type runtimeRenderResult struct {
	Manifest   runtimeRenderManifest `json:"manifest"`
	RenderHash string                `json:"render_hash"`
	Warnings   []string              `json:"warnings"`
}

func (e *Executor) parse(ctx context.Context, organizationID, handle, format, mode, contentHash, profileHash string) (runtimeOpenResult, bool, error) {
	cacheKey := hashStrings(organizationID, contentHash, e.client.Version(), profileHash)
	cacheHandle := filepath.ToSlash(filepath.Join("cache", "parse", strings.TrimPrefix(cacheKey, "sha256:")+".json"))
	if path, err := e.client.HandlePath(cacheHandle); err == nil {
		var cached runtimeOpenResult
		if body, readErr := os.ReadFile(path); readErr == nil && json.Unmarshal(body, &cached) == nil && cached.ContentHash == contentHash && cached.Format == format {
			_ = os.Chtimes(path, time.Now(), time.Now())
			return cached, true, nil
		}
	}
	var parsed runtimeOpenResult
	if err := e.call(ctx, "open", map[string]any{"input_handle": handle, "format": format, "mode": mode}, &parsed); err != nil {
		return runtimeOpenResult{}, false, err
	}
	if parsed.ContentHash != contentHash || parsed.Format != format {
		return runtimeOpenResult{}, false, fmt.Errorf("DOCUMENT_PARSE_HASH_MISMATCH: parser result does not match the source")
	}
	if err := e.writeCacheJSON(cacheHandle, parsed); err != nil {
		return runtimeOpenResult{}, false, err
	}
	return parsed, false, nil
}

func (e *Executor) materialize(handle string, reader io.Reader, expectedSize int64) (string, int64, error) {
	path, err := e.client.HandlePath(handle)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".document-input-*")
	if err != nil {
		return "", 0, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(reader, application.MaxArtifactCaptureBytes+1))
	if err != nil {
		_ = temporary.Close()
		return "", 0, err
	}
	if written > application.MaxArtifactCaptureBytes || (expectedSize >= 0 && expectedSize != written) {
		_ = temporary.Close()
		return "", 0, fmt.Errorf("DOCUMENT_SOURCE_SIZE_MISMATCH: source size changed or exceeds the limit")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", 0, err
	}
	if err := temporary.Close(); err != nil {
		return "", 0, err
	}
	contentHash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if existing, statErr := os.Stat(path); statErr == nil && existing.Mode().IsRegular() {
		if existingHash, hashErr := hashFile(path); hashErr == nil && existingHash == contentHash {
			return contentHash, written, nil
		}
		return "", 0, fmt.Errorf("DOCUMENT_IMMUTABLE_REVISION_CONFLICT: runtime handle already contains different bytes")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", 0, statErr
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return "", 0, err
	}
	return contentHash, written, nil
}

func (e *Executor) call(ctx context.Context, method string, params, output any) error {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return e.client.Call(callCtx, method, params, output)
}

func (e *Executor) writeCacheJSON(handle string, value any) error {
	path, err := e.client.HandlePath(handle)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".document-cache-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if fileExists(path) {
			return nil
		}
		return err
	}
	return nil
}

func (e *Executor) pruneCache(now time.Time) error {
	root, err := e.client.HandlePath("cache")
	if err != nil {
		return err
	}
	entries := make([]struct {
		path string
		size int64
		when time.Time
	}, 0)
	var total int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		if now.Sub(info.ModTime()) > cacheMaxAge {
			_ = os.Remove(path)
			return nil
		}
		total += info.Size()
		entries = append(entries, struct {
			path string
			size int64
			when time.Time
		}{path, info.Size(), info.ModTime()})
		return nil
	})
	if total <= cacheMaxBytes {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].when.Before(entries[j].when) })
	for _, entry := range entries {
		if total <= cacheMaxBytes {
			break
		}
		if os.Remove(entry.path) == nil {
			total -= entry.size
		}
	}
	return nil
}

func documentOpenOutput(session core.DocumentSession, cacheHit, assetRead bool) map[string]any {
	var structure map[string]any
	_ = json.Unmarshal([]byte(session.StructureJSON), &structure)
	summary := map[string]any{"unit_kind": structure["unit_kind"], "unit_count": structure["unit_count"], "title": structure["title"]}
	return map[string]any{"document_ref": "document:" + session.ID, "format": session.Format, "revision": session.Revision, "revision_hash": session.RevisionHash, "structure": summary, "warnings": jsonList(session.WarningsJSON), "cache_hit": cacheHit, "asset_read_evidence": assetRead, "runtime_version": session.RuntimeVersion}
}

func documentFormat(name, mediaType string) (string, error) {
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(name)), "."))
	byMedia := map[string]string{
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"application/pdf": "pdf",
	}
	if format := byMedia[strings.ToLower(strings.TrimSpace(mediaType))]; format != "" {
		if extension != "" && extension != format {
			return "", fmt.Errorf("DOCUMENT_FORMAT_MISMATCH: MIME and extension disagree")
		}
		return format, nil
	}
	switch extension {
	case "pptx", "docx", "xlsx", "pdf":
		return extension, nil
	case "ppt", "doc", "xls", "pptm", "docm", "xlsm":
		return "", fmt.Errorf("DOCUMENT_FORMAT_UNSUPPORTED: legacy, encrypted, and macro-enabled Office formats are not supported")
	default:
		return "", fmt.Errorf("DOCUMENT_FORMAT_UNSUPPORTED: only .pptx, .pdf, .docx, and .xlsx are supported")
	}
}

func revisionHandle(run core.RunContext, documentID string, revision int64, format string) string {
	return filepath.ToSlash(filepath.Join("sessions", run.RunID, documentID, fmt.Sprintf("revision-%06d.%s", revision, format)))
}

func frozenDocumentAsset(snapshotJSON, assetID string) (core.Asset, bool) {
	var snapshot struct {
		Assets []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			MediaType   string `json:"media_type"`
			ContentHash string `json:"content_hash"`
			SizeBytes   int64  `json:"size_bytes"`
			ProjectID   string `json:"project_id"`
		} `json:"context_asset_snapshots"`
	}
	if json.Unmarshal([]byte(snapshotJSON), &snapshot) != nil {
		return core.Asset{}, false
	}
	for _, item := range snapshot.Assets {
		if item.ID == assetID {
			return core.Asset{ID: item.ID, ProjectID: item.ProjectID, Name: item.Name, MediaType: item.MediaType, ContentHash: item.ContentHash, SizeBytes: item.SizeBytes}, true
		}
	}
	return core.Asset{}, false
}

func selectDocumentUnits(units []any, selector string) []any {
	if selector == "" || selector == "all" {
		return units
	}
	if _, suffix, found := strings.Cut(selector, ":"); found {
		index, err := strconv.Atoi(strings.TrimSpace(suffix))
		if err != nil {
			index = 0
		}
		if index >= 1 && index <= len(units) {
			return units[index-1 : index]
		}
	}
	for _, unit := range units {
		if item, ok := unit.(map[string]any); ok {
			if name, _ := item["name"].(string); strings.EqualFold(name, selector) {
				return []any{unit}
			}
		}
	}
	return []any{}
}

func runScope(run core.RunContext) core.Scope {
	return core.Scope{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}
}

func stringValue(value any) string { text, _ := value.(string); return strings.TrimSpace(text) }

func intValue(value any, fallback int) int {
	if number, ok := value.(float64); ok && number == float64(int(number)) {
		return int(number)
	}
	return fallback
}

func hashStrings(values ...string) string {
	hasher := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hasher, value)
		_, _ = hasher.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func mustJSON(value any) string { body, _ := json.Marshal(value); return string(body) }

func jsonList(body string) []string {
	var values []string
	_ = json.Unmarshal([]byte(body), &values)
	if values == nil {
		values = []string{}
	}
	return values
}

func documentErrorCode(err error) string {
	var remote *rpcError
	if errors.As(err, &remote) && remote.Code != "" {
		return remote.Code
	}
	text := err.Error()
	if index := strings.IndexByte(text, ':'); index > 0 && index < 80 {
		candidate := text[:index]
		if candidate == strings.ToUpper(candidate) && !strings.Contains(candidate, " ") {
			return candidate
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "DOCUMENT_RUNTIME_TIMEOUT"
	}
	return "DOCUMENT_RUNTIME_FAILED"
}

func safeDocumentError(err error) string {
	var remote *rpcError
	if errors.As(err, &remote) {
		return remote.Message
	}
	return err.Error()
}

func documentErrorRetryable(err error) bool {
	code := documentErrorCode(err)
	return code == "DOCUMENT_RUNTIME_TIMEOUT" || code == "DOCUMENT_RUNTIME_FAILED" || code == "RUNTIME_INTERNAL" || code == "RENDER_FAILED"
}

var _ application.DocumentExecutor = (*Executor)(nil)
var _ application.DocumentPreviewOpener = (*Executor)(nil)
var _ application.FilePreviewer = (*Executor)(nil)
