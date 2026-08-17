package secretstore

import "strings"

func ModelCredentialRef(organizationID, credentialID string) string {
	return "model-provider/" + strings.TrimSpace(organizationID) + "/" + strings.TrimSpace(credentialID)
}

func MCPCredentialRef(organizationID, credentialID string) string {
	return "mcp-capability/" + strings.TrimSpace(organizationID) + "/" + strings.TrimSpace(credentialID)
}
