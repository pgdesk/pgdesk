package pgdesk

import (
	"context"
	"slices"
	"testing"
)

// roleOperator declares its roles, so the shipped Roles authorizer can read them
// without knowing the host's concrete type.
type roleOperator struct {
	id    string
	roles []string
}

func (o roleOperator) SubjectID() string   { return o.id }
func (o roleOperator) DisplayName() string { return o.id }
func (o roleOperator) Roles() []string     { return o.roles }

// plainOperator is a Principal that does NOT declare roles.
type plainOperator struct{ id string }

func (o plainOperator) SubjectID() string   { return o.id }
func (o plainOperator) DisplayName() string { return o.id }

func decide(t *testing.T, az Authorizer, p Principal, cap Capability, resource string) Decision {
	t.Helper()
	dec, err := az.Authorize(context.Background(), Attributes{
		Principal: p, Capability: cap, Resource: resource,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return dec
}

var testGrants = Roles{
	"viewer": {CapAccessAdmin, CapList, CapView},
	"editor": {CapAccessAdmin, CapList, CapView, CapUpdate},
}

// A capability listed for one of the principal's roles is allowed; one listed for
// no role they hold is Abstain, not Deny, so the rule composes.
func TestRolesGrantsByRole(t *testing.T) {
	viewer := roleOperator{id: "v", roles: []string{"viewer"}}

	if got := decide(t, testGrants, viewer, CapList, "users"); got != Allow {
		t.Errorf("viewer CapList = %v, want Allow", got)
	}
	if got := decide(t, testGrants, viewer, CapUpdate, "users"); got != Abstain {
		t.Errorf("viewer CapUpdate = %v, want Abstain so other rules can decide", got)
	}
	if got := decide(t, testGrants, viewer, CapDelete, "users"); got != Abstain {
		t.Errorf("viewer CapDelete = %v, want Abstain", got)
	}
}

// Holding several roles grants the union of their capabilities, and a role the map
// does not mention contributes nothing rather than erroring.
func TestRolesUnionAndUnknownRoles(t *testing.T) {
	both := roleOperator{id: "b", roles: []string{"viewer", "editor", "nonexistent"}}

	if got := decide(t, testGrants, both, CapUpdate, "users"); got != Allow {
		t.Errorf("CapUpdate with editor = %v, want Allow", got)
	}
	if got := decide(t, testGrants, both, CapCreate, "users"); got != Abstain {
		t.Errorf("CapCreate granted by no held role = %v, want Abstain", got)
	}

	onlyUnknown := roleOperator{id: "u", roles: []string{"nonexistent"}}
	if got := decide(t, testGrants, onlyUnknown, CapList, "users"); got != Abstain {
		t.Errorf("unknown role = %v, want Abstain", got)
	}
}

// A Principal that does not declare roles grants nothing. Abstain denies when this
// is the only authorizer, so forgetting to implement RoleBearer fails closed
// rather than opening the admin.
func TestRolesPrincipalWithoutRolesGrantsNothing(t *testing.T) {
	if got := decide(t, testGrants, plainOperator{id: "p"}, CapList, "users"); got != Abstain {
		t.Errorf("principal without Roles() = %v, want Abstain", got)
	}
	// And a nil principal never reaches an authorizer, but must not panic if it does.
	if got := decide(t, testGrants, nil, CapList, "users"); got != Abstain {
		t.Errorf("nil principal = %v, want Abstain", got)
	}
}

// The point of Abstain: a role grant composes with an independent narrowing rule,
// and the narrowing rule wins wherever it applies.
func TestRolesComposesWithNarrowingRule(t *testing.T) {
	frozen := AuthorizerFunc(func(_ context.Context, attrs Attributes) (Decision, error) {
		if attrs.Resource == "ledger" && attrs.Capability == CapUpdate {
			return Deny, nil
		}
		return Abstain, nil
	})
	az := DenyOverrides(testGrants, frozen)
	editor := roleOperator{id: "e", roles: []string{"editor"}}

	if got := decide(t, az, editor, CapUpdate, "users"); got != Allow {
		t.Errorf("update users = %v, want Allow", got)
	}
	if got := decide(t, az, editor, CapUpdate, "ledger"); got != Deny {
		t.Errorf("update ledger = %v, want Deny", got)
	}
}

// AllCapabilities is a convenience for "this role may do everything". It must
// return a fresh slice, so a caller that appends to or sorts the result cannot
// change what the next caller sees.
func TestAllCapabilitiesIsFreshAndComplete(t *testing.T) {
	first := AllCapabilities()
	first[0] = "tampered"
	second := AllCapabilities()

	if slices.Contains(second, "tampered") {
		t.Error("AllCapabilities returns shared state; mutating one result changed the next")
	}
	// Every capability pgdesk checks must be present, or a role granted
	// "everything" would silently lack one.
	for _, want := range []Capability{
		CapAccessAdmin, CapList, CapView, CapCreate, CapUpdate, CapDelete, CapRunAction,
	} {
		if !slices.Contains(second, want) {
			t.Errorf("AllCapabilities is missing %q", want)
		}
	}
}

// A role granted every capability can do everything, including reach the admin.
func TestRolesWithAllCapabilities(t *testing.T) {
	az := Roles{"owner": AllCapabilities()}
	owner := roleOperator{id: "o", roles: []string{"owner"}}

	for _, cap := range AllCapabilities() {
		if got := decide(t, az, owner, cap, "users"); got != Allow {
			t.Errorf("owner %q = %v, want Allow", cap, got)
		}
	}
}

// Authorize must not mutate the grant map: it is shared across every concurrent
// request for the life of the admin.
func TestRolesAuthorizeDoesNotMutateGrants(t *testing.T) {
	grants := Roles{"viewer": {CapList}}
	before := len(grants)
	beforeCaps := len(grants["viewer"])

	decide(t, grants, roleOperator{id: "v", roles: []string{"viewer", "ghost"}}, CapDelete, "users")

	if len(grants) != before || len(grants["viewer"]) != beforeCaps {
		t.Errorf("Authorize mutated the grant map: %d roles / %d caps, want %d / %d",
			len(grants), len(grants["viewer"]), before, beforeCaps)
	}
	if _, ok := grants["ghost"]; ok {
		t.Error("Authorize inserted a missing role into the grant map")
	}
}

// PrincipalRoles is the seam a host uses to read roles off any Principal.
func TestPrincipalRoles(t *testing.T) {
	if got := PrincipalRoles(roleOperator{id: "r", roles: []string{"a", "b"}}); len(got) != 2 {
		t.Errorf("PrincipalRoles = %v, want 2 roles", got)
	}
	if got := PrincipalRoles(plainOperator{id: "p"}); got != nil {
		t.Errorf("PrincipalRoles on a plain Principal = %v, want nil", got)
	}
	if got := PrincipalRoles(nil); got != nil {
		t.Errorf("PrincipalRoles(nil) = %v, want nil", got)
	}
}
