package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (l *Local) executeGit(ctx context.Context, root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	idempotent := call.Name == core.ToolGitStatus || call.Name == core.ToolGitDiff || call.Name == core.ToolGitLog || call.Name == core.ToolGitConflicts
	result := workspaceResult(call, planned, idempotent)
	top, _, err := l.runGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(strings.TrimSpace(top), root) {
		return workspaceFailure(result, "GIT_REPOSITORY_INVALID", errors.New("project workspace is not the Git repository root"))
	}
	switch call.Name {
	case core.ToolGitStatus:
		status, statusHash, err := l.gitStatus(ctx, root)
		if err != nil {
			return workspaceFailure(result, "GIT_STATUS_FAILED", err)
		}
		head, _, headErr := l.runGit(ctx, root, "rev-parse", "HEAD")
		if headErr != nil {
			return workspaceFailure(result, "GIT_STATUS_FAILED", headErr)
		}
		return workspaceSuccess(result, map[string]any{"status": redactText(status), "status_hash": statusHash, "head": strings.TrimSpace(head)})
	case core.ToolGitDiff:
		args := []string{"diff", "--no-ext-diff"}
		if baseline := stringArgument(call.Arguments, "baseline"); baseline != "" {
			args = append(args, baseline)
		} else if staged, _ := call.Arguments["staged"].(bool); staged {
			args = append(args, "--cached")
		}
		if path := stringArgument(call.Arguments, "path"); path != "" {
			if _, err := securePotentialFilePath(root, path); err != nil {
				return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
			}
			args = append(args, "--", filepathSlash(path))
		}
		output, truncated, err := l.runGit(ctx, root, args...)
		if err != nil {
			return workspaceFailure(result, "GIT_DIFF_FAILED", err)
		}
		output, untrackedTruncated, err := l.appendUntrackedDiffs(ctx, root, output, stringArgument(call.Arguments, "path"))
		truncated = truncated || untrackedTruncated
		if err != nil {
			return workspaceFailure(result, "GIT_DIFF_FAILED", err)
		}
		return workspaceSuccess(result, map[string]any{"diff": redactText(output), "truncated": truncated})
	case core.ToolGitLog:
		limit := integerArgument(call.Arguments, "limit", 20)
		output, truncated, err := l.runGit(ctx, root, "log", "--no-decorate", "--date=iso-strict", "--pretty=format:%H%x09%aI%x09%an%x09%s", "-n", strconv.Itoa(limit))
		if err != nil {
			return workspaceFailure(result, "GIT_LOG_FAILED", err)
		}
		return workspaceSuccess(result, map[string]any{"log": redactText(output), "truncated": truncated})
	case core.ToolGitConflicts:
		output, truncated, err := l.runGit(ctx, root, "diff", "--name-only", "--diff-filter=U", "-z")
		if err != nil {
			return workspaceFailure(result, "GIT_CONFLICTS_FAILED", err)
		}
		raw := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
		files := make([]string, 0, len(raw))
		for _, path := range raw {
			if path != "" {
				files = append(files, filepathSlash(path))
			}
		}
		return workspaceSuccess(result, map[string]any{"files": files, "count": len(files), "truncated": truncated})
	}

	before, beforeHash, err := l.gitStatus(ctx, root)
	if err != nil {
		return workspaceFailure(result, "GIT_STATUS_FAILED", err)
	}
	expected := stringArgument(call.Arguments, "expected_status_hash")
	if beforeHash != expected {
		return workspaceFailure(result, "GIT_STATE_CONFLICT", errors.New("Git status changed since approval was prepared"))
	}
	var output string
	var truncated bool
	switch call.Name {
	case core.ToolGitBranch:
		name := stringArgument(call.Arguments, "name")
		if _, _, err := l.runGit(ctx, root, "check-ref-format", "--branch", name); err != nil {
			return workspaceFailure(result, "GIT_BRANCH_INVALID", err)
		}
		output, truncated, err = l.runGit(ctx, root, "branch", "--", name)
	case core.ToolGitCommit:
		output, truncated, err = l.runGit(ctx, root, "commit", "-m", stringArgument(call.Arguments, "message"))
	case core.ToolGitCommitPaths:
		args := []string{"commit", "--only", "-m", stringArgument(call.Arguments, "message"), "--"}
		addArgs := []string{"add", "--"}
		for _, path := range stringSliceArgument(call.Arguments, "paths") {
			if _, pathErr := securePotentialFilePath(root, path); pathErr != nil {
				return workspaceFailure(result, "WORKSPACE_PATH_DENIED", pathErr)
			}
			addArgs = append(addArgs, filepathSlash(path))
			args = append(args, filepathSlash(path))
		}
		if _, _, err = l.runGit(ctx, root, addArgs...); err != nil {
			break
		}
		output, truncated, err = l.runGit(ctx, root, args...)
	case core.ToolGitRestore:
		path := stringArgument(call.Arguments, "path")
		target, pathErr := secureExistingPath(root, path, false)
		if pathErr != nil {
			return workspaceFailure(result, "WORKSPACE_PATH_DENIED", pathErr)
		}
		if _, _, trackedErr := l.runGit(ctx, root, "ls-files", "--error-unmatch", "--", filepathSlash(path)); trackedErr != nil {
			err = os.Remove(target)
			output = "removed untracked file " + filepathSlash(path)
		} else {
			output, truncated, err = l.runGit(ctx, root, "restore", "--source=HEAD", "--worktree", "--", filepathSlash(path))
		}
	case core.ToolGitRestoreHunk:
		path := stringArgument(call.Arguments, "path")
		if _, pathErr := securePotentialFilePath(root, path); pathErr != nil {
			return workspaceFailure(result, "WORKSPACE_PATH_DENIED", pathErr)
		}
		diff, diffTruncated, diffErr := l.runGit(ctx, root, "diff", "--no-ext-diff", "--unified=0", "--", filepathSlash(path))
		if diffErr != nil {
			return workspaceFailure(result, "GIT_DIFF_FAILED", diffErr)
		}
		if diffTruncated {
			return workspaceFailure(result, "GIT_DIFF_TRUNCATED", errors.New("cannot restore a hunk from a truncated diff"))
		}
		patch, patchErr := selectDiffHunk(diff, integerArgument(call.Arguments, "hunk_index", -1))
		if patchErr != nil {
			return workspaceFailure(result, "GIT_HUNK_NOT_FOUND", patchErr)
		}
		output, truncated, err = l.runGitInput(ctx, root, patch, "apply", "--reverse", "--unidiff-zero", "--whitespace=nowarn", "-")
	}
	if err != nil {
		return workspaceFailure(result, "GIT_MUTATION_FAILED", err)
	}
	after, afterHash, statusErr := l.gitStatus(ctx, root)
	if statusErr != nil {
		return workspaceFailure(result, "GIT_STATUS_FAILED", statusErr)
	}
	return workspaceSuccess(result, map[string]any{
		"stdout": redactText(output), "truncated": truncated,
		"before_status": redactText(before), "before_status_hash": beforeHash,
		"after_status": redactText(after), "after_status_hash": afterHash,
	})
}

func (l *Local) gitStatus(ctx context.Context, root string) (string, string, error) {
	output, _, err := l.runGit(ctx, root, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return "", "", err
	}
	return output, hashBytes([]byte(output)), nil
}

func (l *Local) runGit(ctx context.Context, root string, args ...string) (string, bool, error) {
	return l.runGitInput(ctx, root, "", args...)
}

func (l *Local) runGitInput(ctx context.Context, root, input string, args ...string) (string, bool, error) {
	return l.runGitInputAllowed(ctx, root, input, -1, args...)
}

func (l *Local) runGitInputAllowed(ctx context.Context, root, input string, allowedExitCode int, args ...string) (string, bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = root
	environment, err := safeEnvironment(root)
	if err != nil {
		return "", false, err
	}
	command.Env = environment
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
	stdout := &boundedBuffer{limit: maxCommandOutput}
	stderr := &boundedBuffer{limit: maxCommandOutput}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if err != nil {
		var exitError *exec.ExitError
		if allowedExitCode >= 0 && errors.As(err, &exitError) && exitError.ExitCode() == allowedExitCode {
			return stdout.String(), stdout.truncated || stderr.truncated, nil
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return stdout.String(), stdout.truncated || stderr.truncated, errors.New(redactText(detail))
	}
	return stdout.String(), stdout.truncated, nil
}

func (l *Local) appendUntrackedDiffs(ctx context.Context, root, trackedDiff, selectedPath string) (string, bool, error) {
	output := &boundedBuffer{limit: maxCommandOutput}
	_, _ = output.Write([]byte(trackedDiff))
	untracked, listTruncated, err := l.runGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", output.truncated || listTruncated, err
	}
	selectedPath = filepathSlash(strings.TrimSpace(selectedPath))
	untrackedPaths := strings.Split(untracked, "\x00")
	if listTruncated && len(untrackedPaths) > 0 {
		// A bounded listing may end in the middle of a path. Preserve the
		// completed entries and report truncation instead of resolving an
		// incomplete path or failing the whole diff.
		untrackedPaths = untrackedPaths[:len(untrackedPaths)-1]
	}
	for _, path := range untrackedPaths {
		path = filepathSlash(path)
		if path == "" || (selectedPath != "" && path != selectedPath && !strings.HasPrefix(path, strings.TrimSuffix(selectedPath, "/")+"/")) {
			continue
		}
		if _, err := secureExistingPath(root, path, false); err != nil {
			return "", output.truncated || listTruncated, err
		}
		diff, truncated, err := l.runGitInputAllowed(ctx, root, "", 1, "diff", "--no-index", "--no-ext-diff", "--", "/dev/null", path)
		if err != nil {
			return "", output.truncated || listTruncated || truncated, err
		}
		if output.String() != "" && !strings.HasSuffix(output.String(), "\n") {
			_, _ = output.Write([]byte("\n"))
		}
		_, _ = output.Write([]byte(diff))
		listTruncated = listTruncated || truncated
	}
	return output.String(), output.truncated || listTruncated, nil
}

func selectDiffHunk(diff string, index int) (string, error) {
	if index < 0 {
		return "", errors.New("hunk index is invalid")
	}
	lines := strings.SplitAfter(diff, "\n")
	firstHunk := -1
	hunks := make([]int, 0, 8)
	for position, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			if firstHunk < 0 {
				firstHunk = position
			}
			hunks = append(hunks, position)
		}
	}
	if firstHunk < 0 || index >= len(hunks) {
		return "", fmt.Errorf("hunk %d does not exist; diff has %d hunks", index, len(hunks))
	}
	end := len(lines)
	if index+1 < len(hunks) {
		end = hunks[index+1]
	}
	return strings.Join(lines[:firstHunk], "") + strings.Join(lines[hunks[index]:end], ""), nil
}

func stringSliceArgument(arguments map[string]any, key string) []string {
	values, _ := arguments[key].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func samePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	left, leftErr = filepath.Abs(filepath.Clean(left))
	right, rightErr = filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	left = strings.TrimRight(filepathSlash(left), "/")
	right = strings.TrimRight(filepathSlash(right), "/")
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}
