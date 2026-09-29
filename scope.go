package pgdesk

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pgdesk/pgdesk/internal/query"
)

// Constraint is a row filter returned by a Scoper.
type Constraint struct {
	column string
	op     query.Operator
	values []any
}

// Eq matches rows where column equals value.
func Eq(column string, value any) Constraint {
	return Constraint{column: column, op: query.OpEq, values: []any{value}}
}

// Ne matches rows where column does not equal value.
func Ne(column string, value any) Constraint {
	return Constraint{column: column, op: query.OpNe, values: []any{value}}
}

// In matches rows where column equals one of values.
func In(column string, values ...any) Constraint {
	return Constraint{column: column, op: query.OpIn, values: values}
}

// Scoper limits the rows a principal can see and change. Its constraints are added to the
// SQL WHERE clause.
type Scoper interface {
	Scope(ctx context.Context, attrs Attributes) ([]Constraint, error)
}

// ScopeFunc adapts a function to Scoper.
type ScopeFunc func(ctx context.Context, attrs Attributes) ([]Constraint, error)

// Scope calls f.
func (f ScopeFunc) Scope(ctx context.Context, attrs Attributes) ([]Constraint, error) {
	return f(ctx, attrs)
}

// ScopeOnly returns an Authorizer that abstains on every decision and limits rows with fn.
// Combine it with DenyOverrides.
func ScopeOnly(fn func(ctx context.Context, attrs Attributes) ([]Constraint, error)) Authorizer {
	return scopeOnly{ScopeFunc(fn)}
}

type scopeOnly struct{ Scoper }

func (scopeOnly) Authorize(context.Context, Attributes) (Decision, error) { return Abstain, nil }

func scopeOf(ctx context.Context, az Authorizer, attrs Attributes) ([]Constraint, error) {
	sc, ok := az.(Scoper)
	if !ok {
		return nil, nil
	}
	return sc.Scope(ctx, attrs)
}

func resolveConstraints(res *Resource, cs []Constraint) ([]query.Filter, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	out := make([]query.Filter, 0, len(cs))
	for _, c := range cs {
		col, ok := res.table.Column(c.column)
		if !ok {
			return nil, fmt.Errorf("scope on %q: %w: %q", res.name, ErrUnknownColumn, c.column)
		}
		if !query.OperatorAllowed(col, c.op) {
			return nil, fmt.Errorf("scope on %q.%q: %w: %q", res.name, c.column, query.ErrOperatorNotAllowed, c.op)
		}
		out = append(out, query.Filter{Col: col, Op: c.op, Values: c.values})
	}
	return out, nil
}

func (a *Admin) scopeFor(r *http.Request, res *Resource, capability Capability, action string) ([]query.Filter, error) {
	attrs := Attributes{
		Principal:  PrincipalFromContext(r.Context()),
		Capability: capability,
		Resource:   res.name,
		Action:     action,
	}
	cs, err := scopeOf(r.Context(), a.cfg.authorizer, attrs)
	if err != nil {
		return nil, err
	}
	return resolveConstraints(res, cs)
}

func (a *Admin) scopeDenied(w http.ResponseWriter, r *http.Request, res *Resource, err error) {
	LoggerFromContext(r.Context()).Error("pgdesk: scope resolution failed",
		"resource", res.name, "error", err)
	a.renderError(w, r, http.StatusForbidden, "You are not permitted to perform this action.")
}
