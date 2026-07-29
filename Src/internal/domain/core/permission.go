package core

import (
	"encoding/json"
	"time"
)

// FullAccessActive reports whether the user explicitly selected full access
// for this Run. The mode remains active until the user changes the Composer
// control; deterministic path, executable and network denies still apply.
// The time parameter remains for source compatibility with policy callers.
func FullAccessActive(snapshot string, _ time.Time) bool {
	var config map[string]any
	return json.Unmarshal([]byte(snapshot), &config) == nil && config["permission_mode"] == PermissionModeFullAccess
}
