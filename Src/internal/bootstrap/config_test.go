package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLoadConfigModelTimeoutDefaultsAndOverrides(t *testing.T) {
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, "profile: personal\n"))
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ModelTimeouts.FirstToken != 15*time.Second || config.ModelTimeouts.StreamIdle != time.Minute || config.ModelTimeouts.TurnHigh != 10*time.Minute {
		t.Fatalf("defaults=%+v", config.ModelTimeouts)
	}

	body := "profile: personal\nmodel:\n  first_token_timeout: 30s\n  stream_idle_timeout: 45s\n  turn_timeouts:\n    auto: 90s\n    off: 60s\n    low: 120s\n    medium: 240s\n    high: 480s\n"
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, body))
	config, err = LoadConfig()
	if err != nil || config.ModelTimeouts.TurnAuto != 90*time.Second || config.ModelTimeouts.TurnHigh != 8*time.Minute {
		t.Fatalf("config=%+v err=%v", config.ModelTimeouts, err)
	}
}

func TestLoadConfigRejectsUnsafeOrRegressiveModelTimeouts(t *testing.T) {
	for name, body := range map[string]string{
		"too short":        "profile: personal\nmodel:\n  first_token_timeout: 1s\n",
		"token after idle": "profile: personal\nmodel:\n  first_token_timeout: 90s\n  stream_idle_timeout: 60s\n",
		"regressive":       "profile: personal\nmodel:\n  turn_timeouts:\n    off: 120s\n    low: 60s\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, body))
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid model timeout configuration was accepted")
			}
		})
	}
}

func TestLoadConfigRejectsUnknownYAMLFields(t *testing.T) {
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, "profile: personal\nunknown: true\n"))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unknown YAML field must fail closed")
	}
}

func TestLoadConfigPrivateFetchDefaultsAndStrictRules(t *testing.T) {
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, "profile: personal\n"))
	config, err := LoadConfig()
	if err != nil || config.WebAccessPolicy.PrivateFetchEnabled || len(config.WebAccessPolicy.AllowedOrigins) != 0 {
		t.Fatalf("default web access=%#v err=%v", config.WebAccessPolicy, err)
	}

	valid := "profile: personal\nweb_access:\n  private_fetch:\n    enabled: true\n    allowed_origins:\n      - id: intranet-wiki\n        origin: http://192.168.10.20:8080\n"
	t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, valid))
	config, err = LoadConfig()
	if err != nil || !config.WebAccessPolicy.PrivateFetchEnabled || len(config.WebAccessPolicy.AllowedOrigins) != 1 {
		t.Fatalf("valid web access=%#v err=%v", config.WebAccessPolicy, err)
	}

	for name, body := range map[string]string{
		"empty":   "profile: personal\nweb_access:\n  private_fetch:\n    enabled: true\n",
		"unknown": "profile: personal\nweb_access:\n  private_fetch:\n    enabled: false\n    allow_cidr: true\n",
		"domain":  "profile: personal\nweb_access:\n  private_fetch:\n    enabled: true\n    allowed_origins:\n      - id: wiki\n        origin: http://wiki.internal:8080\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OKTAPOD_CONFIG_FILE", writeTestConfig(t, body))
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid private fetch configuration was accepted")
			}
		})
	}
}

func TestConfigManagerPrivateFetchRequiresRestart(t *testing.T) {
	path := writeTestConfig(t, "profile: personal\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", path)
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	manager := NewConfigManager(config)
	updated, err := manager.Update(core.UpdateBootstrapConfiguration{WebAccess: &core.WebAccessConfiguration{PrivateFetch: core.PrivateFetchConfiguration{
		Enabled: true, AllowedOrigins: []core.WebPrivateOrigin{{ID: "preview", Origin: "http://127.0.0.1:3000"}},
	}}})
	if err != nil || !updated.RestartRequired || !updated.WebAccess.PrivateFetch.Enabled {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	current := manager.Snapshot()
	if current.WebAccess.PrivateFetch.Enabled || current.RestartRequired {
		t.Fatalf("pending configuration changed live runtime: %#v", current)
	}
}

func TestConfigManagerReturnsValidationErrorForInvalidPrivateFetchUpdate(t *testing.T) {
	path := writeTestConfig(t, "profile: personal\n")
	t.Setenv("OKTAPOD_CONFIG_FILE", path)
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewConfigManager(config).Update(core.UpdateBootstrapConfiguration{WebAccess: &core.WebAccessConfiguration{PrivateFetch: core.PrivateFetchConfiguration{
		Enabled: true, AllowedOrigins: []core.WebPrivateOrigin{{ID: "wiki", Origin: ""}},
	}}})
	var validation *core.ValidationError
	if !errors.As(err, &validation) || validation.Field != "configuration" {
		t.Fatalf("expected configuration validation error, got %T %v", err, err)
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
