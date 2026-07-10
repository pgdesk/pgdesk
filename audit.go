package pgdesk

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditAction is the kind of mutation recorded in an audit event.
type AuditAction string

const (
	AuditCreate     AuditAction = "create"
	AuditUpdate     AuditAction = "update"
	AuditDelete     AuditAction = "delete"
	AuditBulkAction AuditAction = "bulk_action"
)

// AuditEvent is a structured, complete record of a mutation. Before/after
// snapshots respect field-level redaction so secrets never enter the audit trail
// (columns marked with Resource.Redact are omitted).
type AuditEvent struct {
	// ActorID and ActorName come from the request Principal.
	ActorID   string
	ActorName string
	// Action is the mutation kind.
	Action AuditAction
	// Resource is the resource (table) name; Action detail for bulk actions.
	Resource   string
	ActionName string
	// Key is the affected row's primary key, rendered as its URL segment.
	Key string
	// Before and After are redaction-filtered column snapshots. Before is nil for
	// creates; After is nil for deletes.
	Before map[string]any
	After  map[string]any
	// SourceIP is the client IP as seen by the server.
	SourceIP string
	// RequestID correlates the event with server logs and the browser 500 page.
	RequestID string
	// At is the event time in UTC.
	At time.Time
}

// AuditLogger records audit events out of band (best effort). Because it runs
// outside the mutation transaction, an event can be lost if the process crashes
// between commit and log; that tradeoff is acceptable for hosts shipping audit
// to an external sink and is documented here. For guaranteed durability, prefer
// TxAuditLogger.
type AuditLogger interface {
	// LogAudit records an event. A returned error is logged but does NOT roll
	// back the mutation (this is the best-effort variant).
	LogAudit(ctx context.Context, e AuditEvent) error
}

// TxAuditLogger records an audit event inside the SAME transaction as the
// mutation. A mutation cannot commit without its audit record, and an audit
// failure rolls back the mutation. Use this when the audit log is DB-backed and
// must not miss events.
type TxAuditLogger interface {
	// LogAuditTx writes the event using tx. Returning an error aborts the whole
	// transaction, including the mutation it accompanies.
	LogAuditTx(ctx context.Context, tx pgx.Tx, e AuditEvent) error
}
