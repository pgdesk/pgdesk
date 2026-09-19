package pgdesk

import "context"

// RoleBearer is an optional interface a Principal may implement to declare the
// roles it holds. pgdesk never requires it: it is what the shipped Roles
// authorizer reads, so a host can use role-based grants without pgdesk knowing
// anything about its user type.
//
// It is an optional interface for the same reason Scoper is -- adding a method to
// Principal would break every existing implementation, while a type assertion
// costs nothing and stays opt-in.
type RoleBearer interface {
	// Roles returns the role names the principal holds. An empty result grants
	// nothing.
	Roles() []string
}

// PrincipalRoles returns the roles p declares through RoleBearer, or nil if p is
// absent or declares none. It is exported so a host writing its own authorizer can
// read roles the same way the shipped one does.
func PrincipalRoles(p Principal) []string {
	rb, ok := p.(RoleBearer)
	if !ok {
		return nil
	}
	return rb.Roles()
}

// Roles grants capabilities by role name -- the smallest useful authorizer, and
// the one most hosts need:
//
//	pgdesk.WithAuthorizer(pgdesk.Roles{
//	    "viewer": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView},
//	    "editor": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView,
//	               pgdesk.CapCreate, pgdesk.CapUpdate},
//	    "owner":  pgdesk.AllCapabilities(),
//	})
//
// The principal's roles are read through RoleBearer. A principal that does not
// implement it holds no roles and is granted nothing.
//
// Include CapAccessAdmin in every role that should be able to reach the admin at
// all: it gates every route, so a role without it is locked out entirely.
//
// Grants are admin-wide. Roles deliberately has no per-resource dimension: a rule
// that narrows one resource is a separate authorizer in a DenyOverrides set, which
// keeps each rule readable on its own and means adding one can only ever remove
// access. A role that should not touch one table is expressed as
//
//	pgdesk.DenyOverrides(roles, frozenLedger)
//
// A capability no held role grants yields Abstain, not Deny, so Roles composes.
// Used as the only authorizer, Abstain still denies -- only Allow allows.
//
// The map is read, never written, and must not be modified after New: it is shared
// by every concurrent request for the life of the admin.
type Roles map[string][]Capability

// Authorize implements Authorizer. It reports Allow when any role the principal
// holds lists attrs.Capability, and Abstain otherwise. It never returns an error:
// a grant table cannot fail.
func (rs Roles) Authorize(_ context.Context, attrs Attributes) (Decision, error) {
	for _, role := range PrincipalRoles(attrs.Principal) {
		// Indexing a missing role yields a nil slice, so an unrecognized role
		// contributes nothing rather than inserting into the map.
		for _, granted := range rs[role] {
			if granted == attrs.Capability {
				return Allow, nil
			}
		}
	}
	return Abstain, nil
}

// AllCapabilities returns every capability pgdesk checks, for a role that may do
// everything. A fresh slice each call, so a caller that appends to or reorders the
// result cannot change what another caller sees.
//
// A capability added in a later release appears here automatically, which widens
// what such a role may do. Where that is not wanted, list the capabilities
// explicitly instead.
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
