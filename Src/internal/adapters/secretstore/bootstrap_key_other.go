//go:build !windows

package secretstore

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeBootstrapKey(path string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("bootstrap key must be 256-bit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// WriteProtectedFile writes user-exported recovery material for the current
// service account only. The parent directory may be an existing user-selected
// location, so its permissions are not changed.
func WriteProtectedFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
