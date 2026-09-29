package pgdesk

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditAction is the kind of change an AuditEvent records.
type AuditAction string

// AuditAction values.
const (
	AuditCreate     AuditAction = "create"
	AuditUpdate     AuditAction = "update"
	AuditDelete     AuditAction = "delete"
	AuditBulkAction AuditAction = "bulk_action"
)

// AuditEvent describes one change made through the admin.
type AuditEvent struct {
	ActorID   string
	ActorName string

	Action AuditAction

	Resource string
	// ActionName is the bulk action name, set when Action is AuditBulkAction.
	ActionName string

	// Key is the row key as it appears in admin URLs; for a bulk action, the row count.
	Key string

	// Before is the deleted row; After is the created or updated row.
	// Redact columns are left out.
	Before map[string]any
	After  map[string]any

	SourceIP string

	RequestID string

	At time.Time
}

// AuditLogger records an AuditEvent after the change commits. Its errors are only logged.
type AuditLogger interface {
	LogAudit(ctx context.Context, e AuditEvent) error
}

// TxAuditLogger records an AuditEvent inside the change's transaction. An error rolls the change back.
type TxAuditLogger interface {
	LogAuditTx(ctx context.Context, tx pgx.Tx, e AuditEvent) error
}
