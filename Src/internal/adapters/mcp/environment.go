package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const mcpSecretSetSchemaV1 = "oktapod.mcp-secrets/v1"

type mcpSecretSet struct {
	Schema string            `json:"schema"`
	Values map[string]string `json:"values"`
}

func decodeCredentialSet(supplied string) (map[string]string, bool, error) {
	if !strings.HasPrefix(strings.TrimSpace(supplied), "{") {
		return nil, false, nil
	}
	var envelope mcpSecretSet
	if err := json.Unmarshal([]byte(supplied), &envelope); err != nil {
		return nil, false, err
	}
	if envelope.Schema != mcpSecretSetSchemaV1 || len(envelope.Values) == 0 {
		return nil, false, nil
	}
	return envelope.Values, true, nil
}

// mcpCommandEnvironment deliberately starts from an allow-list. A stdio MCP
// server must not inherit the Oktapod process environment (which may include
// the model key); a manifest can opt into one named auth variable only.
func mcpCommandEnvironment(spec manifest, credential string) []string {
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true,
		"TEMP": true, "TMP": true, "LANG": true, "LC_ALL": true, "COMSPEC": true,
	}
	result := make([]string, 0, len(allowed)+10)
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if found && allowed[strings.ToUpper(name)] {
			result = append(result, item)
		}
	}
	isolationRoot := mcpCommandIsolationRoot(spec)
	for _, directory := range []string{
		isolationRoot,
		filepath.Join(isolationRoot, "home"),
		filepath.Join(isolationRoot, "appdata", "roaming"),
		filepath.Join(isolationRoot, "appdata", "local"),
		filepath.Join(isolationRoot, "npm-cache"),
		filepath.Join(isolationRoot, "uv-cache"),
	} {
		_ = os.MkdirAll(directory, 0o700)
	}
	result = append(result,
		"HOME="+filepath.Join(isolationRoot, "home"),
		"USERPROFILE="+filepath.Join(isolationRoot, "home"),
		"HOMEDRIVE=",
		"HOMEPATH=",
		"APPDATA="+filepath.Join(isolationRoot, "appdata", "roaming"),
		"LOCALAPPDATA="+filepath.Join(isolationRoot, "appdata", "local"),
		"XDG_CACHE_HOME="+filepath.Join(isolationRoot, "cache"),
		"NPM_CONFIG_CACHE="+filepath.Join(isolationRoot, "npm-cache"),
		"UV_CACHE_DIR="+filepath.Join(isolationRoot, "uv-cache"),
	)
	values, isSet, _ := decodeCredentialSet(credential)
	if isSet {
		for _, binding := range spec.SecretBindings {
			if name, ok := strings.CutPrefix(binding.Target, "env:"); ok {
				if value := strings.TrimSpace(values[binding.Name]); value != "" {
					result = append(result, name+"="+value)
				}
			}
		}
	} else if authEnv := strings.TrimSpace(spec.AuthEnv); authEnv != "" {
		if value := strings.TrimSpace(credential); value != "" {
			result = append(result, authEnv+"="+value)
		}
	}
	return result
}

func mcpCommandIsolationRoot(spec manifest) string {
	parts := []string{strings.ToLower(strings.TrimSpace(spec.Command))}
	for _, arg := range spec.Args {
		value := strings.TrimSpace(arg)
		if filepath.IsAbs(value) {
			value = "<absolute-path>"
		}
		parts = append(parts, value)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return filepath.Join(os.TempDir(), "oktapod-mcp-runtime", hex.EncodeToString(sum[:8]))
}
