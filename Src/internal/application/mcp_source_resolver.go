package application

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type MCPRegistry interface {
	Search(ctx context.Context, query string, limit int) ([]MCPRegistryServer, error)
}

// MCPPackageResolver resolves an unpinned package request to one concrete
// version before an install manifest is created. Implementations are
// ecosystem-specific adapters; the application layer only depends on this
// small contract.
type MCPPackageResolver interface {
	Resolve(ctx context.Context, ecosystem, packageName string) (string, error)
}

type MCPRegistryServer struct {
	Name        string                `json:"name"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Version     string                `json:"version"`
	Repository  MCPRegistryRepository `json:"repository"`
	Packages    []MCPRegistryPackage  `json:"packages"`
	Remotes     []MCPRegistryRemote   `json:"remotes"`
}

type MCPRegistryRepository struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type MCPRegistryPackage struct {
	RegistryType         string                           `json:"registryType"`
	Identifier           string                           `json:"identifier"`
	Version              string                           `json:"version"`
	Transport            MCPRegistryTransport             `json:"transport"`
	EnvironmentVariables []MCPRegistryEnvironmentVariable `json:"environmentVariables"`
}

type MCPRegistryTransport struct {
	Type string `json:"type"`
}

type MCPRegistryEnvironmentVariable struct {
	Name        string `json:"name"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
	Description string `json:"description"`
}

type MCPRegistryRemote struct {
	Type          string                    `json:"type"`
	TransportType string                    `json:"transportType"`
	URL           string                    `json:"url"`
	Headers       []MCPRegistryRemoteHeader `json:"headers"`
}

type MCPRegistryRemoteHeader struct {
	Name        string `json:"name"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
	Description string `json:"description"`
}

var resourceDraftSecretPattern = regexp.MustCompile(`(?i)(?:\b(?:tvly-|sk-|ghp_|github_pat_|ntn_)[a-z0-9_-]{12,}|(?:api[_-]?key|authorization|bearer|password|secret)\s*[:=]\s*\S{8,})`)

func (s *Service) WithMCPRegistry(registry MCPRegistry) *Service {
	s.mcpRegistry = registry
	return s
}

func (s *Service) WithMCPPackageResolver(resolver MCPPackageResolver) *Service {
	s.mcpPackageResolver = resolver
	return s
}

func (s *Service) ResolveMCPInstallDraft(ctx context.Context, projectID, sourceInput string) (core.ResourceInstallDraft, error) {
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), strings.TrimSpace(projectID)); err != nil {
		return core.ResourceInstallDraft{}, err
	}
	sourceInput = strings.TrimSpace(sourceInput)
	if sourceInput == "" {
		return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: "is required"}
	}
	if len([]rune(sourceInput)) > 8192 {
		return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: "must not exceed 8192 characters"}
	}
	if resourceDraftSecretPattern.MatchString(sourceInput) {
		return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: "凭据不能写在来源描述中，请在解析后使用安全凭据输入框"}
	}
	now := time.Now().UTC()
	draft := core.ResourceInstallDraft{
		ID: uuid.NewString(), Kind: core.CapabilityKindMCP, SourceInput: sourceInput,
		Status: "draft", Warnings: []string{}, RequiredSecrets: []core.MCPSecretBinding{},
		CreatedAt: now, UpdatedAt: now,
	}
	resolvedInput := trimMCPInstallLanguage(sourceInput)
	if strings.HasPrefix(strings.TrimSpace(resolvedInput), "{") {
		definition, name, version, err := resolveMCPJSON(resolvedInput)
		if err != nil {
			return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: err.Error()}
		}
		return finalizeMCPDraft(draft, name, version, definition, nil), nil
	}
	if strings.HasPrefix(strings.ToLower(resolvedInput), "http://") || strings.HasPrefix(strings.ToLower(resolvedInput), "https://") {
		definition := core.MCPDefinitionV1{
			SchemaVersion: core.MCPDefinitionSchemaV1,
			Source:        core.MCPSource{Type: "url", Reference: resolvedInput},
			Transport:     core.MCPTransport{Type: "streamable_http", URL: resolvedInput},
			// Standard remote MCP authorization is discovered from a 401 by
			// the official SDK. Public endpoints still connect without opening
			// a browser because the handler is only invoked when challenged.
			Auth: core.MCPAuth{Type: "oauth"}, Permissions: []string{"network"},
		}
		encoded, err := definition.JSON()
		if err != nil {
			return core.ResourceInstallDraft{}, err
		}
		if _, err := core.ParseMCPDefinition(encoded); err != nil {
			return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: err.Error()}
		}
		name := remoteMCPName(resolvedInput)
		return finalizeMCPDraft(draft, name, "remote", definition, []string{"远端 URL 未经过 Registry 身份验证；安装前请核对域名和数据权限。若服务使用 API Key，请在高级配置中将认证方式改为 bearer 或 custom_header。"}), nil
	}
	if definition, name, version, matched, err := resolveDirectMCPSource(ctx, resolvedInput, s.mcpPackageResolver); matched {
		if err != nil {
			return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: err.Error()}
		}
		return finalizeMCPDraft(draft, name, version, definition, []string{"本地 MCP 会启动固定命令；安装前请核对包来源和进程权限。"}), nil
	}
	if s.mcpRegistry == nil {
		return core.ResourceInstallDraft{}, &core.ResourceConflictError{Resource: "mcp_registry", Reason: "Registry 暂不可用；请改用 URL、固定版本包或本地命令"}
	}
	servers, err := s.searchMCPRegistry(ctx, resolvedInput)
	if err != nil {
		return core.ResourceInstallDraft{}, &core.ResourceConflictError{Resource: "mcp_registry", Reason: "Registry 查询失败；请稍后重试或改用明确来源"}
	}
	for _, server := range servers {
		candidate, candidateErr := registryInstallCandidate(server)
		if candidateErr == nil {
			draft.Candidates = append(draft.Candidates, candidate)
		}
	}
	if len(draft.Candidates) == 0 {
		return core.ResourceInstallDraft{}, &core.ValidationError{Field: "source_input", Message: "Registry 中没有找到可安装的 Streamable HTTP、npm 或 PyPI MCP；请提供 URL、server.json 或本地命令"}
	}
	for _, candidate := range draft.Candidates {
		if strings.EqualFold(candidate.Name, resolvedInput) {
			return finalizeMCPDraft(draft, candidate.Name, candidate.Version, candidate.Definition, []string{"Registry 只提供发现元数据，不代表代码已由 Oktapod 审核；安装前仍需核对来源和权限。"}), nil
		}
	}
	if len(draft.Candidates) == 1 {
		candidate := draft.Candidates[0]
		return finalizeMCPDraft(draft, candidate.Name, candidate.Version, candidate.Definition, []string{"Registry 只提供发现元数据，不代表代码已由 Oktapod 审核；安装前仍需核对来源和权限。"}), nil
	}
	draft.Warnings = append(draft.Warnings, fmt.Sprintf("找到 %d 个同名或相近 MCP；请选择明确来源后再安装。", len(draft.Candidates)))
	return draft, nil
}

// The public registry treats the query as one token sequence. Natural
// language therefore first searches the full intent, then falls back to the
// explicit product or package identifier when the intent is too narrow.
func (s *Service) searchMCPRegistry(ctx context.Context, query string) ([]MCPRegistryServer, error) {
	servers, err := s.mcpRegistry.Search(ctx, query, 8)
	if err != nil || len(servers) > 0 {
		return servers, err
	}
	identifier := explicitRegistryQuery(query)
	if identifier == "" || identifier == query {
		return servers, nil
	}
	return s.mcpRegistry.Search(ctx, identifier, 20)
}

func explicitRegistryQuery(query string) string {
	fields := strings.Fields(query)
	identifiers := make([]string, 0, len(fields))
	for _, field := range fields {
		value := strings.Trim(field, "`\"'[](){}:;,.!?，。；：？！（）【】《》“”‘’")
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, "/@._-") || isLatinIdentifier(value) {
			identifiers = append(identifiers, value)
		}
	}
	if len(identifiers) == 0 {
		return ""
	}
	return strings.Join(identifiers, " ")
}

func isLatinIdentifier(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func parseMCPInstallDirective(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"给项目", "给这个项目", "给本项目"} {
		if strings.HasPrefix(lower, prefix) {
			lower = strings.TrimPrefix(lower, prefix)
			trimmed = strings.TrimSpace(trimmed[len(prefix):])
			break
		}
	}
	matchedPrefix := false
	for _, prefix := range []string{"请帮我安装", "帮我安装", "请安装", "安装", "请添加", "添加", "请配置", "配置", "请接入", "接入", "install ", "add ", "configure "} {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			matchedPrefix = true
			break
		}
	}
	source := trimMCPInstallLanguage(trimmed)
	if !matchedPrefix {
		return "", false
	}
	if strings.TrimSpace(source) == "" {
		return "", false
	}
	return source, true
}

func (s *Service) executeMCPInstallDirective(
	ctx context.Context,
	projectID string,
	input core.ProjectTurnInput,
	content, sourceInput, conversationID, visibility, idempotencyKey string,
	onRoute func(string) error,
	onDelta func(string) error,
) (core.ProjectTurnResult, error) {
	if onRoute != nil {
		if err := onRoute(TurnRouteResource); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	draft, err := s.ResolveMCPInstallDraft(ctx, projectID, sourceInput)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	assistant := "MCP 来源已解析，请核对来源、版本、权限和安装范围。"
	if len(draft.Candidates) > 1 {
		assistant = fmt.Sprintf("找到 %d 个相近的 MCP 来源；请选择明确来源后再安装。", len(draft.Candidates))
	} else if draft.Status == "needs_secret" {
		assistant = "MCP 来源已解析；安装前请在安全输入框补充所需凭据。"
	}
	if onDelta != nil {
		if err := onDelta(assistant); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	turnInput := inputWithTurnDefaults(input, content, conversationID, visibility)
	result, err := s.repository.AppendChatTurn(ctx, s.ScopeFor(ctx), projectID, turnInput, assistant, nil, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
		ProjectID string
		Input     core.ProjectTurnInput
	}{projectID, turnInput})})
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	result.Route = TurnRouteResource
	result.ResourceInstallDraft = &draft
	return result, nil
}

func trimMCPInstallLanguage(input string) string {
	value := strings.TrimSpace(input)
	lower := strings.ToLower(value)
	for _, prefix := range []string{"请帮我安装", "帮我安装", "请安装", "安装", "请添加", "添加", "请配置", "配置", "请接入", "接入", "install", "add", "configure"} {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	if isStructuredMCPSource(value) {
		return value
	}
	for {
		trimmedValue := false
		for _, suffix := range []string{"mcp server", "mcp工具", "mcp 服务", "mcp服务", "mcp", "工具", "tools", "tool", "插件", "plugin"} {
			if hasNaturalLanguageSuffix(value, suffix) {
				value = strings.TrimSpace(value[:len(value)-len(suffix)])
				trimmedValue = true
				break
			}
		}
		if strings.Contains(value, "的") {
			value = strings.Join(strings.Fields(strings.ReplaceAll(value, "的", " ")), " ")
			trimmedValue = true
		}
		if !trimmedValue {
			break
		}
	}
	return value
}

func isStructuredMCPSource(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(value, "{") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "npm:") ||
		strings.HasPrefix(lower, "pypi:") {
		return true
	}
	if strings.HasPrefix(value, "@") && !strings.ContainsAny(value, " \t\r\n") {
		return true
	}
	words, err := splitMCPCommandLine(value)
	if err != nil || len(words) == 0 {
		return false
	}
	switch strings.ToLower(words[0]) {
	case "npx", "uvx", "node", "python", "python3":
		return true
	default:
		return false
	}
}

func hasNaturalLanguageSuffix(value, suffix string) bool {
	value = strings.TrimSpace(value)
	suffix = strings.TrimSpace(suffix)
	if value == "" || suffix == "" || len(value) <= len(suffix) || !strings.HasSuffix(strings.ToLower(value), strings.ToLower(suffix)) {
		return false
	}
	boundary := value[len(value)-len(suffix)-1]
	if boundary == ' ' || boundary == '\t' || boundary == '\r' || boundary == '\n' {
		return true
	}
	// Chinese natural-language phrases often attach particles directly to the
	// source name (for example, "Playwright的插件").
	return boundary >= 0x80
}

func resolveMCPJSON(raw string) (core.MCPDefinitionV1, string, string, error) {
	if definition, err := core.ParseMCPDefinition(raw); err == nil {
		name := definition.Source.Reference
		if name == "" {
			name = "MCP"
		}
		return definition, name, definition.Source.Version, nil
	}
	var server MCPRegistryServer
	if err := json.Unmarshal([]byte(raw), &server); err != nil || strings.TrimSpace(server.Name) == "" {
		return core.MCPDefinitionV1{}, "", "", fmt.Errorf("JSON 必须是 MCPDefinition v1 或有效的 server.json")
	}
	candidate, err := registryInstallCandidate(server)
	return candidate.Definition, candidate.Name, candidate.Version, err
}

func resolveDirectMCPSource(ctx context.Context, input string, packageResolver MCPPackageResolver) (core.MCPDefinitionV1, string, string, bool, error) {
	lower := strings.ToLower(strings.TrimSpace(input))
	if strings.HasPrefix(lower, "npm:") {
		spec := strings.TrimSpace(input[len("npm:"):])
		if name, version, err := pinnedNPMSpec(spec); err == nil {
			if !validNPMPackageName(name) || !validExactPackageVersion(version) {
				return core.MCPDefinitionV1{}, "", "", true, fmt.Errorf("npm 包名或版本格式无效")
			}
			definition := packageMCPDefinition("npm", name, version, "npx", []string{"-y", name + "@" + version})
			return definition, name, version, true, nil
		}
		if validNPMPackageName(spec) {
			version, err := resolvePackageVersion(ctx, packageResolver, "npm", spec)
			if err != nil {
				return core.MCPDefinitionV1{}, "", "", true, err
			}
			definition := packageMCPDefinition("npm", spec, version, "npx", []string{"-y", spec + "@" + version})
			return definition, spec, version, true, nil
		}
		return core.MCPDefinitionV1{}, "", "", true, fmt.Errorf("npm 包名格式无效；请使用 npm:@scope/server 或 npm:@scope/server@1.2.3")
	}
	if strings.HasPrefix(lower, "pypi:") {
		spec := strings.TrimSpace(input[len("pypi:"):])
		parts := strings.Split(spec, "==")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			name := strings.TrimSpace(parts[0])
			version := strings.TrimSpace(parts[1])
			if !validPyPIPackageName(name) || !validExactPackageVersion(version) {
				return core.MCPDefinitionV1{}, "", "", true, fmt.Errorf("PyPI 包名或版本格式无效")
			}
			definition := packageMCPDefinition("pypi", name, version, "uvx", []string{name + "==" + version})
			return definition, name, version, true, nil
		}
		if validPyPIPackageName(spec) {
			version, err := resolvePackageVersion(ctx, packageResolver, "pypi", spec)
			if err != nil {
				return core.MCPDefinitionV1{}, "", "", true, err
			}
			definition := packageMCPDefinition("pypi", spec, version, "uvx", []string{spec + "==" + version})
			return definition, spec, version, true, nil
		}
		return core.MCPDefinitionV1{}, "", "", true, fmt.Errorf("PyPI 包名格式无效；请使用 pypi:mcp-server-git 或 pypi:mcp-server-git==1.2.3")
	}
	if name, version, ok := pinnedBareNPMSpec(input); ok {
		if !validExactPackageVersion(version) {
			return core.MCPDefinitionV1{}, "", "", true, fmt.Errorf("npm 包版本格式无效；请使用明确版本号")
		}
		definition := packageMCPDefinition("npm", name, version, "npx", []string{"-y", name + "@" + version})
		return definition, name, version, true, nil
	}
	if name, ok := bareScopedNPMPackageName(input); ok {
		version, err := resolvePackageVersion(ctx, packageResolver, "npm", name)
		if err != nil {
			return core.MCPDefinitionV1{}, "", "", true, err
		}
		definition := packageMCPDefinition("npm", name, version, "npx", []string{"-y", name + "@" + version})
		return definition, name, version, true, nil
	}
	words, err := splitMCPCommandLine(input)
	if err != nil || len(words) == 0 {
		return core.MCPDefinitionV1{}, "", "", false, err
	}
	command := strings.ToLower(words[0])
	if command != "npx" && command != "uvx" && command != "node" && command != "python" && command != "python3" {
		return core.MCPDefinitionV1{}, "", "", false, nil
	}
	definition := core.MCPDefinitionV1{
		SchemaVersion: core.MCPDefinitionSchemaV1,
		Source:        core.MCPSource{Type: "local", Reference: input},
		Transport:     core.MCPTransport{Type: "stdio", Command: words[0], Args: words[1:]},
		Auth:          core.MCPAuth{Type: "none"}, Permissions: []string{"process"},
	}
	return definition, command, "local", true, nil
}

func resolvePackageVersion(ctx context.Context, resolver MCPPackageResolver, ecosystem, packageName string) (string, error) {
	if resolver == nil {
		return "", fmt.Errorf("包版本解析器暂不可用；请提供固定版本，例如 npm:@scope/server@1.2.3")
	}
	version, err := resolver.Resolve(ctx, ecosystem, packageName)
	if err != nil {
		return "", fmt.Errorf("解析 %s 包 %s 的版本失败: %w", ecosystem, packageName, err)
	}
	version = strings.TrimSpace(version)
	if !validExactPackageVersion(version) {
		return "", fmt.Errorf("%s 元数据返回了无效版本，已拒绝浮动或未知版本", ecosystem)
	}
	return version, nil
}

func pinnedBareNPMSpec(spec string) (string, string, bool) {
	value := strings.TrimSpace(spec)
	if strings.ContainsAny(value, " \t\r\n\"'") {
		return "", "", false
	}
	if strings.HasPrefix(value, "@") {
		slash := strings.Index(value, "/")
		if slash <= 1 || slash == len(value)-1 {
			return "", "", false
		}
	}
	name, version, err := pinnedNPMSpec(value)
	if err != nil || !validNPMPackageName(name) {
		return "", "", false
	}
	return name, version, true
}

func bareScopedNPMPackageName(spec string) (string, bool) {
	value := strings.TrimSpace(spec)
	if !strings.HasPrefix(value, "@") || strings.ContainsAny(value, " \t\r\n\"'") || !validNPMPackageName(value) {
		return "", false
	}
	return value, true
}

func validNPMPackageName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, "@") {
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 || len(parts[0]) < 2 || parts[1] == "" {
			return false
		}
		return validNPMSegment(parts[0][1:]) && validNPMSegment(parts[1])
	}
	return validNPMSegment(name)
}

func validNPMSegment(value string) bool {
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

func validExactPackageVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 200 || version[0] < '0' || version[0] > '9' {
		return false
	}
	for _, char := range version {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '-' || char == '+' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func pinnedNPMSpec(spec string) (string, string, error) {
	index := strings.LastIndex(spec, "@")
	if index <= 0 || index == len(spec)-1 {
		return "", "", fmt.Errorf("npm 包必须固定版本，例如 npm:@scope/server@1.2.3")
	}
	return spec[:index], spec[index+1:], nil
}

func packageMCPDefinition(sourceType, name, version, command string, args []string) core.MCPDefinitionV1 {
	return core.MCPDefinitionV1{
		SchemaVersion: core.MCPDefinitionSchemaV1,
		Source:        core.MCPSource{Type: sourceType, Reference: name, Version: version},
		Transport:     core.MCPTransport{Type: "stdio", Command: command, Args: args},
		Auth:          core.MCPAuth{Type: "none"}, Permissions: []string{"process"},
	}
}

func registryInstallCandidate(server MCPRegistryServer) (core.MCPInstallCandidate, error) {
	definition := core.MCPDefinitionV1{
		SchemaVersion: core.MCPDefinitionSchemaV1,
		Source:        core.MCPSource{Type: "registry", Reference: server.Name, Version: server.Version, Publisher: server.Repository.URL},
		Auth:          core.MCPAuth{Type: "none"}, Description: server.Description,
	}
	for _, remote := range server.Remotes {
		transport := strings.ToLower(strings.TrimSpace(remote.Type))
		if transport == "" {
			transport = strings.ToLower(strings.TrimSpace(remote.TransportType))
		}
		if (transport == "streamable-http" || transport == "streamable_http") && strings.TrimSpace(remote.URL) != "" {
			definition.Transport = core.MCPTransport{Type: "streamable_http", URL: remote.URL}
			definition.Permissions = []string{"network"}
			definition.Auth.Type = "oauth"
			for _, header := range remote.Headers {
				if !header.Secret || strings.TrimSpace(header.Name) == "" {
					continue
				}
				name := strings.TrimSpace(header.Name)
				bindingName := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
				definition.SecretBindings = append(definition.SecretBindings, core.MCPSecretBinding{Name: bindingName, Target: "header:" + name, Required: header.Required})
				if strings.EqualFold(name, "Authorization") {
					definition.Auth.Type = "bearer"
				} else if definition.Auth.Type == "oauth" {
					definition.Auth = core.MCPAuth{Type: "custom_header", Header: name}
				}
			}
			break
		}
	}
	if definition.Transport.Type == "" {
		for _, pkg := range server.Packages {
			version := strings.TrimSpace(pkg.Version)
			if version == "" {
				version = strings.TrimSpace(server.Version)
			}
			if version == "" {
				continue
			}
			switch strings.ToLower(pkg.RegistryType) {
			case "npm":
				definition.Transport = core.MCPTransport{Type: "stdio", Command: "npx", Args: []string{"-y", pkg.Identifier + "@" + version}}
			case "pypi":
				definition.Transport = core.MCPTransport{Type: "stdio", Command: "uvx", Args: []string{pkg.Identifier + "==" + version}}
			default:
				continue
			}
			definition.Permissions = []string{"process"}
			for _, variable := range pkg.EnvironmentVariables {
				if !variable.Secret {
					continue
				}
				definition.SecretBindings = append(definition.SecretBindings, core.MCPSecretBinding{Name: variable.Name, Target: "env:" + variable.Name, Required: variable.Required})
			}
			if len(definition.SecretBindings) > 0 {
				definition.Auth.Type = "environment"
				definition.Transport.AuthEnv = strings.TrimPrefix(definition.SecretBindings[0].Target, "env:")
			}
			break
		}
	}
	if definition.Transport.Type == "" {
		return core.MCPInstallCandidate{}, fmt.Errorf("registry server has no supported transport")
	}
	encoded, err := definition.JSON()
	if err != nil {
		return core.MCPInstallCandidate{}, err
	}
	if _, err := core.ParseMCPDefinition(encoded); err != nil {
		return core.MCPInstallCandidate{}, err
	}
	return core.MCPInstallCandidate{Name: server.Name, Title: server.Title, Version: server.Version, Description: server.Description, RepositoryURL: server.Repository.URL, Definition: definition}, nil
}

func finalizeMCPDraft(draft core.ResourceInstallDraft, name, version string, definition core.MCPDefinitionV1, warnings []string) core.ResourceInstallDraft {
	draft.ResolvedName = name
	draft.ResolvedVersion = version
	draft.MCPDefinition = &definition
	draft.Candidates = nil
	draft.RequiredSecrets = append([]core.MCPSecretBinding(nil), definition.SecretBindings...)
	draft.Warnings = append(draft.Warnings, warnings...)
	draft.Status = "validated"
	for _, binding := range draft.RequiredSecrets {
		if binding.Required {
			draft.Status = "needs_secret"
			break
		}
	}
	return draft
}

func remoteMCPName(raw string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	value = strings.TrimSuffix(value, "/")
	if slash := strings.Index(value, "/"); slash >= 0 {
		value = value[:slash]
	}
	return value
}

func splitMCPCommandLine(input string) ([]string, error) {
	var result []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			result = append(result, current.String())
			current.Reset()
		}
	}
	for _, char := range strings.TrimSpace(input) {
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ' ' || char == '\t' || char == '\r' || char == '\n' {
			flush()
			continue
		}
		current.WriteRune(char)
	}
	if quote != 0 {
		return nil, fmt.Errorf("本地命令包含未闭合的引号")
	}
	flush()
	return result, nil
}
