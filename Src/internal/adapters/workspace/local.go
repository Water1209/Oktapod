package workspace

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const maxCommandOutput = 1 << 20

type Local struct {
	root            string
	executionPolicy application.ExecutionPolicy
}

func NewLocal(root string) (*Local, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	return &Local{root: absolute, executionPolicy: application.StaticExecutionPolicy{}}, nil
}

func (l *Local) Execute(ctx context.Context, projectID string, call core.ToolCall) core.ToolExecution {
	planned, policyErr := l.executionPolicy.EvaluateExecution(ctx, application.Principal{}, application.ExecutionRequest{ProjectID: projectID, Call: call})
	if policyErr != nil {
		return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "TOOL_POLICY_ERROR", ExitCode: -1, Output: map[string]any{"reason": policyErr.Error()}}
	}
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "TOOL_POLICY_DENIED", ExitCode: -1, InputHash: planned.InputHash, Idempotent: planned.Idempotent}
	if planned.Decision == core.PolicyDeny {
		result.Output = map[string]any{"reason": planned.Reason}
		return result
	}
	projectRoot, err := l.projectRoot(projectID)
	if err != nil {
		result.ErrorCode = "WORKSPACE_SCOPE_INVALID"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	switch call.Name {
	case core.ToolWorkspaceReadFile:
		return l.readFile(projectRoot, call, planned)
	case core.ToolWorkspaceListFiles:
		return l.listFiles(projectRoot, call, planned)
	case core.ToolWorkspaceFindFiles:
		return l.findFiles(projectRoot, call, planned)
	case core.ToolWorkspaceGrep:
		return l.grepFiles(projectRoot, call, planned)
	case core.ToolWorkspaceEditFile:
		return l.editFile(projectRoot, call, planned)
	case core.ToolWorkspaceWriteFile:
		return l.writeFile(projectRoot, call, planned)
	case core.ToolWorkspaceRunCommand:
		return l.runCommand(ctx, projectRoot, call, planned)
	case core.ToolWorkspaceShell:
		return l.executeShell(ctx, projectRoot, call, planned)
	case core.ToolWorkspaceExtractArchive:
		return l.extractArchive(projectRoot, call, planned)
	case core.ToolGitStatus, core.ToolGitDiff, core.ToolGitLog, core.ToolGitConflicts,
		core.ToolGitBranch, core.ToolGitCommit, core.ToolGitCommitPaths,
		core.ToolGitRestore, core.ToolGitRestoreHunk:
		return l.executeGit(ctx, projectRoot, call, planned)
	default:
		return result
	}
}

func (l *Local) projectRoot(projectID string) (string, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return "", fmt.Errorf("project id is not a UUID")
	}
	target := filepath.Join(l.root, projectID)
	relative, err := filepath.Rel(l.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("project workspace escapes the configured root")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("open project workspace: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("project workspace is not a real directory")
	}
	return target, nil
}

func (l *Local) writeFile(projectRoot string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ExitCode: -1, InputHash: planned.InputHash, Idempotent: true}
	relative := filepath.Clean(call.Arguments["path"].(string))
	content := []byte(call.Arguments["content"].(string))
	parent, err := secureParent(projectRoot, filepath.Dir(relative))
	if err != nil {
		result.ErrorCode = "WORKSPACE_PATH_DENIED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	target := filepath.Join(parent, filepath.Base(relative))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			result.ErrorCode = "WORKSPACE_PATH_DENIED"
			result.Output = map[string]any{"reason": "target is not a regular file"}
			return result
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil {
			result.ErrorCode = "WORKSPACE_READ_FAILED"
			result.Output = map[string]any{"reason": readErr.Error()}
			return result
		}
		if hashBytes(existing) == hashBytes(content) {
			result.Status = core.StatusSucceeded
			result.ErrorCode = ""
			result.ExitCode = 0
			result.Output = map[string]any{"path": filepath.ToSlash(relative), "content_hash": hashBytes(content), "size_bytes": len(content), "replayed": true}
			return result
		}
		result.ErrorCode = "WORKSPACE_FILE_EXISTS"
		result.Output = map[string]any{"path": filepath.ToSlash(relative), "reason": "create-only tool will not overwrite an existing file"}
		return result
	} else if !errors.Is(err, os.ErrNotExist) {
		result.ErrorCode = "WORKSPACE_PATH_DENIED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}

	temporary, err := os.CreateTemp(parent, ".oktapod-write-*")
	if err != nil {
		result.ErrorCode = "WORKSPACE_WRITE_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		result.ErrorCode = "WORKSPACE_WRITE_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		result.ErrorCode = "WORKSPACE_WRITE_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	if err := temporary.Close(); err != nil {
		result.ErrorCode = "WORKSPACE_WRITE_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	if err := os.Link(temporaryPath, target); err != nil {
		result.ErrorCode = "WORKSPACE_WRITE_CONFLICT"
		result.Output = map[string]any{"path": filepath.ToSlash(relative), "reason": "target appeared before the atomic create completed"}
		return result
	}
	result.Status = core.StatusSucceeded
	result.ExitCode = 0
	result.Output = map[string]any{"path": filepath.ToSlash(relative), "content_hash": hashBytes(content), "size_bytes": len(content), "replayed": false}
	return result
}

func (l *Local) extractArchive(projectRoot string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ExitCode: -1, InputHash: planned.InputHash, Idempotent: false}
	archivePath, _ := call.Arguments["archive_path"].(string)
	destDir, _ := call.Arguments["dest_dir"].(string)
	archivePath = strings.TrimSpace(archivePath)
	destDir = strings.TrimSpace(destDir)
	if archivePath == "" || destDir == "" {
		result.ErrorCode = "TOOL_INVALID_ARGUMENT"
		result.Output = map[string]any{"reason": "archive_path and dest_dir are required"}
		return result
	}
	fullArchivePath, err := secureJoin(projectRoot, archivePath)
	if err != nil {
		result.ErrorCode = "WORKSPACE_PATH_INVALID"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	if !strings.HasSuffix(strings.ToLower(fullArchivePath), ".zip") {
		result.ErrorCode = "TOOL_INVALID_ARGUMENT"
		result.Output = map[string]any{"reason": "only .zip archives are supported"}
		return result
	}
	fullDestPath, err := secureJoin(projectRoot, destDir)
	if err != nil {
		result.ErrorCode = "WORKSPACE_PATH_INVALID"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	reader, err := zip.OpenReader(fullArchivePath)
	if err != nil {
		result.ErrorCode = "ARCHIVE_READ_FAILED"
		result.Output = map[string]any{"reason": "cannot open archive: " + err.Error()}
		return result
	}
	defer reader.Close()
	if err := os.MkdirAll(fullDestPath, 0o755); err != nil {
		result.ErrorCode = "WORKSPACE_SCOPE_INVALID"
		result.Output = map[string]any{"reason": "cannot create destination directory: " + err.Error()}
		return result
	}
	const maxFiles = 5000
	const maxExpanded = 200 << 20 // 200 MiB
	var expanded uint64
	extracted := 0
	for i, file := range reader.File {
		if i >= maxFiles {
			result.ErrorCode = "ARCHIVE_TOO_LARGE"
			result.Output = map[string]any{"reason": fmt.Sprintf("archive exceeds %d file limit", maxFiles)}
			return result
		}
		if file.FileInfo().IsDir() {
			dirPath, err := secureJoin(fullDestPath, file.Name)
			if err != nil {
				continue
			}
			_ = os.MkdirAll(dirPath, 0o755)
			continue
		}
		if !file.Mode().IsRegular() {
			continue
		}
		if uint64(file.UncompressedSize64) > maxExpanded-expanded {
			result.ErrorCode = "ARCHIVE_TOO_LARGE"
			result.Output = map[string]any{"reason": "archive exceeds 200 MiB expanded limit"}
			return result
		}
		filePath, err := secureJoin(fullDestPath, file.Name)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			continue
		}
		src, err := file.Open()
		if err != nil {
			continue
		}
		dst, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			_ = src.Close()
			continue
		}
		n, _ := io.Copy(dst, io.LimitReader(src, maxExpanded))
		expanded += uint64(n)
		_ = dst.Close()
		_ = src.Close()
		extracted++
	}
	result.Status = core.StatusSucceeded
	result.ExitCode = 0
	result.Output = map[string]any{"extracted_files": extracted, "dest_dir": destDir}
	return result
}

func (l *Local) executeShell(ctx context.Context, projectRoot string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ExitCode: -1, InputHash: planned.InputHash, Idempotent: false}
	command, _ := call.Arguments["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		result.ErrorCode = "TOOL_INVALID_ARGUMENT"
		result.Output = map[string]any{"reason": "command is required"}
		return result
	}
	if reason := validateShellCommand(command); reason != "" {
		result.ErrorCode = "SHELL_COMMAND_DENIED"
		result.Output = map[string]any{"reason": reason}
		return result
	}
	timeout := 5 * time.Minute
	if v, ok := call.Arguments["timeout_seconds"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
		if timeout > 5*time.Minute {
			timeout = 5 * time.Minute
		}
	}
	shellCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(shellCtx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(shellCtx, "bash", "-c", command)
	}
	configureCommand(cmd)
	cmd.Cancel = func() error { return terminateProcessTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = projectRoot
	environment, err := safeEnvironment(projectRoot)
	if err != nil {
		result.ErrorCode = "TOOL_ENVIRONMENT_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	cmd.Env = environment
	stdout := &boundedBuffer{limit: maxCommandOutput}
	stderr := &boundedBuffer{limit: maxCommandOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	result.Output = map[string]any{
		"command":         command,
		"stdout":          stdout.String(),
		"stderr":          stderr.String(),
		"stdout_truncated": stdout.truncated,
		"stderr_truncated": stderr.truncated,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if shellCtx.Err() == context.DeadlineExceeded {
		result.ErrorCode = "SHELL_TIMEOUT"
		result.Output = map[string]any{"reason": "shell command exceeded the timeout"}
		return result
	}
	if err != nil {
		result.Status = core.StatusFailed
		return result
	}
	result.Status = core.StatusSucceeded
	result.ExitCode = 0
	return result
}

// validateShellCommand rejects dangerous shell commands. The blacklist is
// defense-in-depth, not a security boundary; scene policy and human approval
// remain the actual control plane.
func validateShellCommand(command string) string {
	lower := strings.ToLower(command)
	dangerousPatterns := []string{
		"rm -rf /", "rm -rf ~", "rm -rf *",
		"rm -rf $home", "rm -rf $pwd",
		"mkfs.", "dd if=/dev/zero", "dd if=/dev/urandom",
		" > /dev/sd", " > /dev/nvme", " > /dev/hd",
		":(){:|:&};:",
		"chmod -r 777 /", "chown -r ",
		"mkfs ", "fdisk ", "format ",
	}
	for _, p := range dangerousPatterns {
		if strings.Contains(lower, p) {
			return "dangerous pattern detected: " + p
		}
	}
	if strings.HasPrefix(strings.TrimSpace(lower), "rm -rf ") && (strings.Contains(command, "..") || strings.HasSuffix(command, "/")) {
		return "recursive delete with unsafe path"
	}
	return ""
}

func (l *Local) runCommand(ctx context.Context, projectRoot string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ExitCode: -1, InputHash: planned.InputHash, Idempotent: false}
	program := call.Arguments["program"].(string)
	rawArgs := call.Arguments["args"].([]any)
	args := make([]string, 0, len(rawArgs))
	for _, raw := range rawArgs {
		args = append(args, raw.(string))
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandCtx, program, args...)
	configureCommand(command)
	// CommandContext only terminates the direct process. The workspace adapter
	// owns the process tree so a timeout/cancel cannot leave a child running
	// against the project workspace after the Step has been fenced.
	command.Cancel = func() error { return terminateProcessTree(command) }
	command.WaitDelay = 2 * time.Second
	command.Dir = projectRoot
	environment, err := safeEnvironment(projectRoot)
	if err != nil {
		result.ErrorCode = "TOOL_ENVIRONMENT_FAILED"
		result.Output = map[string]any{"reason": err.Error()}
		return result
	}
	command.Env = environment
	stdout := &boundedBuffer{limit: maxCommandOutput}
	stderr := &boundedBuffer{limit: maxCommandOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	result.Output = map[string]any{
		"program":          program,
		"args":             args,
		"stdout":           stdout.String(),
		"stderr":           stderr.String(),
		"stdout_truncated": stdout.truncated,
		"stderr_truncated": stderr.truncated,
	}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		result.Status = core.StatusSucceeded
		result.ExitCode = 0
		return result
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		result.ErrorCode = "TOOL_TIMEOUT"
		return result
	}
	if errors.Is(commandCtx.Err(), context.Canceled) {
		result.ErrorCode = "TOOL_CANCELED"
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ErrorCode = "COMMAND_EXIT_NONZERO"
		return result
	}
	result.ErrorCode = "COMMAND_START_FAILED"
	result.Output["reason"] = err.Error()
	return result
}

// secureJoin resolves a project-relative path and rejects traversal attempts
// without creating directories. Used by extractArchive to validate both the
// archive source and the extraction destination.
func secureJoin(root, relative string) (string, error) {
	clean := filepath.Clean(relative)
	if clean == "." {
		return root, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the project workspace")
	}
	return filepath.Join(root, clean), nil
}

func secureParent(root, relative string) (string, error) {
	clean := filepath.Clean(relative)
	if clean == "." {
		return root, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the project workspace")
	}
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid workspace path segment")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("workspace path traverses a symlink or non-directory")
		}
	}
	return current, nil
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func safeEnvironment(projectRoot string) ([]string, error) {
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true,
		"TEMP": true, "TMP": true, "LANG": true, "LC_ALL": true,
	}
	result := make([]string, 0, len(allowed))
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if found && allowed[strings.ToUpper(name)] {
			result = append(result, item)
		}
	}
	runtimeHome, err := secureParent(projectRoot, filepath.Join(".oktapod", "runtime-cache"))
	if err != nil {
		return nil, fmt.Errorf("create isolated command environment: %w", err)
	}
	goBuildCache, err := secureParent(runtimeHome, "go-build")
	if err != nil {
		return nil, err
	}
	goModuleCache, err := secureParent(runtimeHome, "go-mod")
	if err != nil {
		return nil, err
	}
	result = append(result,
		"HOME="+runtimeHome,
		"USERPROFILE="+runtimeHome,
		"XDG_CACHE_HOME="+runtimeHome,
		"GOCACHE="+goBuildCache,
		"GOMODCACHE="+goModuleCache,
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOTELEMETRY=off",
		"CI=1",
		"NO_COLOR=1",
	)
	return result, nil
}

type boundedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	if original > remaining {
		b.truncated = true
	}
	return original, nil
}

func (b *boundedBuffer) String() string {
	return string(b.data)
}

var _ io.Writer = (*boundedBuffer)(nil)

// ExecuteChatTool implements application.ChatToolExecutor. It runs a single
// read-only tool call and returns the result as a plain text string suitable
// for feeding back into a model conversation. Only read-only tools are allowed;
// write or command tools are rejected.
func (l *Local) ExecuteChatTool(ctx context.Context, projectID string, call core.ToolCall) (string, error) {
	switch call.Name {
	case core.ToolWorkspaceReadFile, core.ToolWorkspaceListFiles,
		core.ToolWorkspaceFindFiles, core.ToolWorkspaceGrep,
		core.ToolGitStatus, core.ToolGitDiff, core.ToolGitLog, core.ToolGitConflicts:
		// allowed read-only tools
	default:
		return "", fmt.Errorf("chat mode only allows read-only workspace tools, got: %s", call.Name)
	}
	execution := l.Execute(ctx, projectID, call)
	if execution.Status != core.StatusSucceeded {
		reason, _ := execution.Output["reason"].(string)
		if reason == "" {
			reason = execution.ErrorCode
		}
		return "", fmt.Errorf("%s: %s", execution.ErrorCode, reason)
	}
	return toolExecutionText(execution), nil
}

// toolExecutionText extracts a human/model-readable text summary from a
// ToolExecution output map.
func toolExecutionText(execution core.ToolExecution) string {
	if execution.Output == nil {
		return ""
	}
	if content, ok := execution.Output["content"].(string); ok && content != "" {
		return content
	}
	// list_files / find_files return "entries"
	if entries, ok := execution.Output["entries"].([]any); ok {
		var sb strings.Builder
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok {
				if name, _ := m["name"].(string); name != "" {
					sb.WriteString(name)
					sb.WriteByte('\n')
				}
			}
		}
		return sb.String()
	}
	// Fallback: JSON-encode the output
	data, err := json.Marshal(execution.Output)
	if err != nil {
		return ""
	}
	return string(data)
}
