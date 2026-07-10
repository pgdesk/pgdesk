package pgdesk

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pgdesk/pgdesk/internal/query"
)

// Constraint is one predicate ANDed into every statement pgdesk issues for a
// resource. Build one with Eq, Ne, or In -- a Constraint cannot be constructed any
// other way, so it is always well-formed.
//
// The column is resolved against the live catalog snapshot and emitted as a
// quoted identifier; the value becomes $N. Nothing here is interpolated into SQL.
// Postgres compares the value to the column using the column's own type, so
// a Go string may constrain a uuid column without the host coercing anything.
//
// A constraint on a column that does not exist, or an operator the column's type
// category does not permit, denies the request rather than dropping the predicate.
type Constraint struct {
	column string
	op     query.Operator
	values []any
}

// Eq constrains a resource to rows whose column equals value.
func Eq(column string, value any) Constraint {
	return Constraint{column: column, op: query.OpEq, values: []any{value}}
}

// Ne constrains a resource to rows whose column differs from value. It follows
// SQL semantics, so a row whose column IS NULL does not match -- on a nullable
// column that narrows the visible set, which fails in the safe direction.
func Ne(column string, value any) Constraint {
	return Constraint{column: column, op: query.OpNe, values: []any{value}}
}

// In constrains a resource to rows whose column equals any of the values. With no
// values it matches nothing, so an operator scoped to an empty set sees no rows.
func In(column string, values ...any) Constraint {
	return Constraint{column: column, op: query.OpIn, values: values}
}

// Scoper narrows the set of rows a principal may reach. It is an optional
// interface: when an Authorizer also implements Scoper, pgdesk ANDs the returned
// constraints into the WHERE clause of every statement it issues for that
// resource -- list, export, detail, edit form, update, delete, and a bulk action's
// key check. For a rule that only narrows rows and decides nothing, wrap a
// function in ScopeOnly.
//
// Scope answers "which rows exist for me"; Authorize answers "may I". Keeping
// them apart is what makes list pagination correct -- the constraint is in the
// query, so LIMIT applies after filtering -- and what makes a row-level rule
// atomic: the predicate rides in the mutation's own WHERE clause, leaving no
// window between the check and the write.
//
// Scope receives the same Attributes as Authorize, so a constraint may depend on
// the capability: an operator may be permitted to see a row on the list page and
// still be forbidden to update or delete it. Returning a nil slice means "no
// restriction" and costs nothing.
//
// An equality constraint (Eq) is also enforced on INSERT and UPDATE: the scoped
// column is written from the constraint's value, ignoring any value the operator
// submitted, so a scoped operator can neither create nor move a row outside their
// scope. A Ne or In constraint cannot pin a single write value and so does not
// constrain the written value; mark such columns Readonly if the operator must
// not set them.
type Scoper interface {
	Scope(ctx context.Context, attrs Attributes) ([]Constraint, error)
}

// ScopeFunc adapts an ordinary function to Scoper.
type ScopeFunc func(ctx context.Context, attrs Attributes) ([]Constraint, error)

// Scope implements Scoper.
func (f ScopeFunc) Scope(ctx context.Context, attrs Attributes) ([]Constraint, error) {
	return f(ctx, attrs)
}

// ScopeOnly lifts a scope function into an Authorizer that abstains on every
// capability and only narrows rows. Use it for a rule that restricts which rows a
// principal may reach but makes no permission decision:
//
//	pgdesk.ScopeOnly(func(_ context.Context, a pgdesk.Attributes) ([]pgdesk.Constraint, error) {
//		if isAdmin(a.Principal) {
//			return nil, nil
//		}
//		return []pgdesk.Constraint{pgdesk.Eq("org_id", orgOf(a.Principal))}, nil
//	})
//
// fn may be a bare function or a method value (myPolicy.Scope), so a stateful
// scoper needs no wrapper of its own.
func ScopeOnly(fn func(ctx context.Context, attrs Attributes) ([]Constraint, error)) Authorizer {
	return scopeOnly{ScopeFunc(fn)}
}

// scopeOnly is an Authorizer that abstains and delegates scoping.
type scopeOnly struct{ Scoper }

func (scopeOnly) Authorize(context.Context, Attributes) (Decision, error) { return Abstain, nil }

// scopeOf returns the principal's row constraints for one operation. An
// authorizer that does not implement Scoper restricts nothing and allocates
// nothing.
func scopeOf(ctx context.Context, az Authorizer, attrs Attributes) ([]Constraint, error) {
	sc, ok := az.(Scoper)
	if !ok {
		return nil, nil
	}
	return sc.Scope(ctx, attrs)
}

// resolveConstraints validates constraints against the resource's catalog table
// and converts them into parameterized predicates. It fails closed: nothing here
// silently drops a constraint, because a dropped constraint widens access.
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

// scopeFor resolves the principal's row constraints for one capability into
// catalog-validated predicates, ready to AND into a statement's WHERE clause.
//
// Callers must treat an error as a denial, not as an empty scope.
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

// scopeDenied renders a scope failure. A bad scope is a host configuration bug,
// not an operator mistake: it is logged in full server-side and surfaces as a
// generic 403 so nothing about the policy or the schema leaks.
func (a *Admin) scopeDenied(w http.ResponseWriter, r *http.Request, res *Resource, err error) {
	LoggerFromContext(r.Context()).Error("pgdesk: scope resolution failed",
		"resource", res.name, "error", err)
	a.renderError(w, r, http.StatusForbidden, "You are not permitted to perform this action.")
}
