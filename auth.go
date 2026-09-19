package pgdesk

import "context"

type Principal interface {
	SubjectID() string

	DisplayName() string
}

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

type Decision int

const (
	Deny Decision = iota
	Allow
	Abstain
)

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

type Attributes struct {
	Principal Principal

	Capability Capability

	Resource string

	Action string
}

type Authorizer interface {
	Authorize(ctx context.Context, attrs Attributes) (Decision, error)
}

type AuthorizerFunc func(ctx context.Context, attrs Attributes) (Decision, error)

func (f AuthorizerFunc) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	return f(ctx, attrs)
}

var AllowAll Authorizer = allowAll{}

type allowAll struct{}

func (allowAll) Authorize(context.Context, Attributes) (Decision, error) { return Allow, nil }

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
