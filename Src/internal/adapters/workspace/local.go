package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
