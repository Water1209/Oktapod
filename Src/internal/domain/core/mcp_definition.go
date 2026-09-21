package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const MCPDefinitionSchemaV1 = "oktapod.mcp/v1"

// ErrMCPNotConnected means the capability is visible but no connection health
// record has been persisted yet. It is deliberately distinct from sql.ErrNoRows,
// which the HTTP layer maps to a missing resource.
var ErrMCPNotConnected = errors.New("MCP has not been connected")

var mcpEnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var mcpHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

type MCPDefinitionV1 struct {
	SchemaVersion  string             `json:"schema_version"`
	Source         MCPSource          `json:"source"`
	Transport      MCPTransport       `json:"transport"`
	Auth           MCPAuth            `json:"auth"`
	SecretBindings []MCPSecretBinding `json:"secret_bindings,omitempty"`
	Headers        map[string]string  `json:"headers,omitempty"`
	Permissions    []string           `json:"permissions,omitempty"`
	Description    string             `json:"description,omitempty"`
}

type MCPSource struct {
	Type      string `json:"type"`
	Reference string `json:"reference,omitempty"`
	Version   string `json:"version,omitempty"`
	Publisher string `json:"publisher,omitempty"`
}

type MCPTransport struct {
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
	AuthEnv string   `json:"auth_env,omitempty"`
}

type MCPAuth struct {
	Type   string `json:"type"`
	Header string `json:"header,omitempty"`
}

type MCPSecretBinding struct {
	Name     string `json:"name"`
	Target   string `json:"target"`
	Required bool   `json:"required"`
}

// ParseMCPDefinition accepts the v1 shape and the older flat manifest, then
// returns one canonical representation. Secret values are never part of the
// definition; only their target names are retained.
func ParseMCPDefinition(raw string) (MCPDefinitionV1, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return MCPDefinitionV1{}, fmt.Errorf("must be valid JSON")
	}
	if mcpManifestContainsCredentialValue(document) {
		return MCPDefinitionV1{}, fmt.Errorf("secret values must be supplied through SecretStore")
	}
	definition := MCPDefinitionV1{SchemaVersion: MCPDefinitionSchemaV1, Source: MCPSource{Type: "manual"}, Auth: MCPAuth{Type: "none"}}
	if schema, _ := document["schema_version"].(string); schema != "" && schema != MCPDefinitionSchemaV1 {
		return MCPDefinitionV1{}, fmt.Errorf("unsupported schema_version %q", schema)
	}
	if source, ok := document["source"].(map[string]any); ok {
		definition.Source.Type, _ = source["type"].(string)
		definition.Source.Reference, _ = source["reference"].(string)
		definition.Source.Version, _ = source["version"].(string)
		definition.Source.Publisher, _ = source["publisher"].(string)
	}
	if definition.Source.Type == "" {
		definition.Source.Type = "manual"
	}
	switch transport := document["transport"].(type) {
	case string:
		definition.Transport.Type = transport
		definition.Transport.Command, _ = document["command"].(string)
		definition.Transport.URL, _ = document["url"].(string)
		definition.Transport.AuthEnv, _ = document["auth_env"].(string)
		definition.Transport.Args = stringSlice(document["args"])
	case map[string]any:
		definition.Transport.Type, _ = transport["type"].(string)
		definition.Transport.Command, _ = transport["command"].(string)
		definition.Transport.URL, _ = transport["url"].(string)
		definition.Transport.AuthEnv, _ = transport["auth_env"].(string)
		definition.Transport.Args = stringSlice(transport["args"])
	default:
		definition.Transport.Command, _ = document["command"].(string)
		definition.Transport.URL, _ = document["url"].(string)
		definition.Transport.AuthEnv, _ = document["auth_env"].(string)
		definition.Transport.Args = stringSlice(document["args"])
	}
	definition.Transport.Type = strings.ToLower(strings.TrimSpace(definition.Transport.Type))
	if definition.Transport.Type == "http" {
		definition.Transport.Type = "streamable_http"
	}
	if definition.Transport.Type == "" {
		if definition.Transport.URL != "" {
			definition.Transport.Type = "streamable_http"
		} else {
			definition.Transport.Type = "stdio"
		}
	}
	if auth, ok := document["auth"].(map[string]any); ok {
		definition.Auth.Type, _ = auth["type"].(string)
		definition.Auth.Header, _ = auth["header"].(string)
	}
	definition.Auth.Type = strings.ToLower(strings.TrimSpace(definition.Auth.Type))
	if definition.Auth.Type == "" {
		definition.Auth.Type = "none"
	}
	if definition.Transport.AuthEnv != "" && definition.Auth.Type == "none" {
		definition.Auth.Type = "environment"
	}
	if headers, ok := document["headers"].(map[string]any); ok {
		definition.Headers = map[string]string{}
		for key, value := range headers {
			text, ok := value.(string)
			if !ok {
				return MCPDefinitionV1{}, fmt.Errorf("header %q must be a string", key)
			}
			definition.Headers[key] = text
		}
	}
	definition.Permissions = stringSlice(document["permissions"])
	definition.Description, _ = document["description"].(string)
	if bindings, ok := document["secret_bindings"].([]any); ok {
		for _, rawBinding := range bindings {
			binding, ok := rawBinding.(map[string]any)
			if !ok {
				return MCPDefinitionV1{}, fmt.Errorf("secret_bindings entries must be objects")
			}
			item := MCPSecretBinding{}
			item.Name, _ = binding["name"].(string)
			item.Target, _ = binding["target"].(string)
			item.Required, _ = binding["required"].(bool)
			definition.SecretBindings = append(definition.SecretBindings, item)
		}
	}
	if len(definition.SecretBindings) == 0 && definition.Transport.AuthEnv != "" {
		definition.SecretBindings = []MCPSecretBinding{{Name: "credential", Target: "env:" + definition.Transport.AuthEnv, Required: true}}
	}
	// OAuth credentials are created by the authorization-code flow and stored
	// as one opaque SecretStore envelope. They are not user-entered header
	// bindings. Bearer and custom-header authentication still use explicit
	// write-only bindings.
	if len(definition.SecretBindings) == 0 && (definition.Auth.Type == "bearer" || definition.Auth.Type == "custom_header") {
		target := "header:Authorization"
		if definition.Auth.Type == "custom_header" {
			target = "header:" + definition.Auth.Header
		}
		definition.SecretBindings = []MCPSecretBinding{{Name: "credential", Target: target, Required: true}}
	}
	if err := validateMCPDefinition(definition); err != nil {
		return MCPDefinitionV1{}, err
	}
	return definition, nil
}

func (definition MCPDefinitionV1) JSON() (string, error) {
	encoded, err := json.Marshal(definition)
	return string(encoded), err
}

func validateMCPDefinition(definition MCPDefinitionV1) error {
	switch definition.Transport.Type {
	case "stdio":
		if strings.TrimSpace(definition.Transport.Command) == "" {
			return fmt.Errorf("stdio transport requires command")
		}
	case "streamable_http":
		target, err := url.Parse(strings.TrimSpace(definition.Transport.URL))
		if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return fmt.Errorf("streamable_http transport requires an absolute http(s) URL")
		}
	default:
		return fmt.Errorf("transport.type must be stdio or streamable_http")
	}
	switch definition.Auth.Type {
	case "none", "environment", "bearer", "oauth":
	case "custom_header":
		if strings.TrimSpace(definition.Auth.Header) == "" || strings.EqualFold(definition.Auth.Header, "authorization") {
			return fmt.Errorf("custom_header auth requires a non-Authorization header name")
		}
	default:
		return fmt.Errorf("auth.type must be none, environment, bearer, oauth, or custom_header")
	}
	if len(definition.SecretBindings) > 16 {
		return fmt.Errorf("secret_bindings must not exceed 16 entries")
	}
	bindingNames := make(map[string]bool, len(definition.SecretBindings))
	bindingTargets := make(map[string]bool, len(definition.SecretBindings))
	for _, binding := range definition.SecretBindings {
		if strings.TrimSpace(binding.Name) == "" || (!strings.HasPrefix(binding.Target, "env:") && !strings.HasPrefix(binding.Target, "header:")) {
			return fmt.Errorf("secret_bindings must declare a name and env: or header: target")
		}
		name := strings.TrimSpace(binding.Name)
		target := strings.TrimSpace(binding.Target)
		if bindingNames[name] || bindingTargets[strings.ToLower(target)] {
			return fmt.Errorf("secret_bindings names and targets must be unique")
		}
		bindingNames[name] = true
		bindingTargets[strings.ToLower(target)] = true
		if envName, ok := strings.CutPrefix(target, "env:"); ok && !mcpEnvironmentNamePattern.MatchString(envName) {
			return fmt.Errorf("secret binding environment target %q is invalid", envName)
		}
		if headerName, ok := strings.CutPrefix(target, "header:"); ok {
			lower := strings.ToLower(headerName)
			if !mcpHeaderNamePattern.MatchString(headerName) || lower == "host" || lower == "content-length" || lower == "mcp-session-id" || lower == "origin" {
				return fmt.Errorf("secret binding header target %q is reserved or invalid", headerName)
			}
		}
	}
	for key, value := range definition.Headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" || lower == "authorization" || lower == "host" || lower == "content-length" || lower == "mcp-session-id" || strings.ContainsAny(key+value, "\r\n") {
			return fmt.Errorf("header %q is reserved or invalid", key)
		}
	}
	return nil
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func mcpManifestContainsCredentialValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
			if normalized != "authenv" && (strings.Contains(normalized, "apikey") || strings.Contains(normalized, "token") || strings.Contains(normalized, "password") || strings.Contains(normalized, "secret")) {
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
			if key == "url" {
				if text, ok := child.(string); ok {
					if parsed, err := url.Parse(text); err == nil && (parsed.User != nil || containsSecretQuery(parsed.Query())) {
						return true
					}
				}
			}
			if mcpManifestContainsCredentialValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if mcpManifestContainsCredentialValue(child) {
				return true
			}
		}
	case string:
		lower := strings.ToLower(typed)
		return strings.Contains(lower, "tvly-") || strings.Contains(lower, "authorization: bearer ")
	}
	return false
}

func containsSecretQuery(values url.Values) bool {
	for key, list := range values {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		if len(list) > 0 && (strings.Contains(normalized, "key") || strings.Contains(normalized, "token") || strings.Contains(normalized, "secret")) {
			return true
		}
	}
	return false
}
