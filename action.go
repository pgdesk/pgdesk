package pgdesk

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ActionFunc runs a bulk action on the selected rows inside tx. An error rolls tx back;
// otherwise message is shown to the operator (a default is used if empty).
type ActionFunc func(ctx context.Context, tx pgx.Tx, keys Keys) (message string, err error)

type action struct {
	name    string
	label   string
	confirm string
	fn      ActionFunc
}

// ActionOption configures an action added with Resource.Action.
type ActionOption func(*action)

// WithConfirm shows text in a confirmation prompt before the action runs.
func WithConfirm(text string) ActionOption {
	return func(a *action) { a.confirm = text }
}
