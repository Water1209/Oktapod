package application

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestVerifyCapabilityManifestSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"description": "signed skill", "permissions": []string{"workspace.read"}}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"description": "signed skill",
		"permissions": []string{"workspace.read"},
		capabilitySignatureField: map[string]any{"algorithm": "ed25519", "key_id": "release-1", "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	present, verified, keyID, err := verifyCapabilityManifestSignature(string(manifestJSON), map[string]ed25519.PublicKey{"release-1": publicKey})
	if err != nil || !present || !verified || keyID != "release-1" {
		t.Fatalf("present=%v verified=%v key=%q err=%v", present, verified, keyID, err)
	}
	_, verified, _, err = verifyCapabilityManifestSignature(string(manifestJSON), map[string]ed25519.PublicKey{})
	if err != nil || verified {
		t.Fatalf("unknown key should be untrusted, verified=%v err=%v", verified, err)
	}
	manifest["description"] = "tampered"
	tampered, _ := json.Marshal(manifest)
	if _, _, _, err := verifyCapabilityManifestSignature(string(tampered), map[string]ed25519.PublicKey{"release-1": publicKey}); err == nil {
		t.Fatal("tampered capability signature was accepted")
	}
}
