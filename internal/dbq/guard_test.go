package dbq_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

func TestValidateReadOnlyAccepts(t *testing.T) {
	cases := []struct {
		name    string
		dialect dbq.Dialect
		sql     string
	}{
		{"plain select", dbq.DialectMySQL, "SELECT id, name FROM users"},
		{"trailing semicolon", dbq.DialectMySQL, "SELECT 1;"},
		{"trailing semicolon + space", dbq.DialectPostgres, "SELECT 1 ;  "},
		{"leading line comment", dbq.DialectPostgres, "-- fetch users\nSELECT * FROM users"},
		{"leading block comment", dbq.DialectSQLite, "/* hint */ SELECT 1"},
		{"semicolon inside string", dbq.DialectMySQL, "SELECT 'a;b' FROM t"},
		{"semicolon inside identifier", dbq.DialectMySQL, "SELECT `weird;col` FROM t"},
		{"comment hides semicolon", dbq.DialectPostgres, "SELECT 1 /* ; */ + 1"},
		{"cte select", dbq.DialectPostgres, "WITH x AS (SELECT 1) SELECT * FROM x"},
		{"explain", dbq.DialectMySQL, "EXPLAIN SELECT * FROM users"},
		{"show", dbq.DialectMySQL, "SHOW TABLES"},
		{"describe", dbq.DialectMySQL, "DESCRIBE users"},
		{"pragma", dbq.DialectSQLite, "PRAGMA table_info(users)"},
		{"values", dbq.DialectPostgres, "VALUES (1, 2)"},
		{"information_schema query", dbq.DialectSQLServer, "SELECT name FROM sys.tables"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := dbq.Validate(c.dialect, c.sql, true); err != nil {
				t.Fatalf("expected accept, got: %v", err)
			}
		})
	}
}

func TestValidateReadOnlyRejects(t *testing.T) {
	cases := []struct {
		name    string
		dialect dbq.Dialect
		sql     string
		want    string
	}{
		{"empty", dbq.DialectMySQL, "", "empty"},
		{"only comment", dbq.DialectMySQL, "-- nothing", "empty"},
		{"delete", dbq.DialectMySQL, "DELETE FROM users", "not allowed"},
		{"update", dbq.DialectPostgres, "UPDATE users SET name='x'", "not allowed"},
		{"insert", dbq.DialectPostgres, "INSERT INTO t VALUES (1)", "not allowed"},
		{"drop", dbq.DialectMySQL, "DROP TABLE users", "not allowed"},
		{"multi-statement", dbq.DialectMySQL, "SELECT 1; DELETE FROM users", "multiple statements"},
		{"double semicolon", dbq.DialectMySQL, "SELECT 1;;", "multiple statements"},
		{"comment then statement", dbq.DialectMySQL, "SELECT 1; -- x\nDELETE FROM t", "multiple statements"},
		{"string escape smuggle", dbq.DialectMySQL, "SELECT 'a'; DROP TABLE t; -- '", "multiple statements"},
		{"write hidden in comment-position", dbq.DialectMySQL, "/* SELECT */ TRUNCATE TABLE t", "not allowed"},
		{"with write cte", dbq.DialectPostgres, "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", "DML/DDL keyword"},
		{"select hiding delete word", dbq.DialectMySQL, "SELECT * FROM (DELETE OUTPUT 1) x", "DML/DDL keyword"},
		{"show create table stays readable", dbq.DialectMySQL, "SHOW CREATE TABLE t", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := dbq.Validate(c.dialect, c.sql, true)
			if c.want == "" {
				if err != nil {
					t.Fatalf("expected accept, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
			var ge *dbq.GuardError
			if !errors.As(err, &ge) {
				t.Fatalf("expected GuardError, got %T: %v", err, err)
			}
		})
	}
}

// SQL Server has no engine-level read-only transaction, so the guard scans
// the whole statement for DML/DDL keywords (CTE data-modifying statements).
func TestValidateSQLServerReadOnlyScan(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		ok   bool
	}{
		{"plain cte select", "WITH x AS (SELECT 1) SELECT * FROM x", true},
		{"select with column named deleted_at", "SELECT deleted_at FROM t", true},
		{"dml cte", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", false},
		{"delete anywhere", "SELECT * FROM (DELETE OUTPUT 1) x", false},
		{"exec", "EXEC sp_who", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := dbq.Validate(dbq.DialectSQLServer, c.sql, true)
			if c.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("expected reject, got nil")
			}
		})
	}
}

// Non-readonly profiles still enforce a single statement.
func TestValidateWritableSingleStatementOnly(t *testing.T) {
	if err := dbq.Validate(dbq.DialectMySQL, "DELETE FROM users", false); err != nil {
		t.Fatalf("writable profile should allow DELETE, got %v", err)
	}
	if err := dbq.Validate(dbq.DialectMySQL, "DELETE FROM users; DROP TABLE x", false); err == nil {
		t.Fatal("writable profile must still reject multi-statement")
	}
}
