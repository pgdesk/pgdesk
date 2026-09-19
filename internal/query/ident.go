package query

import (
	"errors"
	"strconv"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

var ErrUnknownColumn = errors.New("pgdesk/query: identifier does not resolve to a catalog column")

func Ident(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func QualifyIdent(schema, name string) string {
	return Ident(schema) + "." + Ident(name)
}

func ResolveColumn(t *introspect.Table, name string) (*introspect.Column, error) {
	if t == nil {
		return nil, ErrUnknownColumn
	}
	if c, ok := t.Column(name); ok {
		return c, nil
	}
	return nil, ErrUnknownColumn
}

type Args struct {
	vals []any
}

func (a *Args) Add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

func (a *Args) Values() []any { return a.vals }

func (a *Args) Len() int { return len(a.vals) }
