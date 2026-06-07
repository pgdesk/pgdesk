package query

import (
	"errors"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// The builders below emit parameterized SQL. Every identifier they write comes
// from a resolved *introspect.Column (catalog-owned Name), quoted with Ident;
// every value goes through *Args as $N (D3).

// ErrNoColumns is returned when a builder is handed an empty column set.
var ErrNoColumns = errors.New("pgdesk/query: no columns supplied")

// ErrColumnValueMismatch is returned when a builder's column and value counts
// differ.
var ErrColumnValueMismatch = errors.New("pgdesk/query: column/value count mismatch")

// VersionStrategy selects the optimistic-concurrency token used to guard updates
// against lost updates (O1). The zero value uses xmin, which needs no schema
// change. Set Column to use an explicit version/updated_at column instead.
type VersionStrategy struct {
	// Column, when non-nil, is an explicit version column (e.g. "version" or
	// "updated_at"). When nil, the system xmin column is used.
	Column *introspect.Column
}

// versionSelectExpr returns the SQL expression that yields the version token for
// SELECT, always as text so it can travel through a signed hidden form field.
func (v VersionStrategy) versionSelectExpr() string {
	if v.Column != nil {
		return Ident(v.Column.Name) + "::text"
	}
	return "xmin::text"
}

// versionPredicate appends the "AND <version> = $N" guard to an UPDATE and
// returns the fragment. token is the version value captured from the edit form.
func (v VersionStrategy) versionPredicate(args *Args, token string) string {
	if v.Column != nil {
		return Ident(v.Column.Name) + "::text = " + args.Add(token)
	}
	return "xmin::text = " + args.Add(token)
}

// SelectRow builds a single-row fetch by key for detail/edit. It selects the
// version token first (O1), then the requested columns. keyCols are the
// resource's key columns (which may differ from t.PrimaryKey for views, D6);
// keyVals must be decoded, typed values matching keyCols in order.
func SelectRow(t *introspect.Table, cols, keyCols []*introspect.Column, keyVals []any, ver VersionStrategy) (string, []any, error) {
	if len(cols) == 0 {
		return "", nil, ErrNoColumns
	}
	if len(keyCols) == 0 {
		return "", nil, ErrNoKey
	}
	if len(keyCols) != len(keyVals) {
		return "", nil, ErrKeyArity
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("SELECT ")
	b.WriteString(ver.versionSelectExpr())
	b.WriteString(" AS __pgdesk_version, ")
	writeColumnList(&b, cols)
	b.WriteString(" FROM ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	b.WriteString(" WHERE ")
	writeKeyPredicate(&b, args, keyCols, keyVals)
	return b.String(), args.Values(), nil
}

// UpdateRow builds an optimistic-concurrency UPDATE guarded by the version token
// (O1). setCols/setVals are the resolved columns and typed values to write;
// returning are the columns to read back with RETURNING (D7 — supplies
// generated defaults without a read-after-write). keyVals match t.PrimaryKey.
func UpdateRow(t *introspect.Table, setCols []*introspect.Column, setVals []any, keyCols []*introspect.Column, keyVals []any, versionToken string, ver VersionStrategy, returning []*introspect.Column) (string, []any, error) {
	if len(setCols) == 0 {
		return "", nil, ErrNoColumns
	}
	if len(setCols) != len(setVals) {
		return "", nil, ErrColumnValueMismatch
	}
	if len(keyCols) == 0 {
		return "", nil, ErrNoKey
	}
	if len(keyCols) != len(keyVals) {
		return "", nil, ErrKeyArity
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("UPDATE ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	b.WriteString(" SET ")
	for i, c := range setCols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(Ident(c.Name))
		b.WriteString(" = ")
		b.WriteString(args.Add(setVals[i]))
	}
	b.WriteString(" WHERE ")
	writeKeyPredicate(&b, args, keyCols, keyVals)
	b.WriteString(" AND ")
	b.WriteString(ver.versionPredicate(args, versionToken))
	if len(returning) > 0 {
		b.WriteString(" RETURNING ")
		writeColumnList(&b, returning)
	}
	return b.String(), args.Values(), nil
}

// writeColumnList writes a comma-separated list of quoted column identifiers.
func writeColumnList(b *strings.Builder, cols []*introspect.Column) {
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(Ident(c.Name))
	}
}

// writeKeyPredicate writes "pk1 = $a AND pk2 = $b ..." binding typed key values.
func writeKeyPredicate(b *strings.Builder, args *Args, pk []*introspect.Column, keyVals []any) {
	for i, c := range pk {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(Ident(c.Name))
		b.WriteString(" = ")
		b.WriteString(args.Add(keyVals[i]))
	}
}
