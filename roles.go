package pgdesk

import "context"

type RoleBearer interface {
	Roles() []string
}

func PrincipalRoles(p Principal) []string {
	rb, ok := p.(RoleBearer)
	if !ok {
		return nil
	}
	return rb.Roles()
}

type Roles map[string][]Capability

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
