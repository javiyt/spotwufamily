// Package maintenance holds shared building blocks for admin maintenance
// workflows: deterministic reports and safe error mapping.
package maintenance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// SortRows orders rows lexicographically by all columns.
func SortRows(r *jobs.Report) {
	slices.SortStableFunc(r.Rows, slices.Compare[[]string])
}

func sortedCopy(r jobs.Report) jobs.Report {
	out := r
	out.Rows = slices.Clone(r.Rows)
	SortRows(&out)
	return out
}

// formulaStarters are the leading characters spreadsheets may treat as the
// start of a formula (OWASP CSV injection), including their full-width forms.
const formulaStarters = "=+-@\t\r\uFF1D\uFF0B\uFF0D\uFF20"

func guard(cell string) string {
	first, _ := utf8.DecodeRuneInString(cell)
	if cell != "" && strings.ContainsRune(formulaStarters, first) {
		return "'" + cell
	}
	return cell
}

// WriteCSV writes the report as CSV with sorted rows and formula-injection
// protection. The caller's report is not modified.
func WriteCSV(w io.Writer, r jobs.Report) error {
	r = sortedCopy(r)
	cw := csv.NewWriter(w)
	header := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		header[i] = guard(c)
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, row := range r.Rows {
		rec := make([]string, len(row))
		for i, c := range row {
			rec[i] = guard(c)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteJSON writes {name, columns, rows:[{col:val}]} with keys in column
// order, two-space indent and a trailing newline. Rows are sorted.
func WriteJSON(w io.Writer, r jobs.Report) error {
	r = sortedCopy(r)
	var buf bytes.Buffer
	enc := func(v any) error {
		b, err := json.Marshal(v)
		buf.Write(b)
		return err
	}
	cols := r.Columns
	if cols == nil {
		cols = []string{}
	}
	buf.WriteString(`{"name":`)
	if err := enc(r.Name); err != nil {
		return err
	}
	buf.WriteString(`,"columns":`)
	if err := enc(cols); err != nil {
		return err
	}
	buf.WriteString(`,"rows":[`)
	for i, row := range r.Rows {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('{')
		for j, col := range cols {
			if j > 0 {
				buf.WriteByte(',')
			}
			if err := enc(col); err != nil {
				return err
			}
			buf.WriteByte(':')
			val := ""
			if j < len(row) {
				val = row[j]
			}
			if err := enc(val); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	}
	buf.WriteString("]}")
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := w.Write(out.Bytes())
	return err
}
