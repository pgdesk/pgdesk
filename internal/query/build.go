package query

import (
	"errors"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

var ErrNoColumns = errors.New("pgdesk/query: no columns supplied")

var ErrColumnValueMismatch = errors.New("pgdesk/query: column/value count mismatch")

type VersionStrategy struct {
	Column *introspect.Column

	NoVersion bool
}

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

	if len(keyCols) == 1 {

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

func writeColumnList(b *strings.Builder, cols []*introspect.Column) {
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(Ident(c.Name))
	}
}

func writeScope(b *strings.Builder, args *Args, scope []Filter) {
	for _, f := range scope {
		b.WriteString(" AND ")
		b.WriteString(emitFilter(args, f))
	}
}

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
