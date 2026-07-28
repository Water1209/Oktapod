package workspace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	maxReadableFileBytes = 8 << 20
	maxGrepFileBytes     = 2 << 20
	maxWorkspaceScan     = 100_000
)

var secretAssignmentPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|auth[_-]?token|password|secret)\b(\s*[:=]\s*)([^\s"']+)`)

func (l *Local) readFile(root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := workspaceResult(call, planned, true)
	path, err := secureExistingPath(root, stringArgument(call.Arguments, "path"), false)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
	}
	content, err := readTextFile(path, maxReadableFileBytes)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_READ_FAILED", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	offset := integerArgument(call.Arguments, "offset_line", 0)
	limit := integerArgument(call.Arguments, "limit_lines", 400)
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	page := redactText(strings.Join(lines[offset:end], "\n"))
	output := map[string]any{
		"path": relativeSlash(root, path), "content": page, "content_hash": hashBytes(content),
		"size_bytes": len(content), "start_line": offset + 1, "end_line": end,
		"total_lines": len(lines), "truncated": end < len(lines),
	}
	if end < len(lines) {
		output["next_cursor"] = end
	}
	return workspaceSuccess(result, output)
}

func (l *Local) listFiles(root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := workspaceResult(call, planned, true)
	path, err := secureExistingPath(root, stringArgument(call.Arguments, "path"), true)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
	}
	recursive, _ := call.Arguments["recursive"].(bool)
	items := make([]map[string]any, 0)
	err = walkWorkspace(path, recursive, func(candidate string, entry fs.DirEntry) bool {
		if candidate == path {
			return true
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		info, _ := entry.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		items = append(items, map[string]any{"path": relativeSlash(root, candidate), "kind": kind, "size_bytes": size})
		return len(items) < maxWorkspaceScan
	})
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_LIST_FAILED", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["path"].(string) < items[j]["path"].(string) })
	page, next := pageMaps(items, integerArgument(call.Arguments, "cursor", 0), integerArgument(call.Arguments, "limit", 200))
	output := map[string]any{"path": relativeSlash(root, path), "items": page, "total_scanned": len(items), "truncated": next >= 0}
	if next >= 0 {
		output["next_cursor"] = next
	}
	return workspaceSuccess(result, output)
}

func (l *Local) findFiles(root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := workspaceResult(call, planned, true)
	base, err := secureExistingPath(root, stringArgument(call.Arguments, "path"), true)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
	}
	pattern := stringArgument(call.Arguments, "pattern")
	if _, err := filepath.Match(pattern, "probe"); err != nil {
		return workspaceFailure(result, "WORKSPACE_PATTERN_INVALID", err)
	}
	items := make([]map[string]any, 0)
	err = walkWorkspace(base, true, func(candidate string, entry fs.DirEntry) bool {
		if candidate == base || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return true
		}
		relative := relativeSlash(root, candidate)
		matched, _ := filepath.Match(pattern, filepath.Base(candidate))
		if !matched {
			matched, _ = filepath.Match(filepath.ToSlash(pattern), relative)
		}
		if matched {
			items = append(items, map[string]any{"path": relative})
		}
		return len(items) < maxWorkspaceScan
	})
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_FIND_FAILED", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["path"].(string) < items[j]["path"].(string) })
	page, next := pageMaps(items, integerArgument(call.Arguments, "cursor", 0), integerArgument(call.Arguments, "limit", 200))
	output := map[string]any{"items": page, "total_matches": len(items), "truncated": next >= 0}
	if next >= 0 {
		output["next_cursor"] = next
	}
	return workspaceSuccess(result, output)
}

func (l *Local) grepFiles(root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := workspaceResult(call, planned, true)
	base, err := secureExistingPath(root, stringArgument(call.Arguments, "path"), true)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
	}
	query := stringArgument(call.Arguments, "query")
	glob := stringArgument(call.Arguments, "glob")
	matches := make([]map[string]any, 0)
	scanned := 0
	err = walkWorkspace(base, true, func(candidate string, entry fs.DirEntry) bool {
		if candidate == base || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return true
		}
		if scanned >= maxWorkspaceScan {
			return false
		}
		if glob != "" {
			matched, matchErr := filepath.Match(glob, filepath.Base(candidate))
			if matchErr != nil || !matched {
				return true
			}
		}
		scanned++
		content, readErr := readTextFile(candidate, maxGrepFileBytes)
		if readErr != nil {
			return true
		}
		scanner := bufio.NewScanner(bytes.NewReader(content))
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		line := 0
		for scanner.Scan() {
			line++
			if strings.Contains(scanner.Text(), query) {
				matches = append(matches, map[string]any{"path": relativeSlash(root, candidate), "line": line, "text": redactText(scanner.Text())})
			}
		}
		return len(matches) < maxWorkspaceScan
	})
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_GREP_FAILED", err)
	}
	page, next := pageMaps(matches, integerArgument(call.Arguments, "cursor", 0), integerArgument(call.Arguments, "limit", 200))
	output := map[string]any{"items": page, "files_scanned": scanned, "total_matches": len(matches), "truncated": next >= 0}
	if next >= 0 {
		output["next_cursor"] = next
	}
	return workspaceSuccess(result, output)
}

func (l *Local) editFile(root string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := workspaceResult(call, planned, true)
	target, err := secureExistingPath(root, stringArgument(call.Arguments, "path"), false)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_PATH_DENIED", err)
	}
	content, err := readTextFile(target, maxReadableFileBytes)
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_READ_FAILED", err)
	}
	expected := stringArgument(call.Arguments, "preimage_hash")
	if hashBytes(content) != expected {
		return workspaceFailure(result, "WORKSPACE_EDIT_CONFLICT", errors.New("file content changed since the edit was prepared"))
	}
	oldText, newText := rawStringArgument(call.Arguments, "old_text"), rawStringArgument(call.Arguments, "new_text")
	if bytes.Count(content, []byte(oldText)) != 1 {
		return workspaceFailure(result, "WORKSPACE_EDIT_AMBIGUOUS", errors.New("old_text must match exactly once"))
	}
	updated := bytes.Replace(content, []byte(oldText), []byte(newText), 1)
	temporary, err := os.CreateTemp(filepath.Dir(target), ".oktapod-edit-*")
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_WRITE_FAILED", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(updated)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return workspaceFailure(result, "WORKSPACE_WRITE_FAILED", err)
	}
	latest, err := os.ReadFile(target)
	if err != nil || hashBytes(latest) != expected {
		return workspaceFailure(result, "WORKSPACE_EDIT_CONFLICT", errors.New("file changed before atomic replacement"))
	}
	if err := atomicReplaceFile(temporaryPath, target); err != nil {
		return workspaceFailure(result, "WORKSPACE_WRITE_FAILED", err)
	}
	return workspaceSuccess(result, map[string]any{
		"path": relativeSlash(root, target), "preimage_hash": expected,
		"content_hash": hashBytes(updated), "size_bytes": len(updated),
	})
}

func secureExistingPath(root, relative string, allowDirectory bool) (string, error) {
	relative = filepath.Clean(strings.TrimSpace(relative))
	if relative == "" || relative == "." {
		if allowDirectory {
			return root, nil
		}
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the project workspace")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid workspace path segment")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("workspace path traverses a symlink")
		}
	}
	info, err := os.Lstat(current)
	if err != nil {
		return "", err
	}
	if allowDirectory && !info.IsDir() {
		return "", errors.New("workspace path is not a directory")
	}
	if !allowDirectory && !info.Mode().IsRegular() {
		return "", errors.New("workspace path is not a regular file")
	}
	return current, nil
}

func securePotentialFilePath(root, relative string) (string, error) {
	relative = filepath.Clean(strings.TrimSpace(relative))
	if relative == "" || relative == "." || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid project-relative file path")
	}
	parent, err := secureExistingPath(root, filepath.Dir(relative), true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, filepath.Base(relative))
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("workspace target is a symlink")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return target, nil
}

func walkWorkspace(root string, recursive bool, visit func(string, fs.DirEntry) bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && entry.Type()&os.ModeSymlink != 0 {
			if !visit(path, entry) {
				return fs.SkipAll
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != root && entry.IsDir() && !recursive {
			if !visit(path, entry) {
				return fs.SkipAll
			}
			return fs.SkipDir
		}
		if !visit(path, entry) {
			return fs.SkipAll
		}
		return nil
	})
}

func readTextFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("file is not a regular text file within the %d-byte limit", maximum)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	probe := content
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if bytes.IndexByte(probe, 0) >= 0 || !utf8.Valid(content) {
		return nil, errors.New("binary or non-UTF-8 file is not supported")
	}
	return content, nil
}

func pageMaps(items []map[string]any, cursor, limit int) ([]map[string]any, int) {
	if cursor > len(items) {
		cursor = len(items)
	}
	end := cursor + limit
	if end > len(items) {
		end = len(items)
	}
	next := -1
	if end < len(items) {
		next = end
	}
	return items[cursor:end], next
}

func integerArgument(arguments map[string]any, key string, fallback int) int {
	value, ok := arguments[key].(float64)
	if !ok {
		return fallback
	}
	return int(value)
}

func stringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}

func rawStringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func relativeSlash(root, target string) string {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	if relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func redactText(value string) string {
	value = secretAssignmentPattern.ReplaceAllString(value, `${1}${2}[REDACTED]`)
	if strings.Contains(value, "-----BEGIN PRIVATE KEY-----") {
		return "[REDACTED PRIVATE KEY]"
	}
	return value
}

func workspaceResult(call core.ToolCall, planned core.PlannedTool, idempotent bool) core.ToolExecution {
	return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ExitCode: -1, InputHash: planned.InputHash, Idempotent: idempotent}
}

func workspaceFailure(result core.ToolExecution, code string, err error) core.ToolExecution {
	result.ErrorCode = code
	result.Output = map[string]any{"reason": redactText(err.Error())}
	return result
}

func workspaceSuccess(result core.ToolExecution, output map[string]any) core.ToolExecution {
	result.Status = core.StatusSucceeded
	result.ErrorCode = ""
	result.ExitCode = 0
	result.Output = output
	return result
}
