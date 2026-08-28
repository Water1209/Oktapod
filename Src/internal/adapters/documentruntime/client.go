package documentruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
)

const protocolVersion = "oktapod.document/v1"

type signedManifest struct {
	SignedPayload string `json:"signed_payload"`
	Signature     struct {
		Algorithm string `json:"algorithm"`
		KeyID     string `json:"key_id"`
		Value     string `json:"value"`
	} `json:"signature"`
}

type manifestPayload struct {
	SchemaVersion string            `json:"schema_version"`
	Version       string            `json:"version"`
	Protocol      string            `json:"protocol"`
	Platform      string            `json:"platform"`
	Python        string            `json:"python"`
	Entrypoint    string            `json:"entrypoint"`
	LibreOffice   string            `json:"libreoffice,omitempty"`
	Tesseract     string            `json:"tesseract,omitempty"`
	Files         map[string]string `json:"files"`
}

type rpcError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func (e *rpcError) Error() string { return e.Code + ": " + e.Message }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type Client struct {
	packageRoot string
	workRoot    string
	manifest    manifestPayload
	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	decoder     *json.Decoder
	stderr      *boundedWriter
	nextID      atomic.Int64
}

func Discover(dataRoot string, trustedKeys map[string]ed25519.PublicKey) (*Client, error) {
	paths := make([]string, 0, 3)
	if explicit := strings.TrimSpace(os.Getenv("OKTAPOD_DOCUMENT_RUNTIME_ROOT")); explicit != "" {
		paths = append(paths, explicit)
	}
	paths = append(paths, filepath.Join(dataRoot, "runtimes", "document-runtime", "current"))
	if executable, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(executable), "runtime", "document-runtime"))
	}
	development := strings.TrimSpace(os.Getenv("OKTAPOD_DOCUMENT_RUNTIME_DEV")) == "1"
	if development {
		if cwd, err := os.Getwd(); err == nil {
			paths = append(paths, filepath.Join(cwd, "Runtime", "document-runtime"))
		}
	}
	var failures []string
	for _, candidate := range paths {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(absolute, "runtime-manifest.json")); err != nil {
			if development && fileExists(filepath.Join(absolute, "runtime.py")) {
				client, devErr := newDevelopmentClient(absolute, filepath.Join(dataRoot, "document-runtime"))
				if devErr == nil {
					return client, nil
				}
				failures = append(failures, devErr.Error())
			}
			continue
		}
		client, err := NewClient(absolute, filepath.Join(dataRoot, "document-runtime"), trustedKeys)
		if err == nil {
			return client, nil
		}
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("document runtime unavailable: %s", strings.Join(failures, "; "))
	}
	return nil, fmt.Errorf("document runtime package is not installed")
}

func NewClient(packageRoot, workRoot string, trustedKeys map[string]ed25519.PublicKey) (*Client, error) {
	payload, err := verifyManifest(packageRoot, trustedKeys)
	if err != nil {
		return nil, err
	}
	return newClient(packageRoot, workRoot, payload)
}

func newDevelopmentClient(packageRoot, workRoot string) (*Client, error) {
	python := strings.TrimSpace(os.Getenv("OKTAPOD_DOCUMENT_RUNTIME_PYTHON"))
	if python == "" {
		python = "python"
	}
	payload := manifestPayload{
		SchemaVersion: "oktapod.document-runtime/v1", Version: "1.0.0-dev", Protocol: protocolVersion,
		Platform: runtime.GOOS + "-" + runtime.GOARCH, Python: python, Entrypoint: "runtime.py",
		LibreOffice: strings.TrimSpace(os.Getenv("OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE")),
		Tesseract:   strings.TrimSpace(os.Getenv("OKTAPOD_DOCUMENT_RUNTIME_TESSERACT")),
		Files:       map[string]string{},
	}
	return newClient(packageRoot, workRoot, payload)
}

func newClient(packageRoot, workRoot string, payload manifestPayload) (*Client, error) {
	packageAbsolute, err := filepath.Abs(packageRoot)
	if err != nil {
		return nil, err
	}
	workAbsolute, err := filepath.Abs(workRoot)
	if err != nil {
		return nil, err
	}
	if payload.Protocol != protocolVersion || strings.TrimSpace(payload.Version) == "" {
		return nil, fmt.Errorf("document runtime protocol or version is incompatible")
	}
	if err := os.MkdirAll(workAbsolute, 0o700); err != nil {
		return nil, err
	}
	return &Client{packageRoot: packageAbsolute, workRoot: workAbsolute, manifest: payload}, nil
}

func verifyManifest(root string, trustedKeys map[string]ed25519.PublicKey) (manifestPayload, error) {
	body, err := os.ReadFile(filepath.Join(root, "runtime-manifest.json"))
	if err != nil {
		return manifestPayload{}, err
	}
	var envelope signedManifest
	if err := json.Unmarshal(body, &envelope); err != nil {
		return manifestPayload{}, fmt.Errorf("decode document runtime manifest: %w", err)
	}
	payloadBytes, err := base64.StdEncoding.DecodeString(envelope.SignedPayload)
	if err != nil {
		return manifestPayload{}, fmt.Errorf("decode signed runtime payload: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature.Value)
	if err != nil {
		return manifestPayload{}, fmt.Errorf("decode runtime signature: %w", err)
	}
	publicKey := trustedKeys[strings.TrimSpace(envelope.Signature.KeyID)]
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, payloadBytes, signature) {
		return manifestPayload{}, fmt.Errorf("document runtime signature is not trusted")
	}
	var payload manifestPayload
	if !strings.EqualFold(strings.TrimSpace(envelope.Signature.Algorithm), "ed25519") {
		return manifestPayload{}, fmt.Errorf("document runtime signature algorithm is unsupported")
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return manifestPayload{}, fmt.Errorf("decode runtime payload: %w", err)
	}
	if payload.SchemaVersion != "oktapod.document-runtime/v1" || payload.Platform != runtime.GOOS+"-"+runtime.GOARCH {
		return manifestPayload{}, fmt.Errorf("document runtime manifest is incompatible with this platform")
	}
	for relative, expected := range payload.Files {
		path, err := securePackagePath(root, relative)
		if err != nil {
			return manifestPayload{}, err
		}
		actual, err := hashFile(path)
		if err != nil {
			return manifestPayload{}, err
		}
		if actual != expected {
			return manifestPayload{}, fmt.Errorf("document runtime file integrity failed: %s", relative)
		}
	}
	for _, required := range []string{payload.Python, payload.Entrypoint} {
		if payload.Files[filepath.ToSlash(required)] == "" {
			return manifestPayload{}, fmt.Errorf("document runtime manifest does not hash required file: %s", required)
		}
		if _, err := securePackagePath(root, required); err != nil {
			return manifestPayload{}, err
		}
	}
	return payload, nil
}

func (c *Client) Root() string    { return c.workRoot }
func (c *Client) Version() string { return c.manifest.Version }

func (c *Client) HandlePath(handle string) (string, error) {
	raw := strings.TrimSpace(handle)
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("invalid document runtime handle")
	}
	target := filepath.Join(c.workRoot, clean)
	relative, err := filepath.Rel(c.workRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("document runtime handle escapes root")
	}
	return target, nil
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startLocked(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		c.stopLocked()
		return fmt.Errorf("write document runtime request: %w", err)
	}
	responseChannel := make(chan struct {
		response rpcResponse
		err      error
	}, 1)
	decoder := c.decoder
	go func() {
		var response rpcResponse
		err := decoder.Decode(&response)
		responseChannel <- struct {
			response rpcResponse
			err      error
		}{response, err}
	}()
	select {
	case <-ctx.Done():
		c.stopLocked()
		return ctx.Err()
	case decoded := <-responseChannel:
		if decoded.err != nil {
			stderr := ""
			if c.stderr != nil {
				stderr = c.stderr.String()
			}
			c.stopLocked()
			return fmt.Errorf("read document runtime response: %w (%s)", decoded.err, stderr)
		}
		if decoded.response.ID != id || decoded.response.JSONRPC != "2.0" {
			c.stopLocked()
			return fmt.Errorf("document runtime response identity mismatch")
		}
		if decoded.response.Error != nil {
			return decoded.response.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(decoded.response.Result, result); err != nil {
			return fmt.Errorf("decode document runtime result: %w", err)
		}
		return nil
	}
}

func (c *Client) Health(ctx context.Context) application.DocumentRuntimeHealth {
	result := application.DocumentRuntimeHealth{}
	if err := c.Call(ctx, "health", map[string]any{}, &result); err != nil {
		result.Available = false
		result.Version = c.manifest.Version
		result.Protocol = protocolVersion
		result.DegradedReason = err.Error()
	}
	return result
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	return nil
}

func (c *Client) startLocked() error {
	if c.cmd != nil && c.cmd.Process != nil {
		return nil
	}
	python := c.manifest.Python
	if !filepath.IsAbs(python) {
		candidate, err := securePackagePath(c.packageRoot, python)
		if err == nil && fileExists(candidate) {
			python = candidate
		} else if !strings.HasSuffix(c.manifest.Version, "-dev") {
			return fmt.Errorf("document runtime Python executable is missing")
		}
	}
	entrypoint, err := securePackagePath(c.packageRoot, c.manifest.Entrypoint)
	if err != nil {
		return err
	}
	// Isolated mode ignores PYTHONDONTWRITEBYTECODE; -B is required so a signed
	// Runtime package is never mutated by Python bytecode caches at startup.
	cmd := exec.Command(python, "-I", "-B", entrypoint)
	cmd.Dir = c.packageRoot
	tempRoot := filepath.Join(c.workRoot, "tmp")
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return err
	}
	cmd.Env = runtimeEnvironment(c, tempRoot, python)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stderr := &boundedWriter{limit: 16 << 10}
	go func() { _, _ = io.Copy(stderr, stderrPipe) }()
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd, c.stdin, c.decoder, c.stderr = cmd, stdin, json.NewDecoder(bufio.NewReaderSize(stdout, 64<<10)), stderr
	return nil
}

func runtimeEnvironment(c *Client, tempRoot, python string) []string {
	values := []string{
		"OKTAPOD_DOCUMENT_ROOT=" + c.workRoot,
		"PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUTF8=1",
		"HOME=" + c.workRoot, "USERPROFILE=" + c.workRoot, "TEMP=" + tempRoot, "TMP=" + tempRoot,
		"HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "NO_PROXY=*",
	}
	pathParts := []string{filepath.Dir(python)}
	if c.manifest.LibreOffice != "" {
		if path, ok := c.runtimeExecutable(c.manifest.LibreOffice); ok {
			values = append(values, "OKTAPOD_LIBREOFFICE="+path)
			pathParts = append(pathParts, filepath.Dir(path))
		}
	}
	if c.manifest.Tesseract != "" {
		if path, ok := c.runtimeExecutable(c.manifest.Tesseract); ok {
			values = append(values, "OKTAPOD_TESSERACT="+path)
			pathParts = append(pathParts, filepath.Dir(path))
		}
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR"} {
			if value := os.Getenv(key); value != "" {
				values = append(values, key+"="+value)
			}
		}
	}
	values = append(values, "PATH="+strings.Join(pathParts, string(os.PathListSeparator)))
	return values
}

func (c *Client) runtimeExecutable(configured string) (string, bool) {
	configured = strings.TrimSpace(configured)
	if filepath.IsAbs(configured) && strings.HasSuffix(c.manifest.Version, "-dev") {
		info, err := os.Lstat(configured)
		return configured, err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
	}
	path, err := securePackagePath(c.packageRoot, configured)
	return path, err == nil
}

func (c *Client) stopLocked() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = waitWithTimeout(c.cmd, 2*time.Second)
	}
	c.cmd, c.stdin, c.decoder, c.stderr = nil, nil, nil, nil
}

func waitWithTimeout(cmd *exec.Cmd, timeout time.Duration) (bool, error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return true, err
	case <-time.After(timeout):
		return false, nil
	}
}

func securePackagePath(root, relative string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("runtime package root is missing or unsafe")
	}
	raw := strings.TrimSpace(relative)
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime manifest contains an unsafe path")
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime manifest path escapes package root")
	}
	walk := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		walk = filepath.Join(walk, part)
		info, walkErr := os.Lstat(walk)
		if walkErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime package path contains a missing or symbolic-link component: %s", relative)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("runtime package file is missing or unsafe: %s", relative)
	}
	return path, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

type boundedWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (w *boundedWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(body)
	if remaining := w.limit - w.data.Len(); remaining > 0 {
		if len(body) > remaining {
			body = body[:remaining]
		}
		_, _ = w.data.Write(body)
	}
	return original, nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}
