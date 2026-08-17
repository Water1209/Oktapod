//go:build windows

package secretstore

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func writeBootstrapKey(path string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("bootstrap key must be 256-bit")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return err
	}
	if err := restrictWindowsPath(directory); err != nil {
		return fmt.Errorf("restrict vault directory ACL: %w", err)
	}
	if err := restrictWindowsPath(path); err != nil {
		return fmt.Errorf("restrict bootstrap key ACL: %w", err)
	}
	return nil
}

// WriteProtectedFile writes user-exported recovery material with an explicit
// owner-only DACL. It does not rewrite the parent directory ACL because the
// caller may choose an existing documents or removable-media directory.
func WriteProtectedFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return restrictWindowsPath(path)
}

func restrictWindowsPath(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}
