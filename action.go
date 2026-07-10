package pgdesk

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ActionFunc executes a row or bulk action against the selected rows inside the
// mutation's transaction. keys is the set of selected primary keys, already
// scoped to the principal; Keys.Int64s or Keys.Strings hand them back as a
// typed slice ready to bind to "= ANY($n)". The returned message is shown to the
// operator as a flash; returning an error rolls the whole action back and shows
// the error.
//
// Because pgdesk is pgx-first, actions receive the live pgx.Tx directly -- run
// whatever parameterized statements the action needs on it. The transaction is
// committed only if ActionFunc returns nil.
type ActionFunc func(ctx context.Context, tx pgx.Tx, keys Keys) (message string, err error)

// action is a registered row/bulk action (unexported; configured via
// Resource.Action).
type action struct {
	name    string
	label   string
	confirm string
	fn      ActionFunc
}

// ActionOption configures a registered action.
type ActionOption func(*action)

// WithConfirm attaches a confirmation prompt shown before the action runs
// (progressive enhancement; the POST still requires CSRF regardless).
func WithConfirm(text string) ActionOption {
	return func(a *action) { a.confirm = text }
}
