// Package mcp implements the deliberately small MCP transport adapter used by
// the MVP. It discovers health/tool metadata and performs one-shot tools/call
// invocations; policy, approval, receipts and Run ownership remain in the
// Harness and are never bypassed by connecting.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Health struct {
	Transport   string
	Protocol    string
	Tools       []Tool
	LastError   string
	ConnectedAt time.Time
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema map[string]any  `json:"inputSchema,omitempty"`
	Annotations ToolAnnotations `json:"annotations,omitempty"`
}

type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

type CallResult struct {
	Content          []json.RawMessage
	IsError          bool
	StructuredResult json.RawMessage
}

type manifest struct {
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	URL       string   `json:"url"`
	AuthEnv   string   `json:"auth_env"`
	Version   string   `json:"version"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		ProtocolVersion  string            `json:"protocolVersion"`
		Tools            []Tool            `json:"tools"`
		Content          []json.RawMessage `json:"content"`
		IsError          bool              `json:"isError"`
		StructuredResult json.RawMessage   `json:"structuredContent"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func Connect(ctx context.Context, manifestJSON string) (Health, error) {
	var spec manifest
	if err := json.Unmarshal([]byte(manifestJSON), &spec); err != nil {
		return Health{LastError: "invalid MCP manifest"}, fmt.Errorf("decode MCP manifest: %w", err)
	}
	spec.Transport = strings.ToLower(strings.TrimSpace(spec.Transport))
	if spec.Transport == "" {
		if spec.URL != "" {
			spec.Transport = "streamable_http"
		} else {
			spec.Transport = "stdio"
		}
	}
	switch spec.Transport {
	case "stdio":
		return connectStdio(ctx, spec)
	case "streamable_http", "http":
		return connectHTTP(ctx, spec)
	default:
		return Health{Transport: spec.Transport, LastError: "unsupported MCP transport"}, fmt.Errorf("unsupported MCP transport %q", spec.Transport)
	}
}

// Call performs one explicitly requested MCP tools/call invocation. A fresh
// short-lived transport is used for every invocation so a crashed or
// compromised server process cannot retain a long-lived broker handle. The
// caller must enforce approval, capability scope and receipt semantics before
// invoking this adapter.
func Call(ctx context.Context, manifestJSON, toolName string, arguments map[string]any) (CallResult, error) {
	if strings.TrimSpace(toolName) == "" || len(toolName) > 200 {
		return CallResult{}, errors.New("MCP tool name is invalid")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 1<<20 {
		return CallResult{}, errors.New("MCP tool arguments exceed the 1 MiB safety limit")
	}
	var spec manifest
	if err := json.Unmarshal([]byte(manifestJSON), &spec); err != nil {
		return CallResult{}, fmt.Errorf("decode MCP manifest: %w", err)
	}
	spec.Transport = strings.ToLower(strings.TrimSpace(spec.Transport))
	if spec.Transport == "" {
		if spec.URL != "" {
			spec.Transport = "streamable_http"
		} else {
			spec.Transport = "stdio"
		}
	}
	request := rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{"name": toolName, "arguments": arguments}}
	switch spec.Transport {
	case "stdio":
		return callStdio(ctx, spec, request)
	case "streamable_http", "http":
		return callHTTP(ctx, spec, request)
	default:
		return CallResult{}, fmt.Errorf("unsupported MCP transport %q", spec.Transport)
	}
}

func callStdio(ctx context.Context, spec manifest, request rpcRequest) (CallResult, error) {
	if strings.TrimSpace(spec.Command) == "" || strings.ContainsAny(spec.Command, "&|;<>\r\n") {
		return CallResult{}, errors.New("stdio command must be a direct executable path")
	}
	command := exec.CommandContext(ctx, spec.Command, spec.Args...)
	configureMCPCommand(command)
	command.Cancel = func() error { return terminateMCPProcessTree(command) }
	command.WaitDelay = 2 * time.Second
	command.Env = mcpCommandEnvironment(spec)
	stdin, err := command.StdinPipe()
	if err != nil {
		return CallResult{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return CallResult{}, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return CallResult{}, fmt.Errorf("start MCP stdio server: %w", err)
	}
	defer func() { _ = stdin.Close(); _ = terminateMCPProcessTree(command); _ = command.Wait() }()
	reader := bufio.NewReader(io.LimitReader(stdout, 8<<20))
	if err := writeRPC(stdin, rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "oktapod", "version": "0.1"},
	}}); err != nil {
		return CallResult{}, err
	}
	response, err := readRPC(reader)
	if err != nil {
		return CallResult{}, err
	}
	if response.Error != nil {
		return CallResult{}, errors.New(response.Error.Message)
	}
	if err := writeRPC(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return CallResult{}, err
	}
	if err := writeRPC(stdin, request); err != nil {
		return CallResult{}, err
	}
	response, err = readRPC(reader)
	if err != nil {
		return CallResult{}, err
	}
	return callResult(response)
}

func callHTTP(ctx context.Context, spec manifest, request rpcRequest) (CallResult, error) {
	target, err := url.Parse(strings.TrimSpace(spec.URL))
	if err != nil || target.Scheme == "" || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return CallResult{}, errors.New("MCP URL must be an absolute http(s) URL")
	}
	if err := validateHost(ctx, target.Hostname()); err != nil {
		return CallResult{}, err
	}
	client := safeHTTPClient(target)
	call := func(rpc rpcRequest) (rpcResponse, error) {
		body, err := json.Marshal(rpc)
		if err != nil {
			return rpcResponse{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			return rpcResponse{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Origin", "http://127.0.0.1")
		if env := strings.TrimSpace(spec.AuthEnv); env != "" {
			if value := strings.TrimSpace(os.Getenv(env)); value != "" {
				req.Header.Set("Authorization", "Bearer "+value)
			}
		}
		response, err := client.Do(req)
		if err != nil {
			return rpcResponse{}, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return rpcResponse{}, fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)
		}
		return readHTTPRPC(response.Body, response.Header.Get("Content-Type"))
	}
	response, err := call(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "oktapod", "version": "0.1"},
	}})
	if err != nil {
		return CallResult{}, err
	}
	if response.Error != nil {
		return CallResult{}, errors.New(response.Error.Message)
	}
	response, err = call(request)
	if err != nil {
		return CallResult{}, err
	}
	return callResult(response)
}

func callResult(response rpcResponse) (CallResult, error) {
	if response.Error != nil {
		return CallResult{}, errors.New(response.Error.Message)
	}
	total := 0
	for _, item := range response.Result.Content {
		total += len(item)
		if total > 1<<20 {
			return CallResult{}, errors.New("MCP tool response exceeds the 1 MiB safety limit")
		}
	}
	if len(response.Result.StructuredResult) > 1<<20 {
		return CallResult{}, errors.New("MCP structured response exceeds the 1 MiB safety limit")
	}
	return CallResult{Content: response.Result.Content, IsError: response.Result.IsError, StructuredResult: response.Result.StructuredResult}, nil
}

func connectStdio(ctx context.Context, spec manifest) (Health, error) {
	health := Health{Transport: "stdio"}
	if strings.TrimSpace(spec.Command) == "" || strings.ContainsAny(spec.Command, "&|;<>\r\n") {
		health.LastError = "stdio command must be a direct executable path"
		return health, errors.New(health.LastError)
	}
	command := exec.CommandContext(ctx, spec.Command, spec.Args...)
	configureMCPCommand(command)
	command.Cancel = func() error { return terminateMCPProcessTree(command) }
	command.WaitDelay = 2 * time.Second
	command.Env = mcpCommandEnvironment(spec)
	stdin, err := command.StdinPipe()
	if err != nil {
		return health, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return health, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		health.LastError = "MCP stdio process failed to start"
		return health, fmt.Errorf("start MCP stdio server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = terminateMCPProcessTree(command)
		_ = command.Wait()
	}()
	reader := bufio.NewReader(io.LimitReader(stdout, 8<<20))
	if err := writeRPC(stdin, rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "oktapod", "version": "0.1"},
	}}); err != nil {
		return health, err
	}
	response, err := readRPC(reader)
	if err != nil {
		health.LastError = "MCP initialize response unavailable"
		return health, err
	}
	if response.Error != nil {
		health.LastError = response.Error.Message
		return health, errors.New(health.LastError)
	}
	health.Protocol = response.Result.ProtocolVersion
	if err := writeRPC(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return health, err
	}
	if err := writeRPC(stdin, rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list", Params: map[string]any{}}); err != nil {
		return health, err
	}
	response, err = readRPC(reader)
	if err != nil {
		health.LastError = "MCP tools/list response unavailable"
		return health, err
	}
	if response.Error != nil {
		health.LastError = response.Error.Message
		return health, errors.New(health.LastError)
	}
	health.Tools = response.Result.Tools
	health.ConnectedAt = time.Now().UTC()
	return health, nil
}

func connectHTTP(ctx context.Context, spec manifest) (Health, error) {
	health := Health{Transport: "streamable_http"}
	target, err := url.Parse(strings.TrimSpace(spec.URL))
	if err != nil || target.Scheme == "" || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		health.LastError = "MCP URL must be an absolute http(s) URL"
		return health, errors.New(health.LastError)
	}
	if err := validateHost(ctx, target.Hostname()); err != nil {
		health.LastError = "MCP URL is not allowed by SSRF policy"
		return health, err
	}
	client := safeHTTPClient(target)
	call := func(request rpcRequest) (rpcResponse, error) {
		body, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return rpcResponse{}, marshalErr
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
		if requestErr != nil {
			return rpcResponse{}, requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Origin", "http://127.0.0.1")
		if strings.TrimSpace(spec.AuthEnv) != "" {
			if value := strings.TrimSpace(os.Getenv(spec.AuthEnv)); value != "" {
				req.Header.Set("Authorization", "Bearer "+value)
			}
		}
		response, doErr := client.Do(req)
		if doErr != nil {
			return rpcResponse{}, doErr
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return rpcResponse{}, fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)
		}
		return readHTTPRPC(response.Body, response.Header.Get("Content-Type"))
	}
	response, err := call(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "oktapod", "version": "0.1"},
	}})
	if err != nil {
		health.LastError = "MCP initialize failed"
		return health, err
	}
	if response.Error != nil {
		health.LastError = response.Error.Message
		return health, errors.New(health.LastError)
	}
	health.Protocol = response.Result.ProtocolVersion
	response, err = call(rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list", Params: map[string]any{}})
	if err != nil {
		health.LastError = "MCP tools/list failed"
		return health, err
	}
	if response.Error != nil {
		health.LastError = response.Error.Message
		return health, errors.New(health.LastError)
	}
	health.Tools = response.Result.Tools
	health.ConnectedAt = time.Now().UTC()
	return health, nil
}

func writeRPC(writer io.Writer, request rpcRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s\n", data)
	return err
}

func readRPC(reader *bufio.Reader) (rpcResponse, error) {
	for i := 0; i < 32; i++ {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return rpcResponse{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		var response rpcResponse
		if json.Unmarshal(line, &response) == nil && response.JSONRPC != "" {
			return response, nil
		}
	}
	return rpcResponse{}, errors.New("MCP response exceeded framing limit")
}

func readHTTPRPC(reader io.Reader, contentType string) (rpcResponse, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 8<<20))
	if err != nil {
		return rpcResponse{}, err
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "data:") {
				data = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				break
			}
		}
	}
	var response rpcResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return rpcResponse{}, err
	}
	return response, nil
}

func validateHost(ctx context.Context, host string) error {
	if host == "" {
		return errors.New("empty MCP host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		if isPrivateIP(ip) {
			return errors.New("private MCP address blocked")
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if isPrivateIP(ip) || ip.IsLoopback() {
			return errors.New("MCP hostname resolves to a private address")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}

// safeHTTPClient prevents the transport layer from undoing the SSRF decision
// made during manifest validation. The default http.Transport may use a
// process-wide proxy and may resolve a hostname again after validation; both
// behaviours are unsafe for a user-controlled MCP endpoint. Every connection
// therefore resolves the host again, rejects private/link-local addresses,
// dials the validated IP directly, and never follows redirects.
func safeHTTPClient(target *url.URL) *http.Client {
	defaultPort := "80"
	if strings.EqualFold(target.Scheme, "https") {
		defaultPort = "443"
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DialContext:           safeDialContext(defaultPort),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("MCP redirects are denied by SSRF policy")
		},
	}
}

func safeDialContext(defaultPort string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			host = address
			port = defaultPort
		}
		if host == "" {
			return nil, errors.New("MCP destination host is empty")
		}
		ips, err := resolveMCPHost(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, ip := range ips {
			if !allowedMCPIP(ip) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, fmt.Errorf("MCP destination has no reachable allowed address: %w", lastErr)
		}
		return nil, errors.New("MCP destination resolves only to a blocked address")
	}
}

func resolveMCPHost(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if strings.EqualFold(host, "localhost") {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP host: %w", err)
	}
	return ips, nil
}

func allowedMCPIP(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || !isPrivateIP(ip))
}
