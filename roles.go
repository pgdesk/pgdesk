package pgdesk

import "context"

// RoleBearer is a Principal with roles.
type RoleBearer interface {
	Roles() []string
}

// PrincipalRoles returns p's roles, or nil if p is not a RoleBearer.
func PrincipalRoles(p Principal) []string {
	rb, ok := p.(RoleBearer)
	if !ok {
		return nil
	}
	return rb.Roles()
}

// Roles is an Authorizer that maps each role to the capabilities it grants on every resource.
type Roles map[string][]Capability

// Authorize allows the capability if one of the principal's roles grants it, otherwise abstains.
func (rs Roles) Authorize(_ context.Context, attrs Attributes) (Decision, error) {
	for _, role := range PrincipalRoles(attrs.Principal) {

		for _, granted := range rs[role] {
			if granted == attrs.Capability {
				return Allow, nil
			}
		}
	}
	return Abstain, nil
}

// AllCapabilities returns every Capability.
func AllCapabilities() []Capability {
	return []Capability{
		CapAccessAdmin,
		CapList,
		CapView,
		CapCreate,
		CapUpdate,
		CapDelete,
		CapRunAction,
	}
}
