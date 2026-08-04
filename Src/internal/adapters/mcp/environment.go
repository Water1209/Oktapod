package mcp

import (
	"os"
	"strings"
)

// mcpCommandEnvironment deliberately starts from an allow-list. A stdio MCP
// server must not inherit the Oktapod process environment (which may include
// the model key); a manifest can opt into one named auth variable only.
func mcpCommandEnvironment(spec manifest, credential string) []string {
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true,
		"TEMP": true, "TMP": true, "LANG": true, "LC_ALL": true,
	}
	result := make([]string, 0, len(allowed)+1)
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if found && allowed[strings.ToUpper(name)] {
			result = append(result, item)
		}
	}
	if authEnv := strings.TrimSpace(spec.AuthEnv); authEnv != "" {
		if value := strings.TrimSpace(credential); value != "" {
			result = append(result, authEnv+"="+value)
		}
	}
	return result
}
