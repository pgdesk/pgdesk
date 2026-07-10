package query

import (
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// InsertRow builds a parameterized INSERT with RETURNING (D3/D7). cols/vals are
// the columns to write and their typed values; returning are the columns to read
// back (so generated defaults and the new key are available without a
// read-after-write). When cols is empty the row is all-defaults.
func InsertRow(t *introspect.Table, cols []*introspect.Column, vals []any, returning []*introspect.Column) (string, []any, error) {
	if len(cols) != len(vals) {
		return "", nil, ErrColumnValueMismatch
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("INSERT INTO ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	if len(cols) == 0 {
		b.WriteString(" DEFAULT VALUES")
	} else {
		b.WriteString(" (")
		writeColumnList(&b, cols)
		b.WriteString(") VALUES (")
		for i := range cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(args.Add(vals[i]))
		}
		b.WriteString(")")
	}
	if len(returning) > 0 {
		b.WriteString(" RETURNING ")
		writeColumnList(&b, returning)
	}
	return b.String(), args.Values(), nil
}

// DeleteRow builds a parameterized DELETE by key (D3). keyCols/keyVals are the
// resource's key columns and decoded values. returning, when non-empty, reads the
// deleted row back (for the audit before-snapshot and 0-row detection).
//
// scope holds the principal's row constraints (O6), ANDed into the DELETE's own
// WHERE. DELETE carries no version token, so this is the only thing that makes a
// row-level delete rule race-free.
func DeleteRow(t *introspect.Table, keyCols []*introspect.Column, keyVals []any, returning []*introspect.Column, scope []Filter) (string, []any, error) {
	if len(keyCols) == 0 {
		return "", nil, ErrNoKey
	}
	if len(keyCols) != len(keyVals) {
		return "", nil, ErrKeyArity
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("DELETE FROM ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	b.WriteString(" WHERE ")
	writeKeyPredicate(&b, args, keyCols, keyVals)
	writeScope(&b, args, scope)
	if len(returning) > 0 {
		b.WriteString(" RETURNING ")
		writeColumnList(&b, returning)
	}
	return b.String(), args.Values(), nil
}
