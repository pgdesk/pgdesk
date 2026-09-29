package pgdesk

import "context"

// Principal is the signed-in operator. Middleware sets it with WithPrincipal.
type Principal interface {
	// SubjectID is a stable, unique ID, recorded as AuditEvent.ActorID.
	SubjectID() string

	// DisplayName is shown in the header and recorded as AuditEvent.ActorName.
	DisplayName() string
}

// Capability is an operation an Authorizer can allow.
type Capability string

// Capabilities.
const (
	// CapAccessAdmin allows using the admin at all. It is checked on every request.
	CapAccessAdmin Capability = "access_admin"
	// CapList allows list pages and CSV export.
	CapList Capability = "list"
	// CapView allows detail pages and showing rows as foreign-key labels.
	CapView   Capability = "view"
	CapCreate Capability = "create"
	CapUpdate Capability = "update"
	CapDelete Capability = "delete"
	// CapRunAction allows running the bulk action named in Attributes.Action.
	CapRunAction Capability = "run_action"
)

// Decision is an Authorizer's answer. Only Allow grants access.
type Decision int

// Decisions.
const (
	Deny Decision = iota
	Allow
	// Abstain leaves the decision to other authorizers combined with DenyOverrides.
	Abstain
)

// String returns "deny", "allow" or "abstain".
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

// Attributes describe an operation being authorized.
type Attributes struct {
	Principal Principal

	Capability Capability

	// Resource is the resource name; empty for CapAccessAdmin.
	Resource string

	// Action is the bulk action name for CapRunAction.
	Action string
}

// Authorizer decides whether a principal may use a capability.
// Without an Authorizer every capability is denied.
type Authorizer interface {
	Authorize(ctx context.Context, attrs Attributes) (Decision, error)
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, attrs Attributes) (Decision, error)

// Authorize calls f.
func (f AuthorizerFunc) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	return f(ctx, attrs)
}

// AllowAll allows every capability to any signed-in principal.
var AllowAll Authorizer = allowAll{}

type allowAll struct{}

func (allowAll) Authorize(context.Context, Attributes) (Decision, error) { return Allow, nil }

// DenyOverrides combines authorizers: a Deny or an error denies, otherwise an Allow allows,
// otherwise it abstains. The constraints of those that implement Scoper are combined.
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
