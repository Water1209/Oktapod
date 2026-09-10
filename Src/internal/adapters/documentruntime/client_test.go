package documentruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
)

func TestSuccessfulRuntimeHealthIsCachedAcrossBusyOperations(t *testing.T) {
	cached := application.DocumentRuntimeHealth{Available: true, Version: "1.0.0", Protocol: protocolVersion, Renderer: "test", OCRLanguages: []string{"chi_sim"}}
	client := &Client{healthCache: &cached}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := client.Health(ctx)
	if !result.Available || result.Version != "1.0.0" || result.Protocol != protocolVersion {
		t.Fatalf("cached health=%#v", result)
	}
	result.OCRLanguages[0] = "mutated"
	if client.Health(context.Background()).OCRLanguages[0] != "chi_sim" {
		t.Fatal("Health returned mutable cached slice")
	}
}

func TestSignedRuntimeManifestRejectsTamperingAndUnsafeHandles(t *testing.T) {
	packageRoot, workRoot := t.TempDir(), t.TempDir()
	pythonName := "python"
	if runtime.GOOS == "windows" {
		pythonName = "python.exe"
	}
	writeRuntimeFixture(t, packageRoot, pythonName, []byte("runtime-python"))
	writeRuntimeFixture(t, packageRoot, "runtime.py", []byte("print('runtime')\n"))
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeSignedManifest(t, packageRoot, privateKey, manifestPayload{
		SchemaVersion: "oktapod.document-runtime/v1", Version: "1.0.0", Protocol: protocolVersion,
		Platform: runtime.GOOS + "-" + runtime.GOARCH, Python: pythonName, Entrypoint: "runtime.py",
		Files: map[string]string{pythonName: mustHashFile(t, filepath.Join(packageRoot, pythonName)), "runtime.py": mustHashFile(t, filepath.Join(packageRoot, "runtime.py"))},
	})
	client, err := NewClient(packageRoot, workRoot, map[string]ed25519.PublicKey{"release-test": publicKey})
	if err != nil {
		t.Fatalf("open signed runtime: %v", err)
	}
	defer client.Close()
	for _, unsafe := range []string{"", "../escape", "/absolute", `C:\escape`} {
		if _, err := client.HandlePath(unsafe); err == nil {
			t.Fatalf("unsafe handle %q was accepted", unsafe)
		}
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "runtime.py"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(packageRoot, t.TempDir(), map[string]ed25519.PublicKey{"release-test": publicKey}); err == nil {
		t.Fatal("tampered signed runtime was accepted")
	}
}

func TestRuntimeManifestRequiresHashesForEntrypointAndPython(t *testing.T) {
	packageRoot := t.TempDir()
	writeRuntimeFixture(t, packageRoot, "python", []byte("python"))
	writeRuntimeFixture(t, packageRoot, "runtime.py", []byte("runtime"))
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeSignedManifest(t, packageRoot, privateKey, manifestPayload{
		SchemaVersion: "oktapod.document-runtime/v1", Version: "1.0.0", Protocol: protocolVersion,
		Platform: runtime.GOOS + "-" + runtime.GOARCH, Python: "python", Entrypoint: "runtime.py", Files: map[string]string{},
	})
	if _, err := NewClient(packageRoot, t.TempDir(), map[string]ed25519.PublicKey{"release-test": publicKey}); err == nil {
		t.Fatal("runtime manifest omitted hashes for executable files")
	}
}

func TestDevelopmentRuntimePassesOnlyExplicitRegularRendererExecutables(t *testing.T) {
	packageRoot, workRoot := t.TempDir(), t.TempDir()
	writeRuntimeFixture(t, packageRoot, "runtime.py", []byte("print('runtime')\n"))
	python, libreOffice, tesseract := filepath.Join(t.TempDir(), "python"), filepath.Join(t.TempDir(), "soffice"), filepath.Join(t.TempDir(), "tesseract")
	for _, executable := range []string{python, libreOffice, tesseract} {
		if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OKTAPOD_DOCUMENT_RUNTIME_PYTHON", python)
	t.Setenv("OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE", libreOffice)
	t.Setenv("OKTAPOD_DOCUMENT_RUNTIME_TESSERACT", tesseract)
	client, err := newDevelopmentClient(packageRoot, workRoot)
	if err != nil {
		t.Fatal(err)
	}
	environment := strings.Join(runtimeEnvironment(client, t.TempDir(), python), "\n")
	for _, expected := range []string{"OKTAPOD_LIBREOFFICE=" + libreOffice, "OKTAPOD_TESSERACT=" + tesseract} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("development Runtime environment omitted %q: %s", expected, environment)
		}
	}
}

func TestPackagedRuntimeUsesConsoleLibreOfficeOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific LibreOffice launcher behavior")
	}
	packageRoot := t.TempDir()
	executable := "libreoffice/program/soffice.exe"
	console := "libreoffice/program/soffice.com"
	writeRuntimeFixture(t, packageRoot, executable, []byte("gui launcher"))
	writeRuntimeFixture(t, packageRoot, console, []byte("console launcher"))
	client := &Client{
		packageRoot: packageRoot,
		manifest: manifestPayload{
			Version:     "1.0.0",
			LibreOffice: executable,
			Files:       map[string]string{console: mustHashFile(t, filepath.Join(packageRoot, filepath.FromSlash(console)))},
		},
	}
	path, ok := client.libreOfficeExecutable()
	if !ok || !strings.EqualFold(path, filepath.Join(packageRoot, filepath.FromSlash(console))) {
		t.Fatalf("libreOfficeExecutable() = %q, %v; want signed console launcher", path, ok)
	}
}

func writeRuntimeFixture(t *testing.T, root, name string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeSignedManifest(t *testing.T, root string, privateKey ed25519.PrivateKey, payload manifestPayload) {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signedManifest{SignedPayload: base64.StdEncoding.EncodeToString(payloadBytes)}
	envelope.Signature.Algorithm = "ed25519"
	envelope.Signature.KeyID = "release-test"
	envelope.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payloadBytes))
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime-manifest.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustHashFile(t *testing.T, path string) string {
	t.Helper()
	digest, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
