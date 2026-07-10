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

// Decision is the outcome of one authorizer's check.
//
// The zero value is Deny. An uninitialized Decision, one returned alongside an
// error, and one left behind by a switch with no matching case all forbid the
// operation. Failing closed is a property of the type, not of the caller.
type Decision int

const (
	Deny    Decision = iota // this authorizer forbids the operation
	Allow                   // this authorizer permits the operation
	Abstain                 // this authorizer has no opinion; others decide
)

// String implements fmt.Stringer so a Decision is legible in logs.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Abstain:
		return "abstain"
	default:
		return "deny"
	}
}

// Attributes describes the operation under consideration. It is a value type, so
// pgdesk can add fields in a later release without breaking implementations.
//
// It is deliberately not named Request: every call site inside this package
// already has an *http.Request named r in scope.
type Attributes struct {
	// Principal is the authenticated operator. Never nil when an Authorizer runs.
	Principal Principal
	// Capability is the operation being attempted.
	Capability Capability
	// Resource is the resource name; empty for CapAccessAdmin.
	Resource string
	// Action is the action name; set only for CapRunAction.
	Action string
}

// Authorizer decides whether a principal may perform a capability.
//
// pgdesk consults it at one choke point per route, before any query runs, and
// again when rendering an affordance, so the UI never offers an operation the
// operator cannot perform. Both call sites reduce the Decision the same way: only
// Allow allows.
//
// An Authorizer may additionally implement Scoper to restrict which rows the
// principal can reach. Authorize must be safe for concurrent use, and should be
// cheap: it is called once per rendered action on a list page. An implementation
// that performs I/O is responsible for memoizing against the request context.
type Authorizer interface {
	Authorize(ctx context.Context, attrs Attributes) (Decision, error)
}

// AuthorizerFunc adapts an ordinary function to Authorizer.
type AuthorizerFunc func(ctx context.Context, attrs Attributes) (Decision, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	return f(ctx, attrs)
}

// AllowAll is an Authorizer that permits every capability and restricts no rows.
// It is a convenience for hosts that gate the entire admin behind their own
// middleware and treat any authenticated principal as fully authorized. A missing
// Principal is still denied upstream.
//
// It is a value, not a type, following io.Discard and slog.DiscardHandler:
// WithAuthorizer(pgdesk.AllowAll).
var AllowAll Authorizer = allowAll{}

type allowAll struct{}

func (allowAll) Authorize(context.Context, Attributes) (Decision, error) { return Allow, nil }

// DenyOverrides combines authorizers under the deny-overrides rule: any Deny
// forbids the operation; otherwise a single Allow permits it; otherwise the
// result is Abstain.
//
// Because Abstain is the identity of this operation, adding an authorizer to a
// DenyOverrides set can only ever narrow access, never widen it. Because the
// result is Abstain rather than Deny when every member abstains, the sets nest.
//
// Row constraints compose the same way: the returned Authorizer implements Scoper
// by ANDing the constraints of every member that scopes.
//
// Nil authorizers are dropped. DenyOverrides() abstains, which denies.
func DenyOverrides(azs ...Authorizer) Authorizer {
	set := make(denyOverrides, 0, len(azs))
	for _, az := range azs {
		if az != nil {
			set = append(set, az)
		}
	}
	return set
}

type denyOverrides []Authorizer

// Authorize implements Authorizer. An error from any member denies, and the error
// propagates: a broken authorizer must never be mistaken for one that abstained.
func (set denyOverrides) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	allowed := false
	for _, az := range set {
		dec, err := az.Authorize(ctx, attrs)
		if err != nil {
			return Deny, err
		}
		switch dec {
		case Deny:
			return Deny, nil
		case Allow:
			allowed = true
		}
	}
	if allowed {
		return Allow, nil
	}
	return Abstain, nil
}

// Scope implements Scoper by concatenating the constraints of every member that
// is itself a Scoper. Constraints are ANDed, so -- as with Deny -- adding an
// authorizer can only narrow the rows a principal can reach.
func (set denyOverrides) Scope(ctx context.Context, attrs Attributes) ([]Constraint, error) {
	var out []Constraint
	for _, az := range set {
		sc, ok := az.(Scoper)
		if !ok {
			continue
		}
		cs, err := sc.Scope(ctx, attrs)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// permitted reports whether az allows attrs. This is the single reduction from
// Decision to bool in the whole package. It is written "dec == Allow" and never
// "dec != Deny": every path that is not an explicit Allow denies -- a nil
// authorizer, a nil principal, an Abstain, or an error.
//
// The attrs.Principal == nil gate holds only because PrincipalFromContext
// normalizes a typed-nil pointer principal to nil; without that normalization a
// non-nil interface wrapping a nil pointer would slip past this check.
func permitted(ctx context.Context, az Authorizer, attrs Attributes) (bool, error) {
	if az == nil || attrs.Principal == nil {
		return false, nil
	}
	dec, err := az.Authorize(ctx, attrs)
	if err != nil {
		return false, err
	}
	return dec == Allow, nil
}
