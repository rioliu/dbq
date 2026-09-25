package dbq_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

// End-to-end tests against a real SQLite database (the only engine that
// needs no server). Engine-level read-only enforcement is exercised here.

func newSQLiteProfile(t *testing.T, rows int) dbq.Profile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := dbq.OpenRaw(dbq.DialectSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= rows; i++ {
		if _, err := db.Exec("INSERT INTO users(name) VALUES (?)", "user"+string(rune('a'+i-1))); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	return dbq.Profile{Type: "sqlite", Path: path}
}

func TestRunSQLiteSelect(t *testing.T) {
	p := newSQLiteProfile(t, 3)
	res, err := dbq.Run(context.Background(), p, "SELECT id, name FROM users ORDER BY id", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(res.Rows))
	}
	if res.Columns[0] != "id" || res.Columns[1] != "name" {
		t.Fatalf("bad columns: %v", res.Columns)
	}
	if res.Truncated {
		t.Fatal("3 rows with default limit must not truncate")
	}
}

func TestRunSQLiteLimitTruncates(t *testing.T) {
	p := newSQLiteProfile(t, 5)
	res, err := dbq.Run(context.Background(), p, "SELECT id FROM users ORDER BY id", 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(res.Rows))
	}
	if !res.Truncated {
		t.Fatal("expected truncation flag")
	}
}

func TestRunReadOnlyBlocksWriteAtGuard(t *testing.T) {
	p := newSQLiteProfile(t, 1)
	_, err := dbq.Run(context.Background(), p, "DELETE FROM users", 0)
	if err == nil {
		t.Fatal("DELETE must be rejected")
	}
	var ge *dbq.GuardError
	if !errors.As(err, &ge) {
		t.Fatalf("want GuardError, got %T: %v", err, err)
	}
}

// The engine layer must block writes even if the statement guard is bypassed.
func TestRunReadOnlyEngineLayerBlocksWrite(t *testing.T) {
	p := newSQLiteProfile(t, 1)
	// Simulate a guard bypass: exercise the engine-level setup directly.
	conn, teardown, err := dbq.OpenReadonly(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	if _, err := conn.ExecContext(context.Background(), "DELETE FROM users"); err == nil {
		t.Fatal("engine-level read-only must block DELETE")
	}
	// read still works
	var n int
	if err := conn.QueryRowContext(context.Background(), "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatalf("read should work under readonly: %v", err)
	}
	if n != 1 {
		t.Fatalf("row disappeared, n=%d", n)
	}
}

func TestRunWritableProfileExecutesWrite(t *testing.T) {
	p := newSQLiteProfile(t, 1)
	p.ReadOnly = boolPtr(false)
	res, err := dbq.Run(context.Background(), p, "UPDATE users SET name='changed'", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.IsExec || res.Affected != 1 {
		t.Fatalf("want exec with 1 affected, got %+v", res)
	}
}

func TestRunMultiStatementRejected(t *testing.T) {
	p := newSQLiteProfile(t, 1)
	p.ReadOnly = boolPtr(false) // even writable profiles: single statement only
	_, err := dbq.Run(context.Background(), p, "SELECT 1; DELETE FROM users", 0)
	if err == nil {
		t.Fatal("multi-statement must be rejected")
	}
	var ge *dbq.GuardError
	if !errors.As(err, &ge) {
		t.Fatalf("want GuardError, got %T: %v", err, err)
	}
}

func TestRunMissingProfileFields(t *testing.T) {
	p := dbq.Profile{Type: "mysql"} // no host
	if _, err := dbq.Run(context.Background(), p, "SELECT 1", 0); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestRunSchemaSQLite(t *testing.T) {
	p := newSQLiteProfile(t, 1)
	res, err := dbq.Schema(context.Background(), p, "")
	if err != nil {
		t.Fatalf("schema tables: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "users" {
		t.Fatalf("want users table, got %+v", res.Rows)
	}

	res, err = dbq.Schema(context.Background(), p, "users")
	if err != nil {
		t.Fatalf("schema columns: %v", err)
	}
	if len(res.Rows) < 2 {
		t.Fatalf("want columns, got %+v", res.Rows)
	}
	found := false
	for _, r := range res.Rows {
		if len(r) >= 2 && r[1] == "name" {
			found = true
		}
	}
	if !found {
		t.Fatalf("name column missing: %+v", res.Rows)
	}
}

func boolPtr(b bool) *bool { return &b }
