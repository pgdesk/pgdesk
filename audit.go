package pgdesk

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type AuditAction string

const (
	AuditCreate     AuditAction = "create"
	AuditUpdate     AuditAction = "update"
	AuditDelete     AuditAction = "delete"
	AuditBulkAction AuditAction = "bulk_action"
)

type AuditEvent struct {
	ActorID   string
	ActorName string

	Action AuditAction

	Resource   string
	ActionName string

	Key string

	Before map[string]any
	After  map[string]any

	SourceIP string

	RequestID string

	At time.Time
}

type AuditLogger interface {
	LogAudit(ctx context.Context, e AuditEvent) error
}

type TxAuditLogger interface {
	LogAuditTx(ctx context.Context, tx pgx.Tx, e AuditEvent) error
}
