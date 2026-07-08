package pgdesk

import "context"

// Principal is the authenticated operator for a request. pgdesk has no built-in
// user database: the host's middleware authenticates the request and attaches a
// Principal via WithPrincipal. All authorization decisions are made against it.
//
// The interface is intentionally minimal (accept interfaces, return structs):
// implement it on whatever user type the host already has.
type Principal interface {
	// SubjectID is a stable identifier for the operator (used in audit events).
	SubjectID() string
	// DisplayName is a human-friendly label shown in the UI.
	DisplayName() string
}

// Capability enumerates the authorization checks pgdesk performs. Exactly one
// capability is checked per route, in the handler layer, before any query runs.
// Deny is the default: an unhandled capability is forbidden, not allowed.
type Capability string

const (
	CapAccessAdmin Capability = "access_admin"
	CapList        Capability = "list"
	CapView        Capability = "view"
	CapCreate      Capability = "create"
	CapUpdate      Capability = "update"
	CapDelete      Capability = "delete"
	CapRunAction   Capability = "run_action"
)

// Authorizer decides whether a principal may perform a capability. Every method
// receives the request context (which carries the Principal and request-scoped
// catalog snapshot) and the resource name (empty for CapAccessAdmin). Returning
// false denies; returning an error also denies and is logged.
//
// The seven hooks map 1:1 to Capability. They are enforced centrally in the
// handler and never duplicated in templates or query code where they could
// drift.
type Authorizer interface {
	CanAccessAdmin(ctx context.Context, p Principal) (bool, error)
	CanList(ctx context.Context, p Principal, resource string) (bool, error)
	CanView(ctx context.Context, p Principal, resource string) (bool, error)
	CanCreate(ctx context.Context, p Principal, resource string) (bool, error)
	CanUpdate(ctx context.Context, p Principal, resource string) (bool, error)
	CanDelete(ctx context.Context, p Principal, resource string) (bool, error)
	CanRunAction(ctx context.Context, p Principal, resource, action string) (bool, error)
}

// AllowAll is an Authorizer that permits every capability. It is a convenience
// for hosts that gate the entire admin behind their own middleware and treat any
// authenticated principal as fully authorized. It still requires a non-nil
// Principal on protected routes — a missing principal is denied upstream.
type AllowAll struct{}

func (AllowAll) CanAccessAdmin(context.Context, Principal) (bool, error)    { return true, nil }
func (AllowAll) CanList(context.Context, Principal, string) (bool, error)   { return true, nil }
func (AllowAll) CanView(context.Context, Principal, string) (bool, error)   { return true, nil }
func (AllowAll) CanCreate(context.Context, Principal, string) (bool, error) { return true, nil }
func (AllowAll) CanUpdate(context.Context, Principal, string) (bool, error) { return true, nil }
func (AllowAll) CanDelete(context.Context, Principal, string) (bool, error) { return true, nil }
func (AllowAll) CanRunAction(context.Context, Principal, string, string) (bool, error) {
	return true, nil
}

// authorize dispatches a capability to the configured Authorizer and fails
// closed: a nil authorizer, a nil principal, an error, or a false result all
// deny. It is the single choke point through which every route's authorization
// flows.
func authorize(ctx context.Context, az Authorizer, p Principal, cap Capability, resource, action string) (bool, error) {
	if az == nil || p == nil {
		return false, nil
	}
	switch cap {
	case CapAccessAdmin:
		return az.CanAccessAdmin(ctx, p)
	case CapList:
		return az.CanList(ctx, p, resource)
	case CapView:
		return az.CanView(ctx, p, resource)
	case CapCreate:
		return az.CanCreate(ctx, p, resource)
	case CapUpdate:
		return az.CanUpdate(ctx, p, resource)
	case CapDelete:
		return az.CanDelete(ctx, p, resource)
	case CapRunAction:
		return az.CanRunAction(ctx, p, resource, action)
	default:
		return false, nil // unknown capability → deny
	}
}
