package cursor

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPatchCursorStatsigGatesDoesNotRewriteAuth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	dbPath := filepath.Join(root, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}

	originalToken := "user-real-token"
	bootstrap, err := json.Marshal(map[string]any{
		"feature_gates": map[string]any{
			"decompose_always_local_ext_host": map[string]any{"value": true, "name": "decompose_always_local_ext_host"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "cursorAuth/accessToken", []byte(originalToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, cursorStateStatsigBootstrapKey, bootstrap); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := PatchCursorStatsigGates(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var token []byte
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "cursorAuth/accessToken").Scan(&token); err != nil {
		t.Fatal(err)
	}
	if string(token) != originalToken {
		t.Fatalf("auth rewritten: %q", token)
	}
	var updated []byte
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, cursorStateStatsigBootstrapKey).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(updated, &payload); err != nil {
		t.Fatal(err)
	}
	gates, _ := payload["feature_gates"].(map[string]any)
	gate, _ := gates["decompose_always_local_ext_host"].(map[string]any)
	if value, _ := gate["value"].(bool); value {
		t.Fatal("expected decompose gate disabled")
	}
}
