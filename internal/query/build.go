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
// against lost updates (O1). It has three modes:
//
//   - Column != nil: use an explicit version/updated_at column.
//   - Column == nil && !NoVersion: use the system xmin column (the default for
//     ordinary and partitioned tables, which needs no schema change).
//   - NoVersion: emit no version token at all. This is used for views and foreign
//     tables, which have no selectable xmin -- selecting it there errors. Such a
//     relation relies on the key+scope predicate alone to identify the row; a
//     zero-row update is disambiguated as out-of-scope (404) via ExistsRow, so
//     there are no false conflicts, at the cost of no lost-update protection.
//     Declare a version column with Resource.VersionColumn to restore it.
type VersionStrategy struct {
	// Column, when non-nil, is an explicit version column (e.g. "version" or
	// "updated_at").
	Column *introspect.Column
	// NoVersion, when true, disables optimistic-concurrency versioning entirely.
	// It takes precedence only when Column is nil.
	NoVersion bool
}

// versionSelectExpr returns the SQL expression that yields the version token for
// SELECT, always as text so it can travel through a signed hidden form field. In
// NoVersion mode it selects a NULL placeholder so the row shape stays uniform and
// the caller reads an empty token.
func (v VersionStrategy) versionSelectExpr() string {
	switch {
	case v.Column != nil:
		return Ident(v.Column.Name) + "::text"
	case v.NoVersion:
		return "NULL::text"
	default:
		return "xmin::text"
	}
}

// versionPredicate returns the "<version> = $N" guard fragment for an UPDATE, or
// an empty string in NoVersion mode (the caller then omits the guard). token is
// the version value captured from the edit form.
func (v VersionStrategy) versionPredicate(args *Args, token string) string {
	switch {
	case v.Column != nil:
		return Ident(v.Column.Name) + "::text = " + args.Add(token)
	case v.NoVersion:
		return ""
	default:
		return "xmin::text = " + args.Add(token)
	}
}

// SelectRow builds a single-row fetch by key for detail/edit. It selects the
// version token first (O1), then the requested columns. keyCols are the
// resource's key columns (which may differ from t.PrimaryKey for views, D6);
// keyVals must be decoded, typed values matching keyCols in order.
//
// scope holds the principal's row constraints (O6). They are ANDed into the
// WHERE, so a row outside the principal's scope simply does not exist: the fetch
// returns no rows and the caller renders 404, never 403.
func SelectRow(t *introspect.Table, cols, keyCols []*introspect.Column, keyVals []any, ver VersionStrategy, scope []Filter) (string, []any, error) {
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
	writeScope(&b, args, scope)
	return b.String(), args.Values(), nil
}

// ExistsRow builds "SELECT 1 FROM t WHERE key ... [AND scope]" -- the probe that
// tells an update or delete affecting zero rows apart from one the principal may
// not reach. It carries no version predicate on purpose: with the version guard
// removed, a row that still does not appear is out of scope (404), and a row that
// does appear was changed underneath us (409).
func ExistsRow(t *introspect.Table, keyCols []*introspect.Column, keyVals []any, scope []Filter) (string, []any, error) {
	if len(keyCols) == 0 {
		return "", nil, ErrNoKey
	}
	if len(keyCols) != len(keyVals) {
		return "", nil, ErrKeyArity
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("SELECT 1 FROM ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	b.WriteString(" WHERE ")
	writeKeyPredicate(&b, args, keyCols, keyVals)
	writeScope(&b, args, scope)
	return b.String(), args.Values(), nil
}

// CountRowsInScope builds "SELECT count(*) FROM t WHERE (key1 OR key2 ...) [AND
// scope]" for a bulk action's selected keys.
//
// A bulk action runs host-authored SQL that pgdesk cannot rewrite, so the keys
// must be vetted before the action sees them. The caller compares the count with
// the number of distinct keys submitted and refuses the whole action on any
// shortfall: a key outside the principal's scope, a key that does not exist, and
// a duplicated key all fail closed. Running it inside the action's transaction
// makes the check and the action consistent.
func CountRowsInScope(t *introspect.Table, keyCols []*introspect.Column, keys [][]any, scope []Filter) (string, []any, error) {
	if len(keyCols) == 0 {
		return "", nil, ErrNoKey
	}
	if len(keys) == 0 {
		return "", nil, ErrKeyArity
	}
	for _, kv := range keys {
		if len(kv) != len(keyCols) {
			return "", nil, ErrKeyArity
		}
	}
	var b strings.Builder
	args := &Args{}

	// The caller compares this count with the number of keys submitted, so the
	// count must be of DISTINCT key tuples: a duplicated key, or a non-unique
	// declared key on a view (D6), must not inflate the count into a false pass.
	if len(keyCols) == 1 {
		// Single-column key: a bounded IN list of individual $N placeholders.
		// Preferred over "= ANY($1)" because a []any array argument fails to encode
		// when pgx runs without a describe step (PgBouncer transaction pooling); an
		// IN list encodes in every mode and the planner treats it as a ScalarArrayOp.
		col := Ident(keyCols[0].Name)
		b.WriteString("SELECT count(DISTINCT ")
		b.WriteString(col)
		b.WriteString(") FROM ")
		b.WriteString(QualifyIdent(t.Schema, t.Name))
		b.WriteString(" WHERE ")
		b.WriteString(col)
		b.WriteString(" IN (")
		for i, kv := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(args.Add(kv[0]))
		}
		b.WriteString(")")
		writeScope(&b, args, scope)
		return b.String(), args.Values(), nil
	}

	// Composite key: OR of per-key AND-groups. Composite keys are primary keys and
	// therefore unique, so count(*) counts distinct tuples.
	b.WriteString("SELECT count(*) FROM ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))
	b.WriteString(" WHERE (")
	for i, kv := range keys {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(")
		writeKeyPredicate(&b, args, keyCols, kv)
		b.WriteString(")")
	}
	b.WriteString(")")
	writeScope(&b, args, scope)
	return b.String(), args.Values(), nil
}

// UpdateRow builds an optimistic-concurrency UPDATE guarded by the version token
// (O1). setCols/setVals are the resolved columns and typed values to write;
// returning are the columns to read back with RETURNING (D7 -- supplies
// generated defaults without a read-after-write). keyVals match t.PrimaryKey.
//
// scope holds the principal's row constraints (O6), ANDed into the WHERE of the
// statement that performs the write. The authorization predicate and the mutation
// are therefore one atomic operation: nothing can falsify it in between.
func UpdateRow(t *introspect.Table, setCols []*introspect.Column, setVals []any, keyCols []*introspect.Column, keyVals []any, versionToken string, ver VersionStrategy, returning []*introspect.Column, scope []Filter) (string, []any, error) {
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
	if pred := ver.versionPredicate(args, versionToken); pred != "" {
		b.WriteString(" AND ")
		b.WriteString(pred)
	}
	writeScope(&b, args, scope)
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

// writeScope appends " AND <predicate>" for each row constraint. The constraints
// were resolved against the catalog, so every identifier is catalog-owned and
// every value becomes $N (D3).
func writeScope(b *strings.Builder, args *Args, scope []Filter) {
	for _, f := range scope {
		b.WriteString(" AND ")
		b.WriteString(emitFilter(args, f))
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
