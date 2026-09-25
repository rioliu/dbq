package dbq

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"  // postgres driver "pgx"
	_ "github.com/microsoft/go-mssqldb" // sqlserver driver "sqlserver"
	_ "github.com/sijms/go-ora/v2"      // oracle driver "oracle"
	_ "modernc.org/sqlite"              // sqlite driver "sqlite"
)

// OpenDB opens a pool for the profile.
func OpenDB(p Profile) (*sql.DB, error) {
	d, err := p.Dialect()
	if err != nil {
		return nil, err
	}
	pass, err := p.ResolvePassword()
	if err != nil {
		return nil, err
	}

	var driver, dsn string
	switch d {
	case DialectMySQL:
		cfg := mysqlConfigFor(p, pass)
		driver, dsn = "mysql", cfg.FormatDSN()
	case DialectPostgres:
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(p.User, pass),
			Host:   fmt.Sprintf("%s:%d", p.Host, orDefault(p.Port, 5432)),
			Path:   "/" + p.Database,
		}
		if p.SSLMode != "" {
			q := url.Values{}
			q.Set("sslmode", p.SSLMode)
			u.RawQuery = q.Encode()
		}
		driver, dsn = "pgx", u.String()
	case DialectSQLServer:
		u := url.URL{
			Scheme: "sqlserver",
			User:   url.UserPassword(p.User, pass),
			Host:   fmt.Sprintf("%s:%d", p.Host, orDefault(p.Port, 1433)),
		}
		q := url.Values{}
		q.Set("database", p.Database)
		u.RawQuery = q.Encode()
		driver, dsn = "sqlserver", u.String()
	case DialectOracle:
		u := url.URL{
			Scheme: "oracle",
			User:   url.UserPassword(p.User, pass),
			Host:   fmt.Sprintf("%s:%d", p.Host, orDefault(p.Port, 1521)),
		}
		if p.SID != "" {
			q := url.Values{}
			q.Set("SID", p.SID)
			u.RawQuery = q.Encode()
		} else {
			u.Path = "/" + p.Database
		}
		driver, dsn = "oracle", u.String()
	case DialectSQLite:
		driver, dsn = "sqlite", p.Path
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	return db, nil
}

// MySQLConfig exposes the driver config for a profile (used by tests).
func MySQLConfig(p Profile) (*mysql.Config, error) {
	pass, err := p.ResolvePassword()
	if err != nil {
		return nil, err
	}
	return mysqlConfigFor(p, pass), nil
}

// mysqlConfigFor builds on mysql.NewConfig() - NOT a struct literal - so
// driver defaults apply (AllowNativePasswords=true, Loc=UTC, liveness check).
// A manual &mysql.Config{...} leaves AllowNativePasswords=false and the
// handshake fails with "this user requires mysql native password
// authentication" for any account using the mysql_native_password plugin.
func mysqlConfigFor(p Profile, pass string) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User = p.User
	cfg.Passwd = pass
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", p.Host, orDefault(p.Port, 3306))
	cfg.DBName = p.Database
	cfg.ParseTime = true
	cfg.Timeout = time.Duration(p.ResolvedTimeoutSeconds()) * time.Second // dial timeout
	return cfg
}

// OpenRaw opens a pool directly from dialect + target (sqlite tests).
func OpenRaw(d Dialect, target string) (*sql.DB, error) {
	if d != DialectSQLite {
		return nil, fmt.Errorf("OpenRaw supports sqlite only, got %s", d)
	}
	return sql.Open("sqlite", target)
}

// OpenReadonly returns a dedicated connection prepared so that the DATABASE
// itself rejects writes, even if the statement guard is bypassed:
//
//	postgres  : BEGIN TRANSACTION READ ONLY
//	mysql     : START TRANSACTION READ ONLY
//	oracle    : SET TRANSACTION READ ONLY
//	sqlite    : PRAGMA query_only = ON
//	sqlserver : no engine-level equivalent; guard only (caller warns)
//
// teardown rolls back / resets and returns the connection.
func OpenReadonly(ctx context.Context, p Profile) (*sql.Conn, func() error, error) {
	d, err := p.Dialect()
	if err != nil {
		return nil, nil, err
	}
	db, err := OpenDB(p)
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	teardown := func() error {
		var firstErr error
		switch d {
		case DialectPostgres, DialectMySQL, DialectOracle:
			if _, e := conn.ExecContext(context.Background(), "ROLLBACK"); e != nil {
				firstErr = e
			}
		case DialectSQLite:
			if _, e := conn.ExecContext(context.Background(), "PRAGMA query_only = OFF"); e != nil && firstErr == nil {
				firstErr = e
			}
		}
		if e := conn.Close(); e != nil && firstErr == nil {
			firstErr = e
		}
		if e := db.Close(); e != nil && firstErr == nil {
			firstErr = e
		}
		return firstErr
	}

	setup := map[Dialect]string{
		DialectPostgres: "BEGIN TRANSACTION READ ONLY",
		DialectMySQL:    "START TRANSACTION READ ONLY",
		DialectOracle:   "SET TRANSACTION READ ONLY",
		DialectSQLite:   "PRAGMA query_only = ON",
	}[d]
	if setup != "" {
		if _, err := conn.ExecContext(ctx, setup); err != nil {
			teardown()
			return nil, nil, fmt.Errorf("enable engine read-only: %w", err)
		}
	}
	if d == DialectSQLServer {
		// No engine-level read-only; surface this every time (documented in README).
		return conn, teardown, nil
	}
	return conn, teardown, nil
}

// Run validates and executes one statement against a profile.
func Run(ctx context.Context, p Profile, statement string, limit int) (*Result, error) {
	return runWithArgs(ctx, p, statement, nil, limit)
}

func runWithArgs(ctx context.Context, p Profile, statement string, args []any, limit int) (*Result, error) {
	d, err := p.Dialect()
	if err != nil {
		return nil, err
	}
	readOnly := p.ResolvedReadOnly()
	if err := Validate(d, statement, readOnly); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = p.ResolvedMaxRows()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.ResolvedTimeoutSeconds())*time.Second)
	defer cancel()

	res := &Result{}
	if d == DialectSQLServer && readOnly {
		res.Warnings = append(res.Warnings,
			"sqlserver: no engine-level read-only transaction; write protection relies on the statement guard")
	}

	if !IsQueryKeyword(statement) {
		// Non-SELECT on a writable profile: run as exec (guard already
		// rejected this on read-only profiles).
		db, err := OpenDB(p)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		out, err := db.ExecContext(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		res.IsExec = true
		res.Affected, _ = out.RowsAffected()
		return res, nil
	}

	runQuery := func(q queryer) (*Result, error) {
		rows, err := q.QueryContext(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		res.Columns = cols
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if len(res.Rows) >= limit {
				res.Truncated = true
				break
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			row := make([]any, len(cols))
			for i, v := range vals {
				row[i] = NormalizeValue(v)
			}
			res.Rows = append(res.Rows, row)
		}
		return res, rows.Err()
	}

	if readOnly {
		conn, teardown, err := OpenReadonly(ctx, p)
		if err != nil {
			return nil, err
		}
		defer teardown()
		return runQuery(conn)
	}

	db, err := OpenDB(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return runQuery(db)
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Ping verifies connectivity.
func Ping(ctx context.Context, p Profile) error {
	db, err := OpenDB(p)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.ResolvedTimeoutSeconds())*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// Schema lists tables (table == "") or columns of one table.
// Portable across all five engines: information_schema where available,
// sqlite_master / user_tab_columns otherwise.
func Schema(ctx context.Context, p Profile, table string) (*Result, error) {
	d, err := p.Dialect()
	if err != nil {
		return nil, err
	}

	if table == "" {
		var q string
		switch d {
		case DialectMySQL:
			q = `SELECT table_name FROM information_schema.tables
			      WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
			      ORDER BY table_name`
		case DialectPostgres:
			q = `SELECT table_name FROM information_schema.tables
			      WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
			      ORDER BY table_name`
		case DialectSQLServer:
			q = `SELECT table_name FROM information_schema.tables
			      WHERE table_type = 'BASE TABLE' ORDER BY table_name`
		case DialectSQLite:
			q = `SELECT name AS table_name FROM sqlite_master
			      WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
			      ORDER BY name`
		case DialectOracle:
			q = `SELECT table_name FROM user_tables ORDER BY table_name`
		}
		return Run(ctx, p, q, 0)
	}

	if err := validateIdent(table); err != nil {
		return nil, err
	}
	// Lower-case match for engines storing unquoted identifiers lower/upper.
	like := strings.ToLower(table)
	var q string
	var args []any
	switch d {
	case DialectMySQL:
		q = `SELECT table_name, column_name, data_type, is_nullable
		     FROM information_schema.columns
		     WHERE table_schema = DATABASE() AND lower(table_name) = ?
		     ORDER BY ordinal_position`
		args = []any{like}
	case DialectPostgres:
		q = `SELECT table_name, column_name, data_type, is_nullable
		     FROM information_schema.columns
		     WHERE table_schema = current_schema() AND lower(table_name) = $1
		     ORDER BY ordinal_position`
		args = []any{like}
	case DialectSQLServer:
		q = `SELECT table_name, column_name, data_type, is_nullable
		     FROM information_schema.columns
		     WHERE lower(table_name) = @p1 ORDER BY ordinal_position`
		args = []any{like}
	case DialectSQLite:
		q = `SELECT name, type, "notnull" FROM pragma_table_info(?) ORDER BY cid`
		rows, err := pragmaColumns(ctx, p, table)
		if err != nil {
			return nil, err
		}
		return rows, nil
	case DialectOracle:
		q = fmt.Sprintf(`SELECT table_name, column_name, data_type, nullable
		     FROM user_tab_columns WHERE lower(table_name) = '%s'
		     ORDER BY column_id`, like) // table validated by identPattern
	}
	return runWithArgs(ctx, p, q, args, 0)
}

func pragmaColumns(ctx context.Context, p Profile, table string) (*Result, error) {
	rows, err := runWithArgs(ctx, p, `SELECT name, type, "notnull" FROM pragma_table_info(?) ORDER BY cid`,
		[]any{table}, 0)
	if err != nil {
		return nil, err
	}
	// Wrap into the uniform 4-column shape: table, column, type, nullable.
	out := &Result{
		Columns: []string{"table_name", "column_name", "data_type", "is_nullable"},
	}
	for _, r := range rows.Rows {
		nullable := "YES"
		if n, ok := r[2].(int64); ok && n != 0 {
			nullable = "NO"
		}
		out.Rows = append(out.Rows, []any{table, r[0], r[1], nullable})
	}
	return out, nil
}
