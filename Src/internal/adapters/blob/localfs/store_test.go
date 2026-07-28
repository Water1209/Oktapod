package localfs

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestPutOpenDeduplicatesAndEnforcesLimits(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Put(t.Context(), strings.NewReader("hello"), "text/plain", 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(t.Context(), strings.NewReader("hello"), "text/plain", 5)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash || first.StorageKey != second.StorageKey {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	reader, err := store.Open(t.Context(), first.StorageKey, first.SizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || string(body) != "hello" {
		t.Fatalf("body=%q err=%v", body, readErr)
	}
	if _, err := store.Put(t.Context(), strings.NewReader("too large"), "text/plain", 3); err == nil {
		t.Fatal("oversized blob was accepted")
	} else {
		var validation *core.ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("oversized err=%T %v", err, err)
		}
	}
}

func TestOpenRejectsEscapingKeyAndSizeMismatch(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(t.Context(), "../secret", -1); err == nil {
		t.Fatal("escaping key was accepted")
	}
	blob, err := store.Put(t.Context(), strings.NewReader("hello"), "text/plain", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(t.Context(), blob.StorageKey, 99); err == nil {
		t.Fatal("size mismatch was accepted")
	}
}
