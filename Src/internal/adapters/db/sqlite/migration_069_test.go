package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	sqlitemigrations "github.com/oktapod/oktapod/Src/db/migrations/sqlite"
)

func TestProjectAdmissionMigrationPreservesConsumedHistoryAndFencesClaims(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "queue-upgrade.db"))+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// Only the referenced keys are needed to exercise the published queue
	// schemas; the full migration gate separately validates every table.
	if _, err := db.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY); CREATE TABLE conversations (id TEXT PRIMARY KEY); CREATE TABLE messages (id TEXT PRIMARY KEY); INSERT INTO tasks VALUES ('task'); INSERT INTO conversations VALUES ('conversation');`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000043_conversation_message_queue.up.sql", "000057_message_presentation_queue.up.sql"} {
		body, err := sqlitemigrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"consumed", "claimed"} {
		if _, err := db.Exec(`INSERT INTO conversation_message_queue (id, organization_id, project_id, conversation_id, active_task_id, content, kind, status, created_at, claimed_at, consumed_at, message_id) VALUES (?, 'org', 'project', 'conversation', 'task', 'historical intent', 'follow_up', ?, 'created', 'claimed-before-upgrade', 'consumed-before-upgrade', 'message-reference')`, state, state); err != nil {
			t.Fatal(err)
		}
	}
	body, err := sqlitemigrations.FS.ReadFile("000069_project_turn_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var state, claimed, consumed, message string
	if err := db.QueryRow(`SELECT status, claimed_at, consumed_at, message_id FROM conversation_message_queue WHERE id = 'consumed'`).Scan(&state, &claimed, &consumed, &message); err != nil {
		t.Fatal(err)
	}
	if state != "consumed" || claimed != "claimed-before-upgrade" || consumed != "consumed-before-upgrade" || message != "message-reference" {
		t.Fatalf("history changed: %q %q %q %q", state, claimed, consumed, message)
	}
	if err := db.QueryRow(`SELECT status, claimed_at FROM conversation_message_queue WHERE id = 'claimed'`).Scan(&state, &claimed); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || claimed != "" {
		t.Fatalf("old claim not reset: %q %q", state, claimed)
	}
	if _, err := db.Exec(`INSERT INTO conversation_message_queue (id, organization_id, project_id, conversation_id, content, created_at, status) VALUES ('first-turn', 'org', 'project', 'conversation', 'new intent', 'now', 'claimed')`); err != nil {
		t.Fatalf("first turn must allow no predecessor: %v", err)
	}
	if _, err := db.Exec(`UPDATE conversation_message_queue SET status = 'claimed' WHERE id = 'claimed'`); err == nil {
		t.Fatal("two project starters were allowed")
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke queue foreign keys")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
