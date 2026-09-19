package pgdesk

import (
	"context"
	"slices"
	"testing"
)

type roleOperator struct {
	id    string
	roles []string
}

func (o roleOperator) SubjectID() string   { return o.id }
func (o roleOperator) DisplayName() string { return o.id }
func (o roleOperator) Roles() []string     { return o.roles }

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

func TestRolesPrincipalWithoutRolesGrantsNothing(t *testing.T) {
	if got := decide(t, testGrants, plainOperator{id: "p"}, CapList, "users"); got != Abstain {
		t.Errorf("principal without Roles() = %v, want Abstain", got)
	}

	if got := decide(t, testGrants, nil, CapList, "users"); got != Abstain {
		t.Errorf("nil principal = %v, want Abstain", got)
	}
}

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

func TestAllCapabilitiesIsFreshAndComplete(t *testing.T) {
	first := AllCapabilities()
	first[0] = "tampered"
	second := AllCapabilities()

	if slices.Contains(second, "tampered") {
		t.Error("AllCapabilities returns shared state; mutating one result changed the next")
	}

	for _, want := range []Capability{
		CapAccessAdmin, CapList, CapView, CapCreate, CapUpdate, CapDelete, CapRunAction,
	} {
		if !slices.Contains(second, want) {
			t.Errorf("AllCapabilities is missing %q", want)
		}
	}
}

func TestRolesWithAllCapabilities(t *testing.T) {
	az := Roles{"owner": AllCapabilities()}
	owner := roleOperator{id: "o", roles: []string{"owner"}}

	for _, cap := range AllCapabilities() {
		if got := decide(t, az, owner, cap, "users"); got != Allow {
			t.Errorf("owner %q = %v, want Allow", cap, got)
		}
	}
}

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
