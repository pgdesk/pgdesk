package query

import (
	"errors"
	"slices"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// Operator is a filter comparison token. The set of legal operators for a column
// is derived from the column's type category (D3): an operator that is not
// whitelisted for the category is rejected, never emitted as SQL.
type Operator string

const (
	OpEq      Operator = "eq"      // column = $1
	OpILike   Operator = "ilike"   // column ILIKE $1  (text contains, case-insensitive)
	OpIn      Operator = "in"      // column = ANY($1)
	OpLt      Operator = "lt"      // column < $1
	OpGt      Operator = "gt"      // column > $1
	OpBetween Operator = "between" // column BETWEEN $1 AND $2
	OpIsNull  Operator = "isnull"  // column IS NULL / IS NOT NULL (value is bool)
)

// ErrOperatorNotAllowed is returned when a filter operator is not permitted for
// the target column's type category. Callers translate it to HTTP 400 (D3).
var ErrOperatorNotAllowed = errors.New("pgdesk/query: operator not allowed for column type")

// allowedByCategory is the authoritative operator whitelist. isnull is universal
// (every nullable-capable column supports a null test). The table mirrors the
// locked D3 spec: text → eq/ilike/in/isnull; timestamp → eq/lt/gt/between/isnull;
// bool → eq/isnull; enum → eq/in/isnull; with numeric/uuid/json filled in
// conservatively.
var allowedByCategory = map[introspect.TypeCategory][]Operator{
	introspect.CatText:      {OpEq, OpILike, OpIn, OpIsNull},
	introspect.CatNumeric:   {OpEq, OpLt, OpGt, OpBetween, OpIn, OpIsNull},
	introspect.CatTimestamp: {OpEq, OpLt, OpGt, OpBetween, OpIsNull},
	introspect.CatBool:      {OpEq, OpIsNull},
	introspect.CatEnum:      {OpEq, OpIn, OpIsNull},
	introspect.CatUUID:      {OpEq, OpIn, OpIsNull},
	introspect.CatJSON:      {OpIsNull},
	introspect.CatOther:     {OpEq, OpIsNull},
}

// AllowedOperators returns the operators permitted for a type category, in a
// stable order suitable for rendering a filter UI. The returned slice must not
// be mutated.
func AllowedOperators(cat introspect.TypeCategory) []Operator {
	if ops, ok := allowedByCategory[cat]; ok {
		return ops
	}
	return allowedByCategory[introspect.CatOther]
}

// OperatorAllowed reports whether op is whitelisted for the column's category.
func OperatorAllowed(col *introspect.Column, op Operator) bool {
	if col == nil {
		return false
	}
	return slices.Contains(AllowedOperators(col.Category), op)
}

// ParseOperator validates a request operator token against a resolved column and
// returns the typed Operator or an error. This is the operator gate: an
// unrecognized or non-whitelisted token fails closed (D3).
func ParseOperator(col *introspect.Column, token string) (Operator, error) {
	op := Operator(token)
	switch op {
	case OpEq, OpILike, OpIn, OpLt, OpGt, OpBetween, OpIsNull:
		// recognized token; fall through to whitelist check
	default:
		return "", ErrOperatorNotAllowed
	}
	if !OperatorAllowed(col, op) {
		return "", ErrOperatorNotAllowed
	}
	return op, nil
}
