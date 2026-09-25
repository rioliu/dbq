package dbq_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rioliu/dbq/internal/dbq"
)

func sampleResult() *dbq.Result {
	return &dbq.Result{
		Columns: []string{"id", "name", "note"},
		Rows: [][]any{
			{int64(1), "alice", nil},
			{int64(2), "bob", "hi"},
		},
	}
}

func TestFormatJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := dbq.Format(&buf, "json", sampleResult()); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if got[0]["name"] != "alice" || got[1]["name"] != "bob" {
		t.Fatalf("wrong values: %+v", got)
	}
	if got[0]["note"] != nil {
		t.Fatalf("nil must serialize as null, got %v", got[0]["note"])
	}
	// numeric fidelity: json numbers, not strings
	if _, ok := got[0]["id"].(float64); !ok {
		t.Fatalf("id should be number, got %T", got[0]["id"])
	}
}

func TestFormatJSONPreservesColumnOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := dbq.Format(&buf, "json", sampleResult()); err != nil {
		t.Fatal(err)
	}
	firstRow := ""
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `"id":`) {
			firstRow = line
			break
		}
	}
	if firstRow == "" {
		t.Fatalf("no row line found: %s", buf.String())
	}
	if strings.Index(firstRow, `"id"`) > strings.Index(firstRow, `"name"`) {
		t.Fatalf("column order not preserved: %s", firstRow)
	}
}

func TestFormatTable(t *testing.T) {
	var buf bytes.Buffer
	if err := dbq.Format(&buf, "table", sampleResult()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 { // header + sep + 2 rows
		t.Fatalf("want 4 lines, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "id") || !strings.Contains(lines[0], "name") {
		t.Fatalf("bad header: %q", lines[0])
	}
	if !strings.Contains(lines[2], "alice") {
		t.Fatalf("missing row: %q", lines[2])
	}
	if !strings.Contains(lines[2], "NULL") {
		t.Fatalf("nil should render as NULL: %q", lines[2])
	}
}

func TestFormatCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := dbq.Format(&buf, "csv", sampleResult()); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("want header+2 rows, got %d", len(records))
	}
	if records[0][0] != "id" || records[1][1] != "alice" {
		t.Fatalf("wrong content: %v", records)
	}
	if records[1][2] != "" {
		t.Fatalf("nil should be empty in csv: %q", records[1][2])
	}
}

func TestFormatExecResult(t *testing.T) {
	var buf bytes.Buffer
	r := &dbq.Result{IsExec: true, Affected: 7}
	if err := dbq.Format(&buf, "json", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"affected":7`) {
		t.Fatalf("bad exec json: %s", buf.String())
	}
	buf.Reset()
	if err := dbq.Format(&buf, "table", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "7") {
		t.Fatalf("bad exec table: %s", buf.String())
	}
}

func TestFormatUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := dbq.Format(&buf, "xml", sampleResult()); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestNormalizeValue(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		in   any
		want any
	}{
		{[]byte("abc"), "abc"},
		{nil, nil},
		{int64(5), int64(5)},
		{"x", "x"},
	}
	for _, c := range cases {
		got := dbq.NormalizeValue(c.in)
		if got != c.want {
			t.Fatalf("NormalizeValue(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// time passes through
	if got := dbq.NormalizeValue(ts); got != ts {
		t.Fatalf("time mutated: %v", got)
	}
}
