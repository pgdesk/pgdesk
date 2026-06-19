package pgdesk

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ActionFunc executes a row or bulk action against the selected rows inside the
// mutation's transaction (O4). keys holds the decoded primary-key tuples of the
// selected rows (one tuple per row; each tuple positionally matches the
// resource's key columns). The returned message is shown to the operator as a
// flash; returning an error rolls the whole action back and shows the error.
//
// Because pgdesk is pgx-first, actions receive the live pgx.Tx directly — run
// whatever parameterized statements the action needs on it. The transaction is
// committed only if ActionFunc returns nil.
type ActionFunc func(ctx context.Context, tx pgx.Tx, keys [][]any) (message string, err error)

// action is a registered row/bulk action (unexported; configured via
// Resource.Action).
type action struct {
	name    string
	label   string
	confirm string
	fn      ActionFunc
	allowed func(Principal) bool
}

// ActionOption configures a registered action.
type ActionOption func(*action)

// WithConfirm attaches a confirmation prompt shown before the action runs
// (progressive enhancement; the POST still requires CSRF regardless).
func WithConfirm(text string) ActionOption {
	return func(a *action) { a.confirm = text }
}

// WithActionAllowed gates an action behind a per-principal predicate, in addition
// to the central CanRunAction authorization (O6). Returning false hides and
// forbids the action.
func WithActionAllowed(fn func(Principal) bool) ActionOption {
	return func(a *action) { a.allowed = fn }
}
