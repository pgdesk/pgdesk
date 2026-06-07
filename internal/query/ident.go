// Package query builds parameterized PostgreSQL statements for pgdesk under one
// invariant (D3): no request-supplied string is ever concatenated into SQL.
//
//   - Values become $N placeholders via *Args. Callers never format $N by hand.
//   - Identifiers are resolved through the catalog (ResolveColumn) and quoted
//     with Ident at emit time as defense-in-depth. A request string is only ever
//     a map key; if it doesn't resolve to a *introspect.Column it is rejected,
//     never emitted.
package query

import (
	"errors"
	"strconv"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// ErrUnknownColumn is returned when a request identifier does not resolve to a
// real column of the target relation. Callers translate it to HTTP 400 and fail
// closed (D3).
var ErrUnknownColumn = errors.New("pgdesk/query: identifier does not resolve to a catalog column")

// Ident quotes a PostgreSQL identifier by doubling embedded double-quotes and
// wrapping in double-quotes. It is applied only to names that already came from
// pg_catalog (via ResolveColumn); it is a defense-in-depth second layer, not the
// primary safety mechanism.
//
//	Ident(`user"; drop`) == `"user""; drop"`
func Ident(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QualifyIdent quotes a schema-qualified relation name: "schema"."table".
func QualifyIdent(schema, name string) string {
	return Ident(schema) + "." + Ident(name)
}

// ResolveColumn is THE identifier gate. It uses name only as a map key against
// the immutable catalog and returns the catalog-owned *Column, or ErrUnknownColumn.
// The returned column's Name is the only identifier text that may reach SQL.
func ResolveColumn(t *introspect.Table, name string) (*introspect.Column, error) {
	if t == nil {
		return nil, ErrUnknownColumn
	}
	if c, ok := t.Column(name); ok {
		return c, nil
	}
	return nil, ErrUnknownColumn
}

// Args is an append-only placeholder builder. Add returns the "$N" token for a
// value and records the value positionally; the builder owns the numbering so a
// caller can never desynchronize placeholders from values.
//
// Args is not safe for concurrent use; build one per statement.
type Args struct {
	vals []any
}

// Add records v and returns its positional placeholder ("$1", "$2", ...).
func (a *Args) Add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

// Values returns the recorded values in placeholder order, ready to pass as the
// variadic args of a pgx Query/Exec call.
func (a *Args) Values() []any { return a.vals }

// Len reports how many placeholders have been allocated.
func (a *Args) Len() int { return len(a.vals) }
