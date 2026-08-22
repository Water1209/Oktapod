package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type PlannedTool struct {
	Call               ToolCall
	ArgumentsJSON      string
	InputHash          string
	RiskLevel          string
	Decision           string
	Reason             string
	Idempotent         bool
	ApprovalScope      string
	ApprovalGrantKey   string
	ApprovalPolicyHash string
	ApprovalOrigin     string
	ApprovalRuleID     string
	ForceHumanApproval bool
}

const ToolPolicyVersion = "tool-policy-v1"

// ExecutionAuthorization is the immutable view checked immediately before a
// side effect starts. Approval rows remain the durable source of authority;
// this value binds them to the exact tool, input, scope, risk and policy.
type ExecutionAuthorization struct {
	ToolIdentity   string
	InputHash      string
	OrganizationID string
	ProjectID      string
	RunID          string
	RiskLevel      string
	PolicyDecision string
	PolicyVersion  string
}

func ValidateExecutionAuthorization(planned PlannedTool, authorization ExecutionAuthorization) error {
	if planned.Decision == PolicyDeny {
		return fmt.Errorf("current policy denies tool execution")
	}
	if authorization.ToolIdentity != planned.Call.Name || authorization.InputHash == "" || authorization.InputHash != planned.InputHash {
		return fmt.Errorf("tool identity or frozen input changed")
	}
	if authorization.OrganizationID == "" || authorization.ProjectID == "" || authorization.RunID == "" {
		return fmt.Errorf("execution scope is incomplete")
	}
	if authorization.RiskLevel != planned.RiskLevel {
		return fmt.Errorf("tool risk classification changed")
	}
	if authorization.PolicyDecision != PolicyAllow && authorization.PolicyDecision != PolicyAsk {
		return fmt.Errorf("persisted policy decision is not executable")
	}
	if authorization.PolicyVersion != ToolPolicyVersion {
		return fmt.Errorf("tool policy version changed")
	}
	return nil
}

func PlanTool(call ToolCall) PlannedTool {
	planned := PlannedTool{Call: call, RiskLevel: "L3", Decision: PolicyDeny, Reason: "tool is not registered", Idempotent: false}
	resolvedName := ResolveToolName(call.Name)
	body, err := json.Marshal(call.Arguments)
	if err != nil {
		planned.Reason = "tool arguments cannot be serialized"
		return planned
	}
	planned.ArgumentsJSON = string(body)
	sum := sha256.Sum256(append([]byte(call.Name+"\n"), body...))
	planned.InputHash = "sha256:" + hex.EncodeToString(sum[:])

	switch resolvedName {
	case ToolUpdatePlan, ToolReplacePlan, ToolAdvancePlan:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		planned.Decision = PolicyAllow
		planned.Reason = "Updates durable semantic plan metadata without executing an external action"
	case ToolRequestInput, ToolRequestPermission:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		planned.Decision = PolicyAsk
		planned.Reason = "execution must pause at a durable checkpoint for a human response"
	case ToolSkillInvoke:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateSkillInvokeArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Loads one Skill from the immutable Run snapshot without expanding runtime permissions"
	case ToolWebSearch:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateWebSearchArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Read-only search is limited to the configured search provider and bounded public results"
	case ToolWebFetch:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateWebFetchArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Web fetch requires the injected Web Access policy to classify its destination"
	case ToolWorkspaceWriteFile:
		planned.RiskLevel = "L1"
		planned.Idempotent = true
		if err := validateWriteArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Standard policy allows create-only writes inside the project workspace"
	case ToolArtifactCapture:
		planned.RiskLevel = "L1"
		planned.Idempotent = true
		if err := validateArtifactCaptureArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Captures one validated regular workspace file into immutable content-addressed storage"
	case ToolAssetStage:
		planned.RiskLevel = "L1"
		planned.Idempotent = true
		if err := validateAssetStageArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Stages one Run-frozen Asset at a temporary read-only internal workspace path"
	case ToolWorkspaceReadFile, ToolWorkspaceListFiles, ToolWorkspaceFindFiles, ToolWorkspaceGrep:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateReadToolArguments(resolvedName, call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Read-only workspace access is bounded, paginated, and confined to the project root"
	case ToolWorkspaceEditFile:
		planned.RiskLevel = "L1"
		planned.Idempotent = true
		if err := validateEditArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "CAS edit is confined to one regular UTF-8 workspace file"
	case ToolGitStatus, ToolGitDiff, ToolGitLog, ToolGitConflicts:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateGitReadArguments(resolvedName, call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Read-only local Git inspection is bounded and does not access remotes"
	case ToolGitBranch, ToolGitCommit, ToolGitCommitPaths, ToolGitRestore, ToolGitRestoreHunk:
		planned.RiskLevel = "L2"
		planned.Idempotent = false
		if err := validateGitMutationArguments(resolvedName, call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAsk
		planned.Reason = "Local Git mutation requires frozen preconditions, a durable receipt, and explicit approval"
	case ToolWorkspaceRunCommand:
		planned.RiskLevel = "L2"
		if err := validateCommandArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAsk
		planned.Reason = "direct command execution requires an explicit one-time approval"
	case ToolWorkspaceShell:
		planned.RiskLevel = "L2"
		if err := validateShellArguments(call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAsk
		planned.Reason = "shell execution requires an explicit one-time approval"
	default:
		if strings.HasPrefix(call.Name, "mcp__") && strings.Count(call.Name, "__") >= 2 {
			// MCP calls are external side effects. They are always persisted,
			// require one-time approval, and are never retried automatically.
			planned.RiskLevel = "L2"
			planned.Decision = PolicyAsk
			planned.Reason = "MCP tool execution requires explicit approval and a side-effect receipt"
			if len(planned.ArgumentsJSON) > 1<<20 {
				planned.Decision = PolicyDeny
				planned.Reason = "MCP tool arguments exceed the 1 MiB safety limit"
			}
		}
	}
	return planned
}

func validateReadToolArguments(name string, arguments map[string]any) error {
	allowed := map[string]map[string]bool{
		ToolWorkspaceReadFile:  {"path": true, "offset_line": true, "limit_lines": true},
		ToolWorkspaceListFiles: {"path": true, "recursive": true, "cursor": true, "limit": true},
		ToolWorkspaceFindFiles: {"pattern": true, "path": true, "cursor": true, "limit": true},
		ToolWorkspaceGrep:      {"query": true, "path": true, "glob": true, "cursor": true, "limit": true},
	}[name]
	for key := range arguments {
		if !allowed[key] {
			return fmt.Errorf("%s received unsupported argument %s", name, key)
		}
	}
	if name == ToolWorkspaceReadFile {
		if err := validateRelativeToolPath(arguments["path"], false); err != nil {
			return err
		}
	}
	if value, ok := arguments["path"]; ok && name != ToolWorkspaceReadFile {
		if err := validateRelativeToolPath(value, true); err != nil {
			return err
		}
	}
	if value, ok := arguments["recursive"]; ok {
		if _, valid := value.(bool); !valid {
			return fmt.Errorf("recursive must be boolean")
		}
	}
	for _, field := range []string{"cursor", "offset_line", "limit", "limit_lines"} {
		if value, ok := arguments[field]; ok {
			number, valid := value.(float64)
			maximum := float64(500)
			if field == "limit_lines" {
				maximum = 1000
			}
			if !valid || number < 0 || number != float64(int64(number)) || (strings.HasPrefix(field, "limit") && (number < 1 || number > maximum)) {
				return fmt.Errorf("%s must be a bounded non-negative integer", field)
			}
		}
	}
	for _, field := range []string{"pattern", "query", "glob"} {
		if value, ok := arguments[field]; ok {
			text, valid := value.(string)
			if !valid || strings.TrimSpace(text) == "" || len([]rune(text)) > 500 || strings.ContainsRune(text, 0) {
				return fmt.Errorf("%s must be a bounded non-empty string", field)
			}
		}
	}
	if name == ToolWorkspaceFindFiles {
		if _, ok := arguments["pattern"]; !ok {
			return fmt.Errorf("pattern is required")
		}
	}
	if name == ToolWorkspaceGrep {
		if _, ok := arguments["query"]; !ok {
			return fmt.Errorf("query is required")
		}
	}
	return nil
}

func validateEditArguments(arguments map[string]any) error {
	if len(arguments) != 4 {
		return fmt.Errorf("edit_file accepts only path, preimage_hash, old_text, and new_text")
	}
	if err := validateRelativeToolPath(arguments["path"], false); err != nil {
		return err
	}
	hash, ok := arguments["preimage_hash"].(string)
	if !ok || !validSHA256Ref(hash) {
		return fmt.Errorf("preimage_hash must be a sha256 content hash")
	}
	for _, field := range []string{"old_text", "new_text"} {
		value, ok := arguments[field].(string)
		if !ok || len([]byte(value)) > 1<<20 || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%s must be bounded UTF-8 text", field)
		}
	}
	if arguments["old_text"].(string) == "" {
		return fmt.Errorf("old_text must not be empty")
	}
	return nil
}

func validateGitReadArguments(name string, arguments map[string]any) error {
	switch name {
	case ToolGitStatus:
		if len(arguments) != 0 {
			return fmt.Errorf("git_status accepts no arguments")
		}
	case ToolGitConflicts:
		if len(arguments) != 0 {
			return fmt.Errorf("git_conflicts accepts no arguments")
		}
	case ToolGitDiff:
		for key := range arguments {
			if key != "path" && key != "staged" && key != "baseline" {
				return fmt.Errorf("git_diff accepts only path, staged, and baseline")
			}
		}
		if path, ok := arguments["path"]; ok {
			if err := validateRelativeToolPath(path, true); err != nil {
				return err
			}
		}
		if staged, ok := arguments["staged"]; ok {
			if _, valid := staged.(bool); !valid {
				return fmt.Errorf("staged must be boolean")
			}
		}
		if baseline, ok := arguments["baseline"]; ok {
			text, valid := baseline.(string)
			if !valid || len(text) < 7 || len(text) > 64 || strings.Trim(text, "0123456789abcdefABCDEF") != "" {
				return fmt.Errorf("baseline must be a 7-64 character hexadecimal commit id")
			}
		}
	case ToolGitLog:
		for key := range arguments {
			if key != "limit" {
				return fmt.Errorf("git_log accepts only limit")
			}
		}
		if limit, ok := arguments["limit"]; ok {
			number, valid := limit.(float64)
			if !valid || number < 1 || number > 100 || number != float64(int(number)) {
				return fmt.Errorf("limit must be an integer from 1 to 100")
			}
		}
	}
	return nil
}

func validateGitMutationArguments(name string, arguments map[string]any) error {
	expected, ok := arguments["expected_status_hash"].(string)
	if !ok || !validSHA256Ref(expected) {
		return fmt.Errorf("expected_status_hash must be a sha256 hash returned by git_status")
	}
	switch name {
	case ToolGitBranch:
		if len(arguments) != 2 {
			return fmt.Errorf("git_branch accepts only name and expected_status_hash")
		}
		branch, ok := arguments["name"].(string)
		if !ok || strings.TrimSpace(branch) == "" || len(branch) > 200 || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") {
			return fmt.Errorf("branch name is invalid")
		}
	case ToolGitCommit:
		if len(arguments) != 2 {
			return fmt.Errorf("git_commit accepts only message and expected_status_hash")
		}
		message, ok := arguments["message"].(string)
		if !ok || strings.TrimSpace(message) == "" || len([]rune(message)) > 500 || strings.ContainsRune(message, 0) {
			return fmt.Errorf("commit message must be a bounded non-empty string")
		}
	case ToolGitCommitPaths:
		if len(arguments) != 3 {
			return fmt.Errorf("git_commit_paths accepts only message, paths, and expected_status_hash")
		}
		message, ok := arguments["message"].(string)
		if !ok || strings.TrimSpace(message) == "" || len([]rune(message)) > 500 || strings.ContainsRune(message, 0) {
			return fmt.Errorf("commit message must be a bounded non-empty string")
		}
		paths, ok := arguments["paths"].([]any)
		if !ok || len(paths) == 0 || len(paths) > 100 {
			return fmt.Errorf("paths must contain 1 to 100 project-relative paths")
		}
		for _, path := range paths {
			if err := validateRelativeToolPath(path, false); err != nil {
				return err
			}
		}
	case ToolGitRestore:
		if len(arguments) != 2 {
			return fmt.Errorf("git_restore accepts only path and expected_status_hash")
		}
		if err := validateRelativeToolPath(arguments["path"], false); err != nil {
			return err
		}
	case ToolGitRestoreHunk:
		if len(arguments) != 3 {
			return fmt.Errorf("git_restore_hunk accepts only path, hunk_index, and expected_status_hash")
		}
		if err := validateRelativeToolPath(arguments["path"], false); err != nil {
			return err
		}
		index, ok := arguments["hunk_index"].(float64)
		if !ok || index < 0 || index > 10000 || index != float64(int(index)) {
			return fmt.Errorf("hunk_index must be an integer from 0 to 10000")
		}
	}
	return nil
}

func validateRelativeToolPath(value any, allowEmpty bool) error {
	path, ok := value.(string)
	path = strings.TrimSpace(path)
	if !ok || (!allowEmpty && path == "") || len([]rune(path)) > 500 || strings.ContainsRune(path, 0) {
		return fmt.Errorf("path must be a bounded project-relative string")
	}
	if path == "" && allowEmpty {
		return nil
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return fmt.Errorf("absolute workspace paths are denied")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace path escape is denied")
	}
	return nil
}

func validateArtifactCaptureArguments(arguments map[string]any) error {
	if len(arguments) != 2 {
		return fmt.Errorf("artifact_capture accepts only path and kind")
	}
	if err := validateRelativeToolPath(arguments["path"], false); err != nil {
		return err
	}
	path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(arguments["path"].(string))))
	if path == ".oktapod" || strings.HasPrefix(path, ".oktapod/") {
		return fmt.Errorf("internal workspace paths cannot be captured")
	}
	kind, ok := arguments["kind"].(string)
	if !ok || (kind != "deliverable" && kind != "evidence") {
		return fmt.Errorf("kind must be deliverable or evidence")
	}
	return nil
}

func validateAssetStageArguments(arguments map[string]any) error {
	if len(arguments) != 1 {
		return fmt.Errorf("asset_stage accepts only asset_id")
	}
	assetID, ok := arguments["asset_id"].(string)
	assetID = strings.TrimSpace(assetID)
	if !ok || len(assetID) != 36 {
		return fmt.Errorf("asset_id must be a UUID")
	}
	for index, char := range assetID {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return fmt.Errorf("asset_id must be a UUID")
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return fmt.Errorf("asset_id must be a UUID")
		}
	}
	return nil
}

func validSHA256Ref(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateSkillInvokeArguments(arguments map[string]any) error {
	if len(arguments) < 1 || len(arguments) > 2 {
		return fmt.Errorf("skill_invoke accepts skill plus optional args")
	}
	for key := range arguments {
		if key != "skill" && key != "args" {
			return fmt.Errorf("skill_invoke accepts only skill and args")
		}
	}
	skill, ok := arguments["skill"].(string)
	skill = strings.TrimSpace(skill)
	if !ok || skill == "" || len([]rune(skill)) > 200 || strings.ContainsRune(skill, 0) {
		return fmt.Errorf("skill must be a non-empty Skill id or name no longer than 200 characters")
	}
	if value, exists := arguments["args"]; exists {
		args, ok := value.(string)
		if !ok || len([]rune(args)) > 10000 || strings.ContainsRune(args, 0) {
			return fmt.Errorf("args must be a string no longer than 10000 characters")
		}
	}
	return nil
}

func validateWebSearchArguments(arguments map[string]any) error {
	if len(arguments) != 1 {
		return fmt.Errorf("web_search accepts only query")
	}
	query, ok := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if !ok || len([]rune(query)) < 2 || len([]rune(query)) > 500 || strings.ContainsRune(query, 0) {
		return fmt.Errorf("query must contain 2 to 500 characters")
	}
	return nil
}

func validateWebFetchArguments(arguments map[string]any) error {
	if len(arguments) != 1 {
		return fmt.Errorf("web_fetch accepts only url")
	}
	raw, ok := arguments["url"].(string)
	if !ok {
		return fmt.Errorf("url is required")
	}
	if _, _, _, _, err := NormalizeWebFetchURL(raw); err != nil {
		return err
	}
	return nil
}

func validateWriteArguments(arguments map[string]any) error {
	if len(arguments) != 2 {
		return fmt.Errorf("write_file accepts only path and content")
	}
	path, ok := arguments["path"].(string)
	if !ok || strings.TrimSpace(path) == "" || len([]rune(path)) > 500 {
		return fmt.Errorf("path must be a non-empty project-relative string")
	}
	if strings.ContainsRune(path, 0) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return fmt.Errorf("absolute or invalid workspace paths are denied")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace path escape is denied")
	}
	content, ok := arguments["content"].(string)
	if !ok || len([]byte(content)) > 1<<20 {
		return fmt.Errorf("content must be UTF-8 text no larger than 1 MiB")
	}
	return nil
}

func validateCommandArguments(arguments map[string]any) error {
	if len(arguments) != 2 {
		return fmt.Errorf("run_command accepts only program and args")
	}
	program, ok := arguments["program"].(string)
	program = strings.TrimSpace(program)
	if !ok || program == "" || len(program) > 100 || strings.ContainsRune(program, 0) {
		return fmt.Errorf("program must be a non-empty executable name")
	}
	if filepath.IsAbs(program) || filepath.VolumeName(program) != "" || strings.ContainsAny(program, `/\\`) || program == "." || program == ".." {
		return fmt.Errorf("program paths are denied; provide an executable name")
	}
	shells := map[string]bool{"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true, "pwsh": true, "bash": true, "sh": true, "zsh": true, "fish": true, "wsl": true, "wsl.exe": true}
	if shells[strings.ToLower(program)] {
		return fmt.Errorf("shell interpreters are denied; execute a concrete program with structured arguments")
	}
	networkPrograms := map[string]bool{"curl": true, "curl.exe": true, "wget": true, "wget.exe": true, "ssh": true, "ssh.exe": true, "scp": true, "scp.exe": true, "sftp": true, "sftp.exe": true, "ftp": true, "ftp.exe": true, "nc": true, "ncat": true, "netcat": true, "telnet": true, "telnet.exe": true}
	if networkPrograms[strings.ToLower(program)] {
		return fmt.Errorf("network-capable programs are denied; use an explicitly governed MCP capability")
	}
	rawArgs, ok := arguments["args"].([]any)
	if !ok || len(rawArgs) > 64 {
		return fmt.Errorf("args must be an array containing at most 64 strings")
	}
	for _, raw := range rawArgs {
		arg, ok := raw.(string)
		if !ok || len([]rune(arg)) > 2000 || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("each command argument must be a valid bounded string")
		}
	}
	return nil
}

func validateShellArguments(arguments map[string]any) error {
	if len(arguments) < 1 || len(arguments) > 2 {
		return fmt.Errorf("workspace_shell accepts command plus optional timeout_seconds")
	}
	for key := range arguments {
		if key != "command" && key != "timeout_seconds" {
			return fmt.Errorf("workspace_shell received unsupported argument %s", key)
		}
	}
	command, ok := arguments["command"].(string)
	command = strings.TrimSpace(command)
	if !ok || command == "" || len([]rune(command)) > 10000 || strings.ContainsRune(command, 0) {
		return fmt.Errorf("command must be a bounded non-empty string")
	}
	if timeout, exists := arguments["timeout_seconds"]; exists {
		number, valid := timeout.(float64)
		if !valid || number < 1 || number > 300 || number != float64(int(number)) {
			return fmt.Errorf("timeout_seconds must be an integer from 1 to 300")
		}
	}
	return nil
}
