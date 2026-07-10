package pgdesk

import (
	"fmt"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// Keys is the set of primary keys selected for a bulk action. pgdesk decodes them
// from the request, scopes them against the principal, and confirms every one
// names a reachable row before the action runs (O6) -- so an ActionFunc receives
// only keys it is allowed to touch.
//
// Because pgdesk introspects the resource's key columns, Keys hands them back in a
// concretely-typed slice ready to bind to "= ANY($n)": Int64s for an integer key,
// Strings for a text, uuid, or enum key. This is both friendlier than a raw
// [][]any and safer -- a []any bound as a query argument fails to encode when pgx
// runs without a describe step (behind a transaction-pooling PgBouncer), whereas
// a concrete []int64 or []string encodes in every mode.
//
// For a composite key, or a key type Int64s/Strings do not cover, range over Raw.
type Keys struct {
	cols []*introspect.Column // the resource's key columns, for typing and errors
	vals [][]any              // one decoded key tuple per selected row
}

// Len reports how many rows were selected.
func (k Keys) Len() int { return len(k.vals) }

// Int64s returns the selected keys as an []int64 ready to bind to "= ANY($n)". It
// errors unless the resource has a single integer key column, and unless every
// decoded value is an integer -- so a wrong assumption about the key type surfaces
// as a clear error rather than a panic or a malformed query.
func (k Keys) Int64s() ([]int64, error) {
	col, err := k.single("Int64s", introspect.CatNumeric)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(k.vals))
	for i, tuple := range k.vals {
		n, ok := tuple[0].(int64)
		if !ok {
			return nil, fmt.Errorf("pgdesk: Keys.Int64s: key %q value is %T, not an integer", col.Name, tuple[0])
		}
		out[i] = n
	}
	return out, nil
}

// Strings returns the selected keys as a []string ready to bind to "= ANY($n)".
// It serves a text, uuid, or enum key column -- pgdesk decodes all three to a
// string -- and errors on any other key shape.
func (k Keys) Strings() ([]string, error) {
	col, err := k.single("Strings", introspect.CatText, introspect.CatUUID, introspect.CatEnum)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(k.vals))
	for i, tuple := range k.vals {
		s, ok := tuple[0].(string)
		if !ok {
			return nil, fmt.Errorf("pgdesk: Keys.Strings: key %q value is %T, not a string", col.Name, tuple[0])
		}
		out[i] = s
	}
	return out, nil
}

// Raw returns the decoded key tuples for a composite key or an exotic type. Each
// tuple positionally matches the resource's key columns. The slice is the action's
// to keep; pgdesk does not reuse it.
func (k Keys) Raw() [][]any { return k.vals }

// single validates that the key is a single column of one of the allowed
// categories and returns it, with an error naming the actual shape otherwise.
func (k Keys) single(method string, allow ...introspect.TypeCategory) (*introspect.Column, error) {
	if len(k.cols) != 1 {
		return nil, fmt.Errorf("pgdesk: Keys.%s needs a single-column key, but the resource key has %d columns; use Raw", method, len(k.cols))
	}
	col := k.cols[0]
	for _, c := range allow {
		if col.Category == c {
			return col, nil
		}
	}
	return nil, fmt.Errorf("pgdesk: Keys.%s: key column %q is %s; use a matching accessor or Raw", method, col.Name, col.DataType)
}
