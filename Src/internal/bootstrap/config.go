package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DefaultModelEndpoint = "https://vip-sg.freemodel.dev/v1"

type Config struct {
	Profile                   string
	ListenAddress             string
	DataRoot                  string
	DatabasePath              string
	DatabaseURL               string
	DatabaseBackupURL         string
	WorkspaceRoot             string
	CapabilityTrustedKeysJSON string
	RunConcurrency            int
}

func LoadConfig() (Config, error) {
	dataRoot := strings.TrimSpace(os.Getenv("OKTAPOD_DATA_ROOT"))
	if dataRoot == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		dataRoot = filepath.Join(base, "Oktapod")
	}
	absoluteRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data root: %w", err)
	}

	profile := strings.ToLower(strings.TrimSpace(os.Getenv("OKTAPOD_PROFILE")))
	if profile == "" {
		profile = "personal"
	}
	if profile != "personal" && profile != "team" {
		return Config{}, fmt.Errorf("OKTAPOD_PROFILE must be personal or team")
	}

	listenAddress := strings.TrimSpace(os.Getenv("OKTAPOD_LISTEN_ADDRESS"))
	if listenAddress == "" {
		listenAddress = "127.0.0.1:8787"
	}
	if profile == "personal" && !isLoopbackAddress(listenAddress) {
		return Config{}, fmt.Errorf("personal profile only permits loopback listen addresses")
	}
	runConcurrency := 4
	if raw := strings.TrimSpace(os.Getenv("OKTAPOD_RUN_CONCURRENCY")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 4 {
			return Config{}, fmt.Errorf("OKTAPOD_RUN_CONCURRENCY must be an integer from 1 to 4")
		}
		runConcurrency = value
	}

	return Config{
		Profile:                   profile,
		ListenAddress:             listenAddress,
		DataRoot:                  absoluteRoot,
		DatabasePath:              filepath.Join(absoluteRoot, "oktapod.db"),
		DatabaseURL:               strings.TrimSpace(os.Getenv("OKTAPOD_DATABASE_URL")),
		DatabaseBackupURL:         strings.TrimSpace(os.Getenv("OKTAPOD_DATABASE_BACKUP_URL")),
		WorkspaceRoot:             filepath.Join(absoluteRoot, "workspaces"),
		CapabilityTrustedKeysJSON: strings.TrimSpace(os.Getenv("OKTAPOD_CAPABILITY_TRUSTED_KEYS_JSON")),
		RunConcurrency:            runConcurrency,
	}, nil
}

// CapabilityTrustedKeys parses a non-secret JSON object of key id to base64
// Ed25519 public key. Private keys and signing material are never accepted by
// the runtime configuration.
func CapabilityTrustedKeys(raw string) (map[string]ed25519.PublicKey, error) {
	keys := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return map[string]ed25519.PublicKey{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("OKTAPOD_CAPABILITY_TRUSTED_KEYS_JSON must be a JSON object: %w", err)
	}
	trusted := make(map[string]ed25519.PublicKey, len(keys))
	for id, encoded := range keys {
		id = strings.TrimSpace(id)
		if id == "" || len([]rune(id)) > 120 {
			return nil, fmt.Errorf("capability trusted key id is invalid")
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("capability trusted key %q must be a base64 Ed25519 public key", id)
		}
		trusted[id] = ed25519.PublicKey(decoded)
	}
	return trusted, nil
}

func isLoopbackAddress(address string) bool {
	host := address
	if index := strings.LastIndex(address, ":"); index >= 0 {
		host = strings.Trim(address[:index], "[]")
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == ""
}
