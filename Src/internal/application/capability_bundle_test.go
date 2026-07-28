package application

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNormalizeCapabilityBundleImportsPortableSkill(t *testing.T) {
	encoded := testCapabilityZip(t, map[string]string{
		"portable/SKILL.md":            "---\nname: portable-skill\ndescription: Use for portable tests.\n---\n\nFollow the bounded workflow.\n",
		"portable/references/guide.md": "reference",
	})
	bundle, err := normalizeCapabilityBundle(encoded, "portable.zip", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Kind != "skill" || bundle.Name != "portable-skill" || bundle.Version != "0.1.0" {
		t.Fatalf("bundle=%#v", bundle)
	}
	if !strings.Contains(bundle.ManifestJSON, `"_oktapod_bundle"`) || !strings.HasPrefix(bundle.Hash, "sha256:") {
		t.Fatalf("manifest=%s hash=%s", bundle.ManifestJSON, bundle.Hash)
	}
}

func TestNormalizeCapabilityBundleRejectsUnsafePathsAndBombRatios(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"traversal": {"../SKILL.md": "---\nname: bad\ndescription: bad\n---\nbad"},
		"drive":     {"C:/SKILL.md": "---\nname: bad\ndescription: bad\n---\nbad"},
		"collision": {"A/SKILL.md": "x", "a/skill.md": "y"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeCapabilityBundle(testCapabilityZip(t, files), "bad.zip", "skill", "bad", "0.1.0"); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func testCapabilityZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}
