package mcppackage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxMetadataBytes = 2 << 20

// Resolver resolves packages through fixed official metadata endpoints. It
// never accepts a caller-provided URL, which keeps package resolution free of
// SSRF paths and unrelated network credentials.
type Resolver struct {
	http         *http.Client
	npmEndpoint  string
	pypiEndpoint string
}

func New(client *http.Client) *Resolver {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Resolver{
		http:         client,
		npmEndpoint:  "https://registry.npmjs.org/",
		pypiEndpoint: "https://pypi.org/pypi/",
	}
}

func (r *Resolver) Resolve(ctx context.Context, ecosystem, packageName string) (string, error) {
	ecosystem = strings.ToLower(strings.TrimSpace(ecosystem))
	packageName = strings.TrimSpace(packageName)
	switch ecosystem {
	case "npm":
		return r.resolveNPM(ctx, packageName)
	case "pypi":
		return r.resolvePyPI(ctx, packageName)
	default:
		return "", fmt.Errorf("unsupported package ecosystem %q", ecosystem)
	}
}

func (r *Resolver) resolveNPM(ctx context.Context, packageName string) (string, error) {
	if !validNPMPackageName(packageName) {
		return "", fmt.Errorf("invalid npm package name")
	}
	endpoint := strings.TrimRight(r.npmEndpoint, "/") + "/" + url.PathEscape(packageName)
	var payload struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := r.getJSON(ctx, endpoint, &payload); err != nil {
		return "", err
	}
	version := strings.TrimSpace(payload.DistTags["latest"])
	if version == "" {
		return "", fmt.Errorf("npm metadata does not declare dist-tags.latest")
	}
	return version, nil
}

func (r *Resolver) resolvePyPI(ctx context.Context, packageName string) (string, error) {
	if !validPyPIPackageName(packageName) {
		return "", fmt.Errorf("invalid PyPI package name")
	}
	endpoint := strings.TrimRight(r.pypiEndpoint, "/") + "/" + url.PathEscape(packageName) + "/json"
	var payload struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := r.getJSON(ctx, endpoint, &payload); err != nil {
		return "", err
	}
	version := strings.TrimSpace(payload.Info.Version)
	if version == "" {
		return "", fmt.Errorf("PyPI metadata does not declare info.version")
	}
	return version, nil
}

func (r *Resolver) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "oktapod-mcp-package-resolver/1")
	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("package metadata was not found")
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("package metadata endpoint returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxMetadataBytes))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode package metadata: %w", err)
	}
	return nil
}

func validNPMPackageName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, "@") {
		parts := strings.SplitN(name, "/", 2)
		return len(parts) == 2 && len(parts[0]) > 1 && validPackageSegment(parts[0][1:]) && validPackageSegment(parts[1])
	}
	return validPackageSegment(name)
}

func validPyPIPackageName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validPackageSegment(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
