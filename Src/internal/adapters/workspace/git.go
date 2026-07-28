package workspace

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
		return workspaceSuccess(result, map[string]any{"status": redactText(status), "status_hash": statusHash})
	case core.ToolGitDiff:
		args := []string{"diff", "--no-ext-diff"}
		if staged, _ := call.Arguments["staged"].(bool); staged {
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
		for _, path := range stringSliceArgument(call.Arguments, "paths") {
			if _, pathErr := securePotentialFilePath(root, path); pathErr != nil {
				return workspaceFailure(result, "WORKSPACE_PATH_DENIED", pathErr)
			}
			args = append(args, filepathSlash(path))
		}
		output, truncated, err = l.runGit(ctx, root, args...)
	case core.ToolGitRestore:
		path := stringArgument(call.Arguments, "path")
		if _, pathErr := securePotentialFilePath(root, path); pathErr != nil {
			return workspaceFailure(result, "WORKSPACE_PATH_DENIED", pathErr)
		}
		output, truncated, err = l.runGit(ctx, root, "restore", "--source=HEAD", "--worktree", "--", filepathSlash(path))
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
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return stdout.String(), stdout.truncated || stderr.truncated, errors.New(redactText(detail))
	}
	return stdout.String(), stdout.truncated, nil
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
	left = strings.TrimRight(strings.ReplaceAll(left, "\\", "/"), "/")
	right = strings.TrimRight(strings.ReplaceAll(right, "\\", "/"), "/")
	return strings.EqualFold(left, right)
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}
