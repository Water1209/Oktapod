package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
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
	t.Setenv("OKTAPOD_PROFILE", "team")
	t.Setenv("OKTAPOD_DATA_ROOT", t.TempDir())
	t.Setenv("OKTAPOD_DATABASE_URL", "postgres://app@localhost/oktapod")
	t.Setenv("OKTAPOD_DATABASE_BACKUP_URL", "postgres://backup@localhost/oktapod")
	t.Setenv("OKTAPOD_LISTEN_ADDRESS", "127.0.0.1:8787")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseBackupURL != "postgres://backup@localhost/oktapod" {
		t.Fatalf("backup url=%q", config.DatabaseBackupURL)
	}
}

func TestLoadConfigIgnoresLegacyModelEnvironment(t *testing.T) {
	t.Setenv("OKTAPOD_DATA_ROOT", t.TempDir())
	t.Setenv("OKTAPOD_MODEL_ENDPOINT", "not-a-url")
	t.Setenv("OKTAPOD_MODEL", "legacy-model")
	t.Setenv("OKTAPOD_MODEL_API_KEY_FILE", "legacy.key")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("model configuration must come from the database, err=%v", err)
	}
}

func TestLoadConfigDefaultsRunConcurrencyAndAllowsOnlyLowerValues(t *testing.T) {
	t.Setenv("OKTAPOD_DATA_ROOT", t.TempDir())
	config, err := LoadConfig()
	if err != nil || config.RunConcurrency != 4 {
		t.Fatalf("default concurrency=%d err=%v", config.RunConcurrency, err)
	}
	t.Setenv("OKTAPOD_RUN_CONCURRENCY", "2")
	config, err = LoadConfig()
	if err != nil || config.RunConcurrency != 2 {
		t.Fatalf("configured concurrency=%d err=%v", config.RunConcurrency, err)
	}
	t.Setenv("OKTAPOD_RUN_CONCURRENCY", "5")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("concurrency above the Release 1A maximum must fail")
	}
}
