package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestCapabilityTrustedKeysParsesPublicKeysOnly(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rawBytes, err := json.Marshal(map[string]string{"release-1": base64.StdEncoding.EncodeToString(publicKey)})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := CapabilityTrustedKeys(string(rawBytes))
	if err != nil || len(keys) != 1 || !keys["release-1"].Equal(publicKey) {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if _, err := CapabilityTrustedKeys(`{"release-1":"not-a-key"}`); err == nil {
		t.Fatal("invalid public key must fail closed")
	}
	if keys, err := CapabilityTrustedKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("empty config keys=%v err=%v", keys, err)
	}
}

func TestLoadConfigReadsSeparatePostgresBackupURL(t *testing.T) {
	path := writeTestConfig(t, "profile: team\nlisten_address: 127.0.0.1:8787\nrun_concurrency: 4\ndatabase:\n  driver: postgresql\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", path)
	t.Setenv("OKTAPOD_DATABASE_URL", "postgres://app@localhost/oktapod")
	t.Setenv("OKTAPOD_DATABASE_BACKUP_URL", "postgres://backup@localhost/oktapod")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseBackupURL != "postgres://backup@localhost/oktapod" {
		t.Fatalf("backup url=%q", config.DatabaseBackupURL)
	}
}

func TestLoadConfigIgnoresLegacyModelEnvironment(t *testing.T) {
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, "profile: personal\n"))
	t.Setenv("OKTAPOD_MODEL_ENDPOINT", "not-a-url")
	t.Setenv("OKTAPOD_MODEL", "legacy-model")
	t.Setenv("OKTAPOD_MODEL_API_KEY_FILE", "legacy.key")
	t.Setenv("OKTAPOD_REVIEWER_MODEL", "legacy-reviewer")
	t.Setenv("OKTAPOD_MODEL_PRICING_JSON", `{"legacy-model":{"input_per_1k":99}}`)
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("model configuration must come from the database, err=%v", err)
	}
}

func TestLoadConfigCommandLinePathOverridesEnvironment(t *testing.T) {
	environmentPath := writeTestConfig(t, "profile: personal\nrun_concurrency: 1\n")
	commandPath := writeTestConfig(t, "profile: personal\nrun_concurrency: 3\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", environmentPath)
	previous := os.Args
	os.Args = []string{"oktapod", "config", "doctor", "--config", commandPath}
	t.Cleanup(func() { os.Args = previous })
	config, err := LoadConfig()
	if err != nil || config.RunConcurrency != 3 || config.ConfigPath != commandPath {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestLoadConfigDefaultsRunConcurrencyAndAllowsOnlyLowerValues(t *testing.T) {
	path := writeTestConfig(t, "profile: personal\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", path)
	config, err := LoadConfig()
	if err != nil || config.RunConcurrency != 4 {
		t.Fatalf("default concurrency=%d err=%v", config.RunConcurrency, err)
	}
	if err := os.WriteFile(path, []byte("profile: personal\nrun_concurrency: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err = LoadConfig()
	if err != nil || config.RunConcurrency != 2 {
		t.Fatalf("configured concurrency=%d err=%v", config.RunConcurrency, err)
	}
	if err := os.WriteFile(path, []byte("profile: personal\nrun_concurrency: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("concurrency above the Release 1A maximum must fail")
	}
}

func TestLoadConfigRejectsUnknownYAMLFields(t *testing.T) {
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, "profile: personal\nunknown: true\n"))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unknown YAML field must fail closed")
	}
}

func TestConfigManagerPreservesPortableBlankDataRoot(t *testing.T) {
	path := writeTestConfig(t, "profile: personal\ndata_root: \"\"\nrun_concurrency: 4\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", path)
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	concurrency := 2
	if _, err := NewConfigManager(config).Update(core.UpdateBootstrapConfiguration{RunConcurrency: &concurrency}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), config.DataRoot) || !strings.Contains(string(data), "data_root: \"\"") {
		t.Fatalf("configuration became machine-bound:\n%s", data)
	}
}

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
