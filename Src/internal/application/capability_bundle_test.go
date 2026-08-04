package application

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
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
		"unicode":   {"portable/SKILL.md": "x", "portable/references/café.md": "x", "portable/references/cafe\u0301.md": "y"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeCapabilityBundle(testCapabilityZip(t, files), "bad.zip", "skill", "bad", "0.1.0"); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestNormalizeCapabilityBundleRejectsInvalidSkillLayouts(t *testing.T) {
	validSkill := "---\nname: portable-skill\ndescription: Use for portable tests.\n---\n\nFollow the workflow.\n"
	for name, files := range map[string]map[string]string{
		"readme":       {"portable/SKILL.md": validSkill, "portable/README.md": "extra"},
		"nested-skill": {"portable/SKILL.md": validSkill, "portable/references/child/SKILL.md": validSkill},
		"dependencies": {"portable/SKILL.md": validSkill, "portable/scripts/node_modules/pkg/index.js": "x"},
		"mixed-roots":  {"portable/SKILL.md": validSkill, "other/references/guide.md": "x"},
		"hidden":       {"portable/SKILL.md": validSkill, "portable/assets/.payload": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeCapabilityBundle(testCapabilityZip(t, files), name+".zip", "skill", "portable-skill", "0.1.0"); err == nil {
				t.Fatal("invalid Skill ZIP was accepted")
			}
		})
	}
}

func TestNormalizeCapabilityBundleRejectsLinksAndCompressionBombs(t *testing.T) {
	validSkill := "---\nname: portable-skill\ndescription: Use for portable tests.\n---\n\nFollow the workflow.\n"
	var linkBuffer bytes.Buffer
	linkWriter := zip.NewWriter(&linkBuffer)
	skill, err := linkWriter.Create("portable/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = skill.Write([]byte(validSkill))
	header := &zip.FileHeader{Name: "portable/assets/link"}
	header.SetMode(os.ModeSymlink | 0o777)
	link, err := linkWriter.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = link.Write([]byte("target"))
	if err := linkWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeCapabilityBundle(base64.StdEncoding.EncodeToString(linkBuffer.Bytes()), "link.zip", "skill", "portable-skill", "0.1.0"); err == nil {
		t.Fatal("symbolic link was accepted")
	}

	bomb := testCapabilityZip(t, map[string]string{"portable/SKILL.md": validSkill, "portable/assets/repeated.txt": strings.Repeat("a", 2<<20)})
	if _, err := normalizeCapabilityBundle(bomb, "bomb.zip", "skill", "portable-skill", "0.1.0"); err == nil || !strings.Contains(err.Error(), "compression ratio") {
		t.Fatalf("compression bomb err=%v", err)
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
