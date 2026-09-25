package dbq

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Result is the uniform query outcome.
type Result struct {
	Columns   []string
	Rows      [][]any
	Truncated bool
	IsExec    bool
	Affected  int64
	Warnings  []string
}

// NormalizeValue converts driver values into stable, portable forms:
// []byte -> string (JSON would base64 otherwise), everything else passes.
func NormalizeValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

func formatCell(v any, null string) string {
	switch t := v.(type) {
	case nil:
		return null
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case string:
		return strings.ReplaceAll(t, "\n", `\n`) // keep table rows on one line
	case []byte:
		return strings.ReplaceAll(string(t), "\n", `\n`)
	default:
		return fmt.Sprint(t)
	}
}

// Format renders r in the requested format: table | json | csv.
func Format(w io.Writer, format string, r *Result) error {
	switch format {
	case "json":
		return formatJSON(w, r)
	case "csv":
		return formatCSV(w, r)
	case "table":
		return formatTable(w, r)
	default:
		return fmt.Errorf("unknown format %q (want table|json|csv)", format)
	}
}

func formatJSON(w io.Writer, r *Result) error {
	if r.IsExec {
		_, err := fmt.Fprintf(w, `{"affected":%d}`+"\n", r.Affected)
		return err
	}
	// Build objects with column order preserved (map would sort keys).
	var b strings.Builder
	b.WriteString("[\n")
	for i, row := range r.Rows {
		b.WriteString("  {")
		for j, col := range r.Columns {
			if j > 0 {
				b.WriteString(",")
			}
			kb, err := json.Marshal(col)
			if err != nil {
				return err
			}
			vb, err := json.Marshal(NormalizeValue(row[j]))
			if err != nil {
				// Fallback: stringify anything JSON cannot represent.
				vb, _ = json.Marshal(fmt.Sprint(row[j]))
			}
			b.Write(kb)
			b.WriteString(":")
			b.Write(vb)
		}
		b.WriteString("}")
		if i < len(r.Rows)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func formatCSV(w io.Writer, r *Result) error {
	if r.IsExec {
		_, err := fmt.Fprintf(w, "affected\n%d\n", r.Affected)
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(r.Columns); err != nil {
		return err
	}
	for _, row := range r.Rows {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = formatCell(v, "")
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func formatTable(w io.Writer, r *Result) error {
	if r.IsExec {
		_, err := fmt.Fprintf(w, "OK, %d row(s) affected\n", r.Affected)
		return err
	}
	cols := make([]string, len(r.Columns))
	copy(cols, r.Columns)
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = utf8.RuneCountInString(c)
	}
	cells := make([][]string, len(r.Rows))
	for ri, row := range r.Rows {
		cells[ri] = make([]string, len(row))
		for ci, v := range row {
			s := formatCell(v, "NULL")
			cells[ri][ci] = s
			if n := utf8.RuneCountInString(s); n > widths[ci] {
				widths[ci] = n
			}
		}
	}

	writeRow := func(vals []string) {
		var b strings.Builder
		for i, v := range vals {
			b.WriteString(v)
			if i < len(vals)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(v)+2))
			}
		}
		b.WriteString("\n")
		io.WriteString(w, b.String())
	}

	writeRow(cols)
	sep := make([]string, len(cols))
	for i := range sep {
		sep[i] = strings.Repeat("-", widths[i])
	}
	writeRow(sep)
	for _, row := range cells {
		writeRow(row)
	}
	return nil
}
