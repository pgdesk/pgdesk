package pgdesk

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type ActionFunc func(ctx context.Context, tx pgx.Tx, keys Keys) (message string, err error)

type action struct {
	name    string
	label   string
	confirm string
	fn      ActionFunc
}

type ActionOption func(*action)

func WithConfirm(text string) ActionOption {
	return func(a *action) { a.confirm = text }
}
