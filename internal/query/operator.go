package query

import (
	"errors"
	"slices"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

type Operator string

const (
	OpEq      Operator = "eq"
	OpNe      Operator = "ne"
	OpILike   Operator = "ilike"
	OpIn      Operator = "in"
	OpLt      Operator = "lt"
	OpGt      Operator = "gt"
	OpBetween Operator = "between"
	OpIsNull  Operator = "isnull"
)

var ErrOperatorNotAllowed = errors.New("pgdesk/query: operator not allowed for column type")

var allowedByCategory = map[introspect.TypeCategory][]Operator{
	introspect.CatText:      {OpEq, OpNe, OpILike, OpIn, OpIsNull},
	introspect.CatNumeric:   {OpEq, OpNe, OpLt, OpGt, OpBetween, OpIn, OpIsNull},
	introspect.CatTimestamp: {OpEq, OpNe, OpLt, OpGt, OpBetween, OpIsNull},
	introspect.CatBool:      {OpEq, OpNe, OpIsNull},
	introspect.CatEnum:      {OpEq, OpNe, OpIn, OpIsNull},
	introspect.CatUUID:      {OpEq, OpNe, OpIn, OpIsNull},
	introspect.CatJSON:      {OpIsNull},
	introspect.CatOther:     {OpEq, OpNe, OpIsNull},
}

func AllowedOperators(cat introspect.TypeCategory) []Operator {
	if ops, ok := allowedByCategory[cat]; ok {
		return ops
	}
	return allowedByCategory[introspect.CatOther]
}

func OperatorAllowed(col *introspect.Column, op Operator) bool {
	if col == nil {
		return false
	}
	return slices.Contains(AllowedOperators(col.Category), op)
}

func ParseOperator(col *introspect.Column, token string) (Operator, error) {
	op := Operator(token)
	switch op {
	case OpEq, OpNe, OpILike, OpIn, OpLt, OpGt, OpBetween, OpIsNull:

	default:
		return "", ErrOperatorNotAllowed
	}
	if !OperatorAllowed(col, op) {
		return "", ErrOperatorNotAllowed
	}
	return op, nil
}
