package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestDocumentCompletionRequiresEveryMutatedDocumentToHaveCurrentExport(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/document-completion.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE tool_calls (id TEXT PRIMARY KEY, run_id TEXT, status TEXT, tool_name TEXT, arguments_json TEXT, step_id TEXT)`,
		`CREATE TABLE side_effect_receipts (tool_call_id TEXT, status TEXT, output_json TEXT)`,
		`CREATE TABLE document_sessions (id TEXT PRIMARY KEY, revision INTEGER, rendered_revision INTEGER, render_hash TEXT)`,
		`CREATE TABLE artifacts (step_id TEXT, source_type TEXT, content_hash TEXT)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, row := range []struct{ id, documentID string }{{"create-1", "document-1"}, {"create-2", "document-2"}} {
		if _, err := tx.Exec(`INSERT INTO tool_calls VALUES (?, 'run-1', 'succeeded', ?, '{}', ?)`, row.id, core.ToolDocumentCreate, row.id+"-step"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO side_effect_receipts VALUES (?, 'succeeded', ?)`, row.id, `{"document_ref":"document:`+row.documentID+`"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO document_sessions VALUES (?, 1, 1, ?)`, row.documentID, "render-"+row.documentID); err != nil {
			t.Fatal(err)
		}
	}
	insertValidDocumentExport(t, tx, "export-1", "document-1")

	valid, err := validDocumentExportEvidence(context.Background(), tx, []string{"run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("one exported document incorrectly satisfied two mutated documents")
	}

	insertValidDocumentExport(t, tx, "export-2", "document-2")
	valid, err = validDocumentExportEvidence(context.Background(), tx, []string{"run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("current render and Artifact evidence for every mutated document was rejected")
	}
}

func insertValidDocumentExport(t *testing.T, tx *sql.Tx, callID, documentID string) {
	t.Helper()
	stepID := callID + "-step"
	renderHash := "render-" + documentID
	contentHash := "content-" + documentID
	arguments := `{"document_ref":"document:` + documentID + `","expected_revision":1}`
	output := `{"document_ref":"document:` + documentID + `","render_hash":"` + renderHash + `","content_hash":"` + contentHash + `"}`
	if _, err := tx.Exec(`INSERT INTO tool_calls VALUES (?, 'run-1', 'succeeded', ?, ?, ?)`, callID, core.ToolDocumentExport, arguments, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO side_effect_receipts VALUES (?, 'succeeded', ?)`, callID, output); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO artifacts VALUES (?, 'document_export', ?)`, stepID, contentHash); err != nil {
		t.Fatal(err)
	}
}
