package core

import (
	"encoding/json"
	"strings"
	"time"
)

// FullAccessActive reports whether a Run snapshot contains the explicit,
// time-bounded full-access window. Invalid or incomplete snapshots fail
// closed. Callers pass the current time so the decision is deterministic in
// tests and consistent across adapters.
func FullAccessActive(snapshot string, now time.Time) bool {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil || config["permission_mode"] != PermissionModeFullAccess {
		return false
	}
	expires, ok := config["full_access_expires_at"].(string)
	if !ok || strings.TrimSpace(expires) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	return err == nil && now.UTC().Before(expiresAt)
}
