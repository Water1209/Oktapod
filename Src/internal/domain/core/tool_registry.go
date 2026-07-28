package core

// ToolSpec is the provider-neutral source of truth for model-visible tools.
// Provider adapters map this schema to their wire format and never own tool
// names, safety semantics, or business policy.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
	Sequential  bool
}

func BuiltinToolSpecs() []ToolSpec {
	path := map[string]any{"type": "string", "minLength": 1, "maxLength": 500}
	cursor := map[string]any{"type": "integer", "minimum": 0}
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": 500}
	return []ToolSpec{
		{
			Name: ToolUpdatePlan, Description: "Publish or replace the complete semantic execution plan for the current Run. Use outcome-oriented user-visible steps, never raw tool calls. This control action must be called alone.",
			Sequential: true,
			Parameters: objectSchema([]string{"items"}, map[string]any{
				"explanation": map[string]any{"type": "string", "maxLength": 1000},
				"items": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 12,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"title", "description", "status"},
						"properties": map[string]any{
							"title":       map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
							"description": map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
							"status":      map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						},
					},
				},
			}),
		},
		{
			Name: ToolRequestInput, Description: "Pause the current plan at a checkpoint and ask the user for required structured input. Use only when execution cannot safely continue without the answer.",
			Sequential: true,
			Parameters: objectSchema([]string{"prompt"}, map[string]any{
				"prompt":  map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
				"choices": map[string]any{"type": "array", "maxItems": 10, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}},
			}),
		},
		{
			Name: ToolRequestPermission, Description: "Request a narrow Run-only capability escalation when the current scene does not expose a required tool. This never changes project defaults.",
			Sequential: true,
			Parameters: objectSchema([]string{"capability", "reason"}, map[string]any{
				"capability": map[string]any{"type": "string", "enum": []string{"workspace_command"}},
				"reason":     map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
			}),
		},
		{
			Name: ToolWebSearch, Description: "Search bounded public web results with optional authoritative domain and freshness filters.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query": map[string]any{"type": "string", "minLength": 2, "maxLength": 500}, "max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				"freshness":       map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"}},
				"allowed_domains": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 253}},
				"blocked_domains": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 253}},
			}},
		},
		{Name: ToolWorkspaceReadFile, Description: "Read a deterministic page of a UTF-8 text file. Binary files are rejected and likely secrets are redacted.", Parameters: objectSchema([]string{"path"}, map[string]any{"path": path, "offset_line": cursor, "limit_lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}})},
		{Name: ToolWorkspaceListFiles, Description: "List a deterministic page of files under a project-relative directory.", Parameters: objectSchema(nil, map[string]any{"path": map[string]any{"type": "string", "maxLength": 500}, "recursive": map[string]any{"type": "boolean"}, "cursor": cursor, "limit": limit})},
		{Name: ToolWorkspaceFindFiles, Description: "Find project-relative file names using a bounded glob pattern and deterministic pagination.", Parameters: objectSchema([]string{"pattern"}, map[string]any{"pattern": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "path": map[string]any{"type": "string", "maxLength": 500}, "cursor": cursor, "limit": limit})},
		{Name: ToolWorkspaceGrep, Description: "Search bounded UTF-8 workspace files for literal text with deterministic pagination and secret redaction.", Parameters: objectSchema([]string{"query"}, map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "path": map[string]any{"type": "string", "maxLength": 500}, "glob": map[string]any{"type": "string", "maxLength": 200}, "cursor": cursor, "limit": limit})},
		{Name: ToolWorkspaceEditFile, Description: "Replace one exact text occurrence only when the current file matches preimage_hash. The write is atomic and conflicts never overwrite external changes.", Sequential: true, Parameters: objectSchema([]string{"path", "preimage_hash", "old_text", "new_text"}, map[string]any{"path": path, "preimage_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}, "old_text": map[string]any{"type": "string", "maxLength": 1000000}, "new_text": map[string]any{"type": "string", "maxLength": 1000000}})},
		{Name: ToolWorkspaceWriteFile, Description: "Create a new UTF-8 text file inside the project workspace. Existing files are never overwritten.", Sequential: true, Parameters: objectSchema([]string{"path", "content"}, map[string]any{"path": path, "content": map[string]any{"type": "string", "maxLength": 1048576}})},
		{Name: ToolWorkspaceRunCommand, Description: "Run one executable directly without a shell. Deterministic command and network blacklists still apply.", Sequential: true, Parameters: objectSchema([]string{"program", "args"}, map[string]any{"program": map[string]any{"type": "string", "minLength": 1, "maxLength": 100}, "args": map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 2000}}})},
		{Name: ToolGitStatus, Description: "Read bounded Git status and return a precondition hash.", Parameters: emptyObjectSchema()},
		{Name: ToolGitDiff, Description: "Read a bounded Git diff against HEAD or the index.", Parameters: objectSchema(nil, map[string]any{"path": map[string]any{"type": "string", "maxLength": 500}, "staged": map[string]any{"type": "boolean"}})},
		{Name: ToolGitLog, Description: "Read a bounded recent Git log.", Parameters: objectSchema(nil, map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}})},
		{Name: ToolGitConflicts, Description: "List unresolved local merge conflicts without changing the index or working tree.", Parameters: emptyObjectSchema()},
		{Name: ToolGitBranch, Description: "Create a local branch after validating the frozen repository status hash.", Sequential: true, Parameters: objectSchema([]string{"name", "expected_status_hash"}, map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "expected_status_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}})},
		{Name: ToolGitCommit, Description: "Create a local commit after validating the frozen repository status hash.", Sequential: true, Parameters: objectSchema([]string{"message", "expected_status_hash"}, map[string]any{"message": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "expected_status_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}})},
		{Name: ToolGitCommitPaths, Description: "Commit only selected project-relative paths while preserving unrelated index state.", Sequential: true, Parameters: objectSchema([]string{"message", "paths", "expected_status_hash"}, map[string]any{"message": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "paths": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": path}, "expected_status_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}})},
		{Name: ToolGitRestore, Description: "Restore one complete file from HEAD after validating the frozen repository status hash.", Sequential: true, Parameters: objectSchema([]string{"path", "expected_status_hash"}, map[string]any{"path": path, "expected_status_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}})},
		{Name: ToolGitRestoreHunk, Description: "Reverse one numbered unstaged diff hunk after validating the frozen repository status hash.", Sequential: true, Parameters: objectSchema([]string{"path", "hunk_index", "expected_status_hash"}, map[string]any{"path": path, "hunk_index": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000}, "expected_status_hash": map[string]any{"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}})},
	}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func emptyObjectSchema() map[string]any {
	return objectSchema(nil, map[string]any{})
}
