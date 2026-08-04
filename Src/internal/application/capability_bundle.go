package application

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	maxCapabilityArchiveBytes   = 50 << 20
	maxCapabilityExpandedBytes  = 200 << 20
	maxCapabilityArchiveFiles   = 5000
	maxCapabilityArchiveDepth   = 16
	maxCapabilitySingleFileSize = 20 << 20
	maxCapabilityEntryText      = 2 << 20
)

type normalizedCapabilityBundle struct {
	Kind         string
	Name         string
	Version      string
	ManifestJSON string
	Data         []byte
	Hash         string
}

type capabilityBundleFile struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}

func normalizeCapabilityBundle(encoded, sourceFilename, fallbackKind, fallbackName, fallbackVersion string) (normalizedCapabilityBundle, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxCapabilityArchiveBytes) {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability archive exceeds 50 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return normalizedCapabilityBundle{}, fmt.Errorf("bundle_base64 is invalid")
	}
	if len(data) == 0 || len(data) > maxCapabilityArchiveBytes {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability archive must be between 1 byte and 50 MiB")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability attachment must be a valid ZIP archive")
	}
	if len(reader.File) == 0 || len(reader.File) > maxCapabilityArchiveFiles {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability archive file count must be between 1 and %d", maxCapabilityArchiveFiles)
	}

	seen := make(map[string]string, len(reader.File))
	files := make([]capabilityBundleFile, 0, len(reader.File))
	var expanded uint64
	var skillFile *zip.File
	var skillPath string
	var packageFile *zip.File
	hasExecutable := false
	for _, file := range reader.File {
		normalizedPath, pathErr := safeCapabilityArchivePath(file.Name)
		if pathErr != nil {
			return normalizedCapabilityBundle{}, pathErr
		}
		if normalizedPath == "" || file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() {
			return normalizedCapabilityBundle{}, fmt.Errorf("capability archive links and special files are not allowed: %s", normalizedPath)
		}
		folded := strings.ToLower(norm.NFC.String(normalizedPath))
		if previous, exists := seen[folded]; exists {
			return normalizedCapabilityBundle{}, fmt.Errorf("capability archive contains a duplicate, case, or Unicode path collision: %s conflicts with %s", normalizedPath, previous)
		}
		seen[folded] = normalizedPath
		if file.UncompressedSize64 > maxCapabilitySingleFileSize {
			return normalizedCapabilityBundle{}, fmt.Errorf("capability archive file exceeds 20 MiB: %s", normalizedPath)
		}
		expanded += file.UncompressedSize64
		if expanded > maxCapabilityExpandedBytes {
			return normalizedCapabilityBundle{}, fmt.Errorf("capability archive expands beyond 200 MiB")
		}
		if file.CompressedSize64 > 0 && file.UncompressedSize64 > 1<<20 && file.UncompressedSize64/file.CompressedSize64 > 100 {
			return normalizedCapabilityBundle{}, fmt.Errorf("capability archive contains a suspicious compression ratio: %s", normalizedPath)
		}
		lowerBase := strings.ToLower(path.Base(normalizedPath))
		if lowerBase == "skill.md" {
			if skillFile != nil {
				return normalizedCapabilityBundle{}, fmt.Errorf("capability archive must contain exactly one SKILL.md")
			}
			skillFile = file
			skillPath = normalizedPath
		}
		if lowerBase == "capability.yaml" || lowerBase == "capability.yml" {
			if packageFile != nil {
				return normalizedCapabilityBundle{}, fmt.Errorf("capability archive must contain at most one capability.yaml")
			}
			packageFile = file
		}
		if capabilityArchiveExecutable(normalizedPath) {
			hasExecutable = true
		}
		files = append(files, capabilityBundleFile{Path: normalizedPath, Size: file.UncompressedSize64})
	}
	if len(files) == 0 {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability archive contains no regular files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if strings.EqualFold(strings.TrimSpace(fallbackKind), "skill") || skillFile != nil {
		if skillFile == nil {
			return normalizedCapabilityBundle{}, fmt.Errorf("Skill ZIP must contain exactly one SKILL.md")
		}
		if packageFile != nil {
			return normalizedCapabilityBundle{}, fmt.Errorf("Skill ZIP cannot contain capability.yaml")
		}
		if layoutErr := validateSkillBundleLayout(files, skillPath); layoutErr != nil {
			return normalizedCapabilityBundle{}, layoutErr
		}
	}

	kind := strings.TrimSpace(fallbackKind)
	name := strings.TrimSpace(fallbackName)
	version := strings.TrimSpace(fallbackVersion)
	manifest := map[string]any{}
	if packageFile != nil {
		body, readErr := readCapabilityArchiveText(packageFile)
		if readErr != nil {
			return normalizedCapabilityBundle{}, readErr
		}
		if yamlErr := yaml.Unmarshal(body, &manifest); yamlErr != nil {
			return normalizedCapabilityBundle{}, fmt.Errorf("invalid capability.yaml: %w", yamlErr)
		}
		if metadata, ok := manifest["metadata"].(map[string]any); ok {
			if value, ok := metadata["displayName"].(string); ok && strings.TrimSpace(value) != "" {
				name = strings.TrimSpace(value)
			} else if value, ok := metadata["id"].(string); ok && strings.TrimSpace(value) != "" {
				parts := strings.Split(strings.TrimSpace(value), "/")
				name = parts[len(parts)-1]
			}
			if value, ok := metadata["version"].(string); ok && strings.TrimSpace(value) != "" {
				version = strings.TrimSpace(value)
			}
		}
	}
	if skillFile != nil {
		body, readErr := readCapabilityArchiveText(skillFile)
		if readErr != nil {
			return normalizedCapabilityBundle{}, readErr
		}
		normalized, skillName, normalizeErr := normalizeSkillManifest(string(body), name)
		if normalizeErr != nil {
			return normalizedCapabilityBundle{}, normalizeErr
		}
		if jsonErr := json.Unmarshal([]byte(normalized), &manifest); jsonErr != nil {
			return normalizedCapabilityBundle{}, jsonErr
		}
		kind = "skill"
		name = skillName
	}
	if len(manifest) == 0 {
		return normalizedCapabilityBundle{}, fmt.Errorf("capability archive requires SKILL.md or capability.yaml")
	}
	if kind == "" {
		kind = "plugin"
	}
	if name == "" {
		name = strings.TrimSuffix(path.Base(sourceFilename), path.Ext(sourceFilename))
	}
	if version == "" {
		version = "0.1.0"
	}
	sum := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	manifest["_oktapod_bundle"] = map[string]any{
		"format":          "zip/v1",
		"source_filename": path.Base(strings.ReplaceAll(sourceFilename, "\\", "/")),
		"content_hash":    hash,
		"files":           files,
		"has_executable":  hasExecutable,
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return normalizedCapabilityBundle{}, err
	}
	return normalizedCapabilityBundle{Kind: kind, Name: name, Version: version, ManifestJSON: string(encodedManifest), Data: data, Hash: hash}, nil
}

func validateSkillBundleLayout(files []capabilityBundleFile, skillPath string) error {
	root := strings.TrimSuffix(skillPath, "/SKILL.md")
	if root == skillPath {
		root = ""
	}
	if root != "" && strings.Contains(root, "/") {
		return fmt.Errorf("Skill ZIP must place SKILL.md at the archive root or inside one top-level folder")
	}
	for _, file := range files {
		relative := file.Path
		if root != "" {
			prefix := root + "/"
			if !strings.HasPrefix(relative, prefix) {
				return fmt.Errorf("Skill ZIP files must share the same top-level folder: %s", relative)
			}
			relative = strings.TrimPrefix(relative, prefix)
		}
		lower := strings.ToLower(relative)
		segments := strings.Split(relative, "/")
		for _, segment := range segments {
			if strings.HasPrefix(segment, ".") {
				return fmt.Errorf("Skill ZIP hidden files and folders are not allowed: %s", relative)
			}
		}
		if strings.Contains("/"+lower+"/", "/node_modules/") || strings.Contains("/"+lower+"/", "/__pycache__/") {
			return fmt.Errorf("Skill ZIP must not vendor dependencies or caches: %s", relative)
		}
		if !strings.Contains(relative, "/") {
			if relative == "SKILL.md" || lower == "license" || strings.HasPrefix(lower, "license.") {
				continue
			}
			return fmt.Errorf("Skill ZIP contains an unsupported root file: %s", relative)
		}
		switch strings.ToLower(segments[0]) {
		case "agents", "scripts", "references", "assets":
		default:
			return fmt.Errorf("Skill ZIP contains an unsupported top-level folder: %s", segments[0])
		}
		if strings.EqualFold(path.Base(relative), "SKILL.md") {
			return fmt.Errorf("Skill ZIP cannot contain nested Skills: %s", relative)
		}
	}
	return nil
}

func safeCapabilityArchivePath(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("capability archive path contains NUL")
	}
	normalized := norm.NFC.String(strings.ReplaceAll(raw, "\\", "/"))
	if strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") || strings.Contains(normalized, ":") {
		return "", fmt.Errorf("capability archive contains an absolute, UNC, drive, or ADS path: %s", raw)
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("capability archive path escapes its root: %s", raw)
	}
	if depth := len(strings.Split(cleaned, "/")); depth > maxCapabilityArchiveDepth {
		return "", fmt.Errorf("capability archive path exceeds depth %d: %s", maxCapabilityArchiveDepth, raw)
	}
	return cleaned, nil
}

func readCapabilityArchiveText(file *zip.File) ([]byte, error) {
	if file.UncompressedSize64 > maxCapabilityEntryText {
		return nil, fmt.Errorf("capability entry exceeds 2 MiB: %s", file.Name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxCapabilityEntryText+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCapabilityEntryText {
		return nil, fmt.Errorf("capability entry exceeds 2 MiB: %s", file.Name)
	}
	return body, nil
}

func capabilityArchiveExecutable(name string) bool {
	extension := strings.ToLower(path.Ext(name))
	switch extension {
	case ".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".sh", ".bash", ".py", ".js", ".ts", ".jar", ".so", ".dylib":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(name), "scripts/") || strings.Contains(strings.ToLower(name), "/scripts/")
	}
}
