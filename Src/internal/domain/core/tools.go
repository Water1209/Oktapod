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
	Call          ToolCall
	ArgumentsJSON string
	InputHash     string
	RiskLevel     string
	Decision      string
	Reason        string
	Idempotent    bool
}

func PlanTool(call ToolCall) PlannedTool {
	planned := PlannedTool{Call: call, RiskLevel: "L3", Decision: PolicyDeny, Reason: "tool is not registered", Idempotent: false}
	body, err := json.Marshal(call.Arguments)
	if err != nil {
		planned.Reason = "tool arguments cannot be serialized"
		return planned
	}
	planned.ArgumentsJSON = string(body)
	sum := sha256.Sum256(append([]byte(call.Name+"\n"), body...))
	planned.InputHash = "sha256:" + hex.EncodeToString(sum[:])

	switch call.Name {
	case ToolUpdatePlan:
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
	case ToolWorkspaceReadFile, ToolWorkspaceListFiles, ToolWorkspaceFindFiles, ToolWorkspaceGrep:
		planned.RiskLevel = "L0"
		planned.Idempotent = true
		if err := validateReadToolArguments(call.Name, call.Arguments); err != nil {
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
		if err := validateGitReadArguments(call.Name, call.Arguments); err != nil {
			planned.Decision = PolicyDeny
			planned.Reason = err.Error()
			return planned
		}
		planned.Decision = PolicyAllow
		planned.Reason = "Read-only local Git inspection is bounded and does not access remotes"
	case ToolGitBranch, ToolGitCommit, ToolGitCommitPaths, ToolGitRestore, ToolGitRestoreHunk:
		planned.RiskLevel = "L2"
		planned.Idempotent = false
		if err := validateGitMutationArguments(call.Name, call.Arguments); err != nil {
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
			if key != "path" && key != "staged" {
				return fmt.Errorf("git_diff accepts only path and staged")
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
	if len(arguments) == 0 || len(arguments) > 5 {
		return fmt.Errorf("web_search accepts query plus optional max_results, freshness, allowed_domains, and blocked_domains")
	}
	query, ok := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if !ok || len([]rune(query)) < 2 || len([]rune(query)) > 500 || strings.ContainsRune(query, 0) {
		return fmt.Errorf("query must contain 2 to 500 characters")
	}
	if value, exists := arguments["max_results"]; exists {
		count, ok := value.(float64)
		if !ok || count < 1 || count > 10 || count != float64(int(count)) {
			return fmt.Errorf("max_results must be an integer from 1 to 10")
		}
	}
	if value, exists := arguments["freshness"]; exists {
		freshness, ok := value.(string)
		if !ok || (freshness != "day" && freshness != "week" && freshness != "month" && freshness != "year") {
			return fmt.Errorf("freshness must be day, week, month, or year")
		}
	}
	allowed, err := validateSearchDomains(arguments["allowed_domains"])
	if err != nil {
		return fmt.Errorf("allowed_domains: %w", err)
	}
	blocked, err := validateSearchDomains(arguments["blocked_domains"])
	if err != nil {
		return fmt.Errorf("blocked_domains: %w", err)
	}
	if len(allowed) > 0 && len(blocked) > 0 {
		return fmt.Errorf("allowed_domains and blocked_domains cannot be used together")
	}
	return nil
}

func validateSearchDomains(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > 20 {
		return nil, fmt.Errorf("must be an array containing at most 20 host names")
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		domain, ok := item.(string)
		domain = strings.ToLower(strings.TrimSpace(domain))
		if !ok || domain == "" || len(domain) > 253 || strings.ContainsAny(domain, "/\\:@?# ") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
			return nil, fmt.Errorf("each domain must be a bounded host name without scheme, path, port, or credentials")
		}
		for _, label := range strings.Split(domain, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return nil, fmt.Errorf("each domain must contain valid DNS labels")
			}
			for _, char := range label {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
					return nil, fmt.Errorf("each domain must contain only ASCII DNS characters")
				}
			}
		}
		result = append(result, domain)
	}
	return result, nil
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
