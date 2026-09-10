package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	sqlitemigrations "github.com/oktapod/oktapod/Src/db/migrations/sqlite"
)

func TestConversationAdmissionMigrationScopesClaimsAndFencesRollback(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "conversation-claims.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE conversation_message_queue(id TEXT PRIMARY KEY, organization_id TEXT, project_id TEXT, conversation_id TEXT, status TEXT, kind TEXT, created_at TEXT, claimed_at TEXT, claim_version INTEGER);
CREATE UNIQUE INDEX idx_queue_project_claim ON conversation_message_queue(organization_id, project_id) WHERE status = 'claimed' AND kind = 'follow_up';
INSERT INTO conversation_message_queue VALUES ('a','org','project','thread-a','claimed','follow_up','1','1',1),('b','org','project','thread-b','pending','follow_up','2','',1),('c','org','project','thread-a','pending','follow_up','3','',1);`); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down"} {
		body, err := sqlitemigrations.FS.ReadFile("000070_conversation_turn_admission." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		if direction == "up" {
			if _, err := db.Exec(`UPDATE conversation_message_queue SET status='claimed' WHERE id='b'`); err != nil {
				t.Fatalf("different threads should claim independently: %v", err)
			}
			if _, err := db.Exec(`UPDATE conversation_message_queue SET status='claimed' WHERE id='c'`); err == nil {
				t.Fatal("same thread claimed twice")
			}
		} else {
			var retained, fenced int
			if err := db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN status='pending' AND claim_version=2 THEN 1 ELSE 0 END) FROM conversation_message_queue`).Scan(&retained, &fenced); err != nil {
				t.Fatal(err)
			}
			if retained != 3 || fenced != 2 {
				t.Fatalf("rollback lost intent or left stale claims: retained=%d fenced=%d", retained, fenced)
			}
		}
	}
}
