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
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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
	Transport      string            `json:"transport"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	URL            string            `json:"url"`
	AuthEnv        string            `json:"auth_env"`
	Version        string            `json:"version"`
	AuthType       string            `json:"-"`
	AuthHeader     string            `json:"-"`
	Headers        map[string]string `json:"-"`
	SecretBindings []secretBinding   `json:"-"`
}

type secretBinding struct {
	Name     string
	Target   string
	Required bool
}

var ErrNeedsAuth = errors.New("MCP credential is not configured")

func manifestCredential(spec manifest, supplied string) (string, error) {
	credential := strings.TrimSpace(supplied)
	values, isSet, err := decodeCredentialSet(credential)
	if err != nil {
		return "", ErrNeedsAuth
	}
	if isSet {
		for _, binding := range spec.SecretBindings {
			if spec.AuthType == "oauth" && strings.EqualFold(binding.Target, "header:Authorization") {
				continue
			}
			if binding.Required && strings.TrimSpace(values[binding.Name]) == "" {
				return "", ErrNeedsAuth
			}
		}
		return credential, nil
	}
	if len(spec.SecretBindings) > 1 {
		return "", ErrNeedsAuth
	}
	requiresCredential := strings.TrimSpace(spec.AuthEnv) != "" || spec.AuthType == "bearer" || spec.AuthType == "custom_header" || spec.AuthType == "environment"
	if len(spec.SecretBindings) == 1 && spec.SecretBindings[0].Required {
		requiresCredential = spec.AuthType != "oauth" || !strings.EqualFold(spec.SecretBindings[0].Target, "header:Authorization")
	}
	if requiresCredential && credential == "" {
		return "", ErrNeedsAuth
	}
	return credential, nil
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

func Connect(ctx context.Context, manifestJSON, credential string) (Health, error) {
	return ConnectWithOptions(ctx, manifestJSON, credential, SessionOptions{})
}

// ConnectWithOptions enables the interactive OAuth boundary while keeping the
// ordinary health check API backward compatible.
func ConnectWithOptions(ctx context.Context, manifestJSON, credential string, options SessionOptions) (Health, error) {
	spec, err := decodeManifest(manifestJSON)
	if err != nil {
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
	credential, err = manifestCredential(spec, credential)
	if err != nil {
		return Health{Transport: spec.Transport, LastError: err.Error()}, err
	}
	session, closeSession, err := openSDKSession(ctx, spec, credential, options)
	if err != nil {
		return Health{Transport: spec.Transport, LastError: safeMCPError(err)}, err
	}
	defer closeSession()
	tools, err := listSDKTools(ctx, session)
	if err != nil {
		return Health{Transport: spec.Transport, LastError: safeMCPError(err)}, err
	}
	protocol := ""
	if initialized := session.InitializeResult(); initialized != nil {
		protocol = initialized.ProtocolVersion
	}
	return Health{Transport: canonicalTransport(spec.Transport), Protocol: protocol, Tools: tools, ConnectedAt: time.Now().UTC()}, nil
}

// Call performs one explicitly requested MCP tools/call invocation using a
// short-lived transport. Production Runtime calls that need protocol session
// continuity use SessionBroker instead; both paths rely on the caller to
// enforce approval, capability scope and receipt semantics before dispatch.
func Call(ctx context.Context, manifestJSON, credential, toolName string, arguments map[string]any) (CallResult, error) {
	return CallWithCredentialUpdater(ctx, manifestJSON, credential, toolName, arguments, nil)
}

// PreflightCall validates every local invocation prerequisite without opening
// a process or network connection. Runtimes use it before durable receipt
// Begin so malformed manifests, oversized arguments, and missing credentials
// cannot create a false "started" side effect.
func PreflightCall(manifestJSON, credential, toolName string, arguments map[string]any) error {
	if strings.TrimSpace(toolName) == "" || len(toolName) > 200 {
		return errors.New("MCP tool name is invalid")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 1<<20 {
		return errors.New("MCP tool arguments exceed the 1 MiB safety limit")
	}
	spec, err := decodeManifest(manifestJSON)
	if err != nil {
		return fmt.Errorf("decode MCP manifest: %w", err)
	}
	_, err = manifestCredential(spec, credential)
	return err
}

// CallWithCredentialUpdater lets a restored OAuth refresh token be rotated in
// SecretStore without exposing the new token to the Worker or Run records.
func CallWithCredentialUpdater(ctx context.Context, manifestJSON, credential, toolName string, arguments map[string]any, updater CredentialUpdater) (CallResult, error) {
	if strings.TrimSpace(toolName) == "" || len(toolName) > 200 {
		return CallResult{}, errors.New("MCP tool name is invalid")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 1<<20 {
		return CallResult{}, errors.New("MCP tool arguments exceed the 1 MiB safety limit")
	}
	spec, err := decodeManifest(manifestJSON)
	if err != nil {
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
	credential, err = manifestCredential(spec, credential)
	if err != nil {
		return CallResult{}, err
	}
	session, closeSession, err := openSDKSession(ctx, spec, credential, SessionOptions{CredentialUpdater: updater})
	if err != nil {
		return CallResult{}, err
	}
	defer closeSession()
	return callSDKTool(ctx, session, toolName, arguments)
}

func callSDKTool(ctx context.Context, session *sdkmcp.ClientSession, toolName string, arguments map[string]any) (CallResult, error) {
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: toolName, Arguments: arguments})
	if err != nil {
		return CallResult{}, err
	}
	if result.NeedsInput() {
		return CallResult{}, errors.New("MCP tool requires structured user input that this Run has not supplied")
	}
	content := make([]json.RawMessage, 0, len(result.Content))
	total := 0
	for _, item := range result.Content {
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return CallResult{}, marshalErr
		}
		total += len(encoded)
		if total > 1<<20 {
			return CallResult{}, errors.New("MCP tool response exceeds the 1 MiB safety limit")
		}
		content = append(content, encoded)
	}
	var structured json.RawMessage
	if result.StructuredContent != nil {
		structured, err = json.Marshal(result.StructuredContent)
		if err != nil {
			return CallResult{}, err
		}
		if len(structured) > 1<<20 {
			return CallResult{}, errors.New("MCP structured response exceeds the 1 MiB safety limit")
		}
	}
	return CallResult{Content: content, IsError: result.IsError, StructuredResult: structured}, nil
}

func openSDKSession(ctx context.Context, spec manifest, credential string, options SessionOptions) (*sdkmcp.ClientSession, func(), error) {
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "oktapod", Title: "Oktapod", Version: "1.0.0"}, &sdkmcp.ClientOptions{Capabilities: &sdkmcp.ClientCapabilities{}})
	var transport sdkmcp.Transport
	switch spec.Transport {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" || strings.ContainsAny(spec.Command, "&|;<>\r\n") {
			return nil, func() {}, errors.New("stdio command must be a direct executable path")
		}
		command, err := newMCPCommand(ctx, spec.Command, spec.Args)
		if err != nil {
			return nil, func() {}, err
		}
		configureMCPCommand(command)
		command.Cancel = func() error { return terminateMCPProcessTree(command) }
		command.WaitDelay = 2 * time.Second
		command.Env = mcpCommandEnvironment(spec, credential)
		command.Stderr = io.Discard
		transport = &sdkmcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}
	case "streamable_http", "http":
		target, err := url.Parse(strings.TrimSpace(spec.URL))
		if err != nil || target.Scheme == "" || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return nil, func() {}, errors.New("MCP URL must be an absolute http(s) URL")
		}
		if err := validateHost(ctx, target.Hostname()); err != nil {
			return nil, func() {}, fmt.Errorf("MCP URL is not allowed by SSRF policy: %w", err)
		}
		httpClient := safeHTTPClient(target)
		if len(spec.Headers) > 0 || (credential != "" && spec.AuthType != "oauth") {
			httpClient.Transport = headerRoundTripper{base: httpClient.Transport, credential: credential, spec: spec}
		}
		streamable := &sdkmcp.StreamableClientTransport{Endpoint: target.String(), HTTPClient: httpClient, MaxRetries: 1, DisableStandaloneSSE: true}
		if spec.AuthType == "oauth" {
			handler, oauthErr := newOAuthHandler(httpClient, options.RedirectURL, credential, options)
			if oauthErr != nil {
				return nil, func() {}, oauthErr
			}
			streamable.OAuthHandler = handler
		}
		transport = streamable
	default:
		return nil, func() {}, fmt.Errorf("unsupported MCP transport %q", spec.Transport)
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, func() {}, err
	}
	return session, func() { _ = session.Close() }, nil
}

type headerRoundTripper struct {
	base       http.RoundTripper
	credential string
	spec       manifest
}

func (roundTripper headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, value := range roundTripper.spec.Headers {
		clone.Header.Set(key, value)
	}
	values, isSet, _ := decodeCredentialSet(roundTripper.credential)
	if isSet {
		for _, binding := range roundTripper.spec.SecretBindings {
			if name, ok := strings.CutPrefix(binding.Target, "header:"); ok && values[binding.Name] != "" {
				clone.Header.Set(name, values[binding.Name])
			}
		}
	} else if roundTripper.credential != "" {
		if roundTripper.spec.AuthType == "custom_header" {
			clone.Header.Set(roundTripper.spec.AuthHeader, roundTripper.credential)
		} else {
			clone.Header.Set("Authorization", "Bearer "+roundTripper.credential)
		}
	}
	return roundTripper.base.RoundTrip(clone)
}

func decodeManifest(raw string) (manifest, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return manifest{}, err
	}
	spec := manifest{}
	switch transport := document["transport"].(type) {
	case string:
		spec.Transport = transport
		spec.Command, _ = document["command"].(string)
		spec.URL, _ = document["url"].(string)
		spec.AuthEnv, _ = document["auth_env"].(string)
		spec.Args = manifestStringSlice(document["args"])
	case map[string]any:
		spec.Transport, _ = transport["type"].(string)
		spec.Command, _ = transport["command"].(string)
		spec.URL, _ = transport["url"].(string)
		spec.AuthEnv, _ = transport["auth_env"].(string)
		spec.Args = manifestStringSlice(transport["args"])
	default:
		return manifest{}, errors.New("MCP transport is missing")
	}
	if auth, ok := document["auth"].(map[string]any); ok {
		spec.AuthType, _ = auth["type"].(string)
		spec.AuthHeader, _ = auth["header"].(string)
	}
	if headers, ok := document["headers"].(map[string]any); ok {
		spec.Headers = make(map[string]string, len(headers))
		for key, value := range headers {
			text, ok := value.(string)
			if !ok {
				return manifest{}, fmt.Errorf("MCP header %q must be a string", key)
			}
			spec.Headers[key] = text
		}
	}
	if bindings, ok := document["secret_bindings"].([]any); ok {
		for _, rawBinding := range bindings {
			binding, ok := rawBinding.(map[string]any)
			if !ok {
				continue
			}
			name, _ := binding["name"].(string)
			target, _ := binding["target"].(string)
			required, _ := binding["required"].(bool)
			spec.SecretBindings = append(spec.SecretBindings, secretBinding{Name: name, Target: target, Required: required})
		}
	}
	return spec, nil
}

func manifestStringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func listSDKTools(ctx context.Context, session *sdkmcp.ClientSession) ([]Tool, error) {
	result := make([]Tool, 0, 32)
	cursor := ""
	for page := 0; page < 20; page++ {
		listed, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range listed.Tools {
			if item == nil || strings.TrimSpace(item.Name) == "" {
				continue
			}
			if len(result) >= 1000 {
				return nil, errors.New("MCP server exposes more than 1000 tools")
			}
			schema := map[string]any{}
			if item.InputSchema != nil {
				encoded, marshalErr := json.Marshal(item.InputSchema)
				if marshalErr != nil || len(encoded) > 1<<20 || json.Unmarshal(encoded, &schema) != nil {
					return nil, fmt.Errorf("MCP tool %q has an invalid or oversized input schema", item.Name)
				}
				if !validToolInputSchema(schema) {
					return nil, fmt.Errorf("MCP tool %q input schema must describe an object", item.Name)
				}
			}
			annotations := ToolAnnotations{}
			if item.Annotations != nil {
				readOnly, idempotent := item.Annotations.ReadOnlyHint, item.Annotations.IdempotentHint
				annotations = ToolAnnotations{Title: item.Annotations.Title, ReadOnlyHint: &readOnly, DestructiveHint: item.Annotations.DestructiveHint, IdempotentHint: &idempotent, OpenWorldHint: item.Annotations.OpenWorldHint}
			}
			result = append(result, Tool{Name: item.Name, Description: item.Description, InputSchema: schema, Annotations: annotations})
		}
		cursor = listed.NextCursor
		if cursor == "" {
			return result, nil
		}
	}
	return nil, errors.New("MCP tools/list exceeded pagination limit")
}

func validToolInputSchema(schema map[string]any) bool {
	typeValue, exists := schema["type"]
	if !exists {
		return true
	}
	switch value := typeValue.(type) {
	case string:
		return value == "object"
	case []any:
		for _, item := range value {
			if item == "object" {
				return true
			}
		}
	}
	return false
}

func canonicalTransport(value string) string {
	if value == "http" {
		return "streamable_http"
	}
	return value
}

func safeMCPError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "MCP connection timed out"
	}
	if errors.Is(err, ErrNeedsAuth) {
		return ErrNeedsAuth.Error()
	}
	return "MCP connection failed"
}

func callStdio(ctx context.Context, spec manifest, credential string, request rpcRequest) (CallResult, error) {
	if strings.TrimSpace(spec.Command) == "" || strings.ContainsAny(spec.Command, "&|;<>\r\n") {
		return CallResult{}, errors.New("stdio command must be a direct executable path")
	}
	command, err := newMCPCommand(ctx, spec.Command, spec.Args)
	if err != nil {
		return CallResult{}, err
	}
	configureMCPCommand(command)
	command.Cancel = func() error { return terminateMCPProcessTree(command) }
	command.WaitDelay = 2 * time.Second
	command.Env = mcpCommandEnvironment(spec, credential)
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

func callHTTP(ctx context.Context, spec manifest, credential string, request rpcRequest) (CallResult, error) {
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
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
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

func connectStdio(ctx context.Context, spec manifest, credential string) (Health, error) {
	health := Health{Transport: "stdio"}
	if strings.TrimSpace(spec.Command) == "" || strings.ContainsAny(spec.Command, "&|;<>\r\n") {
		health.LastError = "stdio command must be a direct executable path"
		return health, errors.New(health.LastError)
	}
	command, err := newMCPCommand(ctx, spec.Command, spec.Args)
	if err != nil {
		health.LastError = "MCP stdio launcher is unavailable"
		return health, err
	}
	configureMCPCommand(command)
	command.Cancel = func() error { return terminateMCPProcessTree(command) }
	command.WaitDelay = 2 * time.Second
	command.Env = mcpCommandEnvironment(spec, credential)
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

func connectHTTP(ctx context.Context, spec manifest, credential string) (Health, error) {
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
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
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
