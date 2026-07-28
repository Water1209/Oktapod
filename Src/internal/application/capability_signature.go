package application

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const capabilitySignatureField = "_oktapod_signature"

// verifyCapabilityManifestSignature verifies an optional detached signature
// embedded in a declarative manifest. The signature covers the canonical JSON
// object with the signature field removed; encoding/json provides stable key
// ordering for this bounded map. Unknown keys are reported to the caller as
// untrusted rather than treated as valid.
func verifyCapabilityManifestSignature(manifestJSON string, trusted map[string]ed25519.PublicKey) (present, verified bool, keyID string, err error) {
	var manifest map[string]any
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return false, false, "", err
	}
	raw, exists := manifest[capabilitySignatureField]
	if !exists {
		return false, false, "", nil
	}
	delete(manifest, capabilitySignatureField)
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return true, false, "", fmt.Errorf("canonicalize capability manifest: %w", err)
	}
	signature, ok := raw.(map[string]any)
	if !ok {
		return true, false, "", fmt.Errorf("%s must be an object", capabilitySignatureField)
	}
	algorithm, _ := signature["algorithm"].(string)
	if strings.ToLower(strings.TrimSpace(algorithm)) != "ed25519" {
		return true, false, "", fmt.Errorf("%s.algorithm must be ed25519", capabilitySignatureField)
	}
	keyID, _ = signature["key_id"].(string)
	keyID = strings.TrimSpace(keyID)
	if keyID == "" || len([]rune(keyID)) > 120 {
		return true, false, keyID, fmt.Errorf("%s.key_id is invalid", capabilitySignatureField)
	}
	signatureText, _ := signature["signature"].(string)
	signatureText = strings.TrimSpace(signatureText)
	decoded, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(signatureText)
	}
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return true, false, keyID, fmt.Errorf("%s.signature must be a base64 Ed25519 signature", capabilitySignatureField)
	}
	publicKey, ok := trusted[keyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return true, false, keyID, nil
	}
	if !ed25519.Verify(publicKey, canonical, decoded) {
		return true, false, keyID, fmt.Errorf("capability signature verification failed for key %q", keyID)
	}
	return true, true, keyID, nil
}
