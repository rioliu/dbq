package dbq_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

// Generic engine-level read-only integration test (layer 2 - the part that
// holds even if the statement guard is bypassed). Opt-in:
//
//	DBQ_IT=1 DBQ_IT_PROFILE=<name> go test ./internal/dbq/ -run ReadonlyEngine -v
//
// The probe is DELETE ... WHERE 1=0: the engine must reject it inside its
// read-only transaction, and even if the layer were broken it would affect
// zero rows.
//
// Profiles for this test: mysql_local, pg_test (container), demo (sqlite).
// sqlserver is skipped - it has no engine-level read-only (documented).
func TestReadonlyEngineIntegration(t *testing.T) {
	if os.Getenv("DBQ_IT") == "" {
		t.Skip("integration test: run with DBQ_IT=1 and DBQ_IT_PROFILE=<name>")
	}
	name := os.Getenv("DBQ_IT_PROFILE")
	if name == "" {
		t.Skip("DBQ_IT_PROFILE not set")
	}

	path, err := dbq.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	// Allow the temp-file workflow: DBQ_PROFILES wins inside DefaultPath.
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		t.Skipf("profile %q not found", name)
	}
	if !p.ResolvedReadOnly() {
		t.Fatalf("profile %q must be read-only for this test", name)
	}
	d, err := p.Dialect()
	if err != nil {
		t.Fatal(err)
	}
	if d == dbq.DialectSQLServer {
		t.Skip("sqlserver has no engine-level read-only transaction (documented)")
	}

	// Discover a real table to probe against.
	res, err := dbq.Schema(context.Background(), p, "")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Skip("no tables in database")
	}
	table := fmt.Sprint(res.Rows[0][0])
	ident := quoteIdent(d, table)

	ctx := context.Background()
	conn, teardown, err := dbq.OpenReadonly(ctx, p)
	if err != nil {
		t.Fatalf("open readonly: %v", err)
	}
	defer teardown()

	// Read first: PostgreSQL aborts the transaction after any error, so the
	// read must precede the write probe to stay dialect-agnostic.
	var one int
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("read under readonly failed: %v (one=%d)", err, one)
	}

	_, err = conn.ExecContext(ctx, "DELETE FROM "+ident+" WHERE 1=0")
	if err == nil {
		t.Fatal("engine read-only FAILED: DELETE was accepted")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "read-only") &&
		!strings.Contains(msg, "read only") &&
		!strings.Contains(msg, "readonly") {
		t.Fatalf("rejected, but not by the read-only mechanism: %v", err)
	}
	t.Logf("engine rejected write as expected: %v", err)
}

func quoteIdent(d dbq.Dialect, name string) string {
	if d == dbq.DialectMySQL {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}
