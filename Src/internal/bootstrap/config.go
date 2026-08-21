package bootstrap

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const DefaultModelEndpoint = "https://vip-sg.freemodel.dev/v1"

type Config struct {
	Profile                   string
	ListenAddress             string
	DataRoot                  string
	DatabasePath              string
	DatabaseURL               string
	DatabaseBackupURL         string
	DatabaseDriver            string
	WorkspaceRoot             string
	CapabilityTrustedKeysJSON string
	CapabilityTrustedKeysFile string
	RunConcurrency            int
	WebAccessPolicy           core.WebAccessPolicy
	ConfigPath                string
	ConfigSource              string
}

type configFile struct {
	Profile        string `yaml:"profile"`
	ListenAddress  string `yaml:"listen_address"`
	DataRoot       string `yaml:"data_root"`
	RunConcurrency int    `yaml:"run_concurrency"`
	Database       struct {
		Driver string `yaml:"driver"`
	} `yaml:"database"`
	Capabilities struct {
		TrustedKeysFile string `yaml:"trusted_keys_file"`
	} `yaml:"capabilities"`
	WebAccess struct {
		PrivateFetch struct {
			Enabled        bool                    `yaml:"enabled"`
			AllowedOrigins []core.WebPrivateOrigin `yaml:"allowed_origins"`
		} `yaml:"private_fetch"`
	} `yaml:"web_access"`
}

func defaultConfigFile() configFile {
	var file configFile
	file.Profile = "personal"
	file.ListenAddress = "127.0.0.1:8787"
	file.RunConcurrency = 4
	file.Database.Driver = "sqlite"
	file.Capabilities.TrustedKeysFile = "trusted-keys.json"
	return file
}

func LoadConfig() (Config, error) {
	defaultRoot, err := defaultDataRoot()
	if err != nil {
		return Config{}, err
	}
	path, source, explicit, err := resolveConfigPath(defaultRoot, os.Args[1:])
	if err != nil {
		return Config{}, err
	}
	file := defaultConfigFile()
	if data, readErr := os.ReadFile(path); readErr == nil {
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&file); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return Config{}, fmt.Errorf("parse config %s: multiple YAML documents are not allowed", path)
			}
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
		source += ":file"
	} else if !errors.Is(readErr, os.ErrNotExist) || explicit {
		return Config{}, fmt.Errorf("read config %s: %w", path, readErr)
	} else {
		source = "built-in defaults"
	}
	return materializeConfig(file, path, source, defaultRoot)
}

func materializeConfig(file configFile, configPath, source, defaultRoot string) (Config, error) {
	profile := strings.ToLower(strings.TrimSpace(file.Profile))
	if profile == "" {
		profile = "personal"
	}
	if profile != "personal" && profile != "team" {
		return Config{}, fmt.Errorf("profile must be personal or team")
	}
	listenAddress := strings.TrimSpace(file.ListenAddress)
	if listenAddress == "" {
		listenAddress = "127.0.0.1:8787"
	}
	if profile == "personal" && !isLoopbackAddress(listenAddress) {
		return Config{}, fmt.Errorf("personal profile only permits loopback listen addresses")
	}
	if file.RunConcurrency == 0 {
		file.RunConcurrency = 4
	}
	if file.RunConcurrency < 1 || file.RunConcurrency > 4 {
		return Config{}, fmt.Errorf("run_concurrency must be an integer from 1 to 4")
	}
	driver := strings.ToLower(strings.TrimSpace(file.Database.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" && driver != "postgresql" {
		return Config{}, fmt.Errorf("database.driver must be sqlite or postgresql")
	}
	if profile == "personal" && driver != "sqlite" {
		return Config{}, fmt.Errorf("personal profile requires the sqlite database driver")
	}
	dataRoot := strings.TrimSpace(file.DataRoot)
	if dataRoot == "" {
		dataRoot = defaultRoot
	} else if !filepath.IsAbs(dataRoot) {
		dataRoot = filepath.Join(filepath.Dir(configPath), dataRoot)
	}
	absoluteRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data root: %w", err)
	}
	trustedKeysFile := strings.TrimSpace(file.Capabilities.TrustedKeysFile)
	if trustedKeysFile != "" && !filepath.IsAbs(trustedKeysFile) {
		trustedKeysFile = filepath.Join(filepath.Dir(configPath), trustedKeysFile)
	}
	trustedKeysJSON := ""
	if trustedKeysFile != "" {
		if data, readErr := os.ReadFile(trustedKeysFile); readErr == nil {
			trustedKeysJSON = string(data)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read capability trusted keys file: %w", readErr)
		}
	}
	webPolicy, err := (core.WebAccessPolicy{
		PrivateFetchEnabled: file.WebAccess.PrivateFetch.Enabled,
		AllowedOrigins:      append([]core.WebPrivateOrigin(nil), file.WebAccess.PrivateFetch.AllowedOrigins...),
		ListenOrigin:        listenOrigin(listenAddress),
	}).Validate()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Profile: profile, ListenAddress: listenAddress, DataRoot: absoluteRoot,
		DatabasePath: filepath.Join(absoluteRoot, "oktapod.db"), DatabaseDriver: driver,
		DatabaseURL:               strings.TrimSpace(os.Getenv("OKTAPOD_DATABASE_URL")),
		DatabaseBackupURL:         strings.TrimSpace(os.Getenv("OKTAPOD_DATABASE_BACKUP_URL")),
		WorkspaceRoot:             filepath.Join(absoluteRoot, "workspaces"),
		CapabilityTrustedKeysJSON: trustedKeysJSON, CapabilityTrustedKeysFile: trustedKeysFile,
		WebAccessPolicy: webPolicy,
		RunConcurrency:  file.RunConcurrency, ConfigPath: configPath, ConfigSource: source,
	}, nil
}

func defaultDataRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(base, "Oktapod"), nil
}

func resolveConfigPath(defaultRoot string, args []string) (path, source string, explicit bool, err error) {
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--config" {
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return "", "", false, fmt.Errorf("--config requires a path")
			}
			absolute, err := filepath.Abs(args[index+1])
			return absolute, "command line", true, err
		}
		if strings.HasPrefix(argument, "--config=") {
			value := strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
			if value == "" {
				return "", "", false, fmt.Errorf("--config requires a path")
			}
			absolute, err := filepath.Abs(value)
			return absolute, "command line", true, err
		}
	}
	if value := strings.TrimSpace(os.Getenv("OKTAPOD_CONFIG_FILE")); value != "" {
		absolute, err := filepath.Abs(value)
		return absolute, "OKTAPOD_CONFIG_FILE", true, err
	}
	return filepath.Join(defaultRoot, "config.yaml"), "default config.yaml", false, nil
}

type ConfigManager struct {
	mu      sync.Mutex
	current Config
}

func NewConfigManager(config Config) *ConfigManager { return &ConfigManager{current: config} }

func (m *ConfigManager) Snapshot() core.BootstrapConfiguration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return core.BootstrapConfiguration{
		Profile: m.current.Profile, ListenAddress: m.current.ListenAddress,
		DataRoot: m.current.DataRoot, RunConcurrency: m.current.RunConcurrency,
		DatabaseDriver: m.current.DatabaseDriver, TrustedKeysFile: m.current.CapabilityTrustedKeysFile,
		WebAccess:  webAccessConfiguration(m.current.WebAccessPolicy),
		ConfigPath: m.current.ConfigPath, Source: m.current.ConfigSource, RestartRequired: false,
	}
}

func (m *ConfigManager) Update(input core.UpdateBootstrapConfiguration) (core.BootstrapConfiguration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := defaultConfigFile()
	if data, err := os.ReadFile(m.current.ConfigPath); err == nil {
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&file); err != nil {
			return core.BootstrapConfiguration{}, fmt.Errorf("parse config %s: %w", m.current.ConfigPath, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return core.BootstrapConfiguration{}, fmt.Errorf("parse config %s: multiple YAML documents are not allowed", m.current.ConfigPath)
			}
			return core.BootstrapConfiguration{}, fmt.Errorf("parse config %s: %w", m.current.ConfigPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return core.BootstrapConfiguration{}, fmt.Errorf("read config %s: %w", m.current.ConfigPath, err)
	}
	if input.Profile != nil {
		file.Profile = strings.TrimSpace(*input.Profile)
	}
	if input.ListenAddress != nil {
		file.ListenAddress = strings.TrimSpace(*input.ListenAddress)
	}
	if input.DataRoot != nil {
		file.DataRoot = strings.TrimSpace(*input.DataRoot)
	}
	if input.RunConcurrency != nil {
		file.RunConcurrency = *input.RunConcurrency
	}
	if input.DatabaseDriver != nil {
		file.Database.Driver = strings.TrimSpace(*input.DatabaseDriver)
	}
	if input.TrustedKeysFile != nil {
		file.Capabilities.TrustedKeysFile = strings.TrimSpace(*input.TrustedKeysFile)
	}
	if input.WebAccess != nil {
		file.WebAccess.PrivateFetch.Enabled = input.WebAccess.PrivateFetch.Enabled
		file.WebAccess.PrivateFetch.AllowedOrigins = append([]core.WebPrivateOrigin(nil), input.WebAccess.PrivateFetch.AllowedOrigins...)
	}
	materialized, err := materializeConfig(file, m.current.ConfigPath, "configuration API", m.current.DataRoot)
	if err != nil {
		return core.BootstrapConfiguration{}, &core.ValidationError{Field: "configuration", Message: err.Error()}
	}
	data, err := yaml.Marshal(file)
	if err != nil {
		return core.BootstrapConfiguration{}, err
	}
	if err := os.MkdirAll(filepath.Dir(m.current.ConfigPath), 0o700); err != nil {
		return core.BootstrapConfiguration{}, err
	}
	temporary := m.current.ConfigPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return core.BootstrapConfiguration{}, err
	}
	if err := os.Rename(temporary, m.current.ConfigPath); err != nil {
		_ = os.Remove(temporary)
		return core.BootstrapConfiguration{}, err
	}
	snapshot := core.BootstrapConfiguration{
		Profile: file.Profile, ListenAddress: file.ListenAddress, DataRoot: file.DataRoot,
		RunConcurrency: file.RunConcurrency, DatabaseDriver: file.Database.Driver,
		TrustedKeysFile: file.Capabilities.TrustedKeysFile, ConfigPath: m.current.ConfigPath,
		WebAccess: webAccessConfiguration(materialized.WebAccessPolicy),
		Source:    "configuration API", RestartRequired: true,
	}
	return snapshot, nil
}

func webAccessConfiguration(policy core.WebAccessPolicy) core.WebAccessConfiguration {
	origins := append([]core.WebPrivateOrigin(nil), policy.AllowedOrigins...)
	return core.WebAccessConfiguration{PrivateFetch: core.PrivateFetchConfiguration{
		Enabled: policy.PrivateFetchEnabled, AllowedOrigins: origins, AllowedOriginCount: len(origins),
	}}
}

func listenOrigin(address string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || port == "" {
		return ""
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// CapabilityTrustedKeys parses a non-secret JSON object of key id to base64
// Ed25519 public key. Private keys and signing material are never accepted.
func CapabilityTrustedKeys(raw string) (map[string]ed25519.PublicKey, error) {
	keys := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return map[string]ed25519.PublicKey{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("trusted keys file must contain a JSON object: %w", err)
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
