package pgdesk

import (
	"context"
	"errors"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

type testPrincipal struct{ id string }

func (p testPrincipal) SubjectID() string   { return p.id }
func (p testPrincipal) DisplayName() string { return p.id }

func fixed(d Decision) Authorizer {
	return AuthorizerFunc(func(context.Context, Attributes) (Decision, error) { return d, nil })
}

var errBoom = errors.New("boom")

func boom() Authorizer {
	return AuthorizerFunc(func(context.Context, Attributes) (Decision, error) { return Allow, errBoom })
}

func TestDecisionZeroValueIsDeny(t *testing.T) {
	var d Decision
	if d != Deny {
		t.Fatalf("zero Decision = %v, want Deny", d)
	}
	if Deny.String() != "deny" || Allow.String() != "allow" || Abstain.String() != "abstain" {
		t.Error("Decision.String is wrong")
	}
}

func TestDenyOverrides(t *testing.T) {
	cases := []struct {
		name string
		azs  []Authorizer
		want Decision
	}{
		{"empty set abstains", nil, Abstain},
		{"all abstain", []Authorizer{fixed(Abstain), fixed(Abstain)}, Abstain},
		{"one allow", []Authorizer{fixed(Abstain), fixed(Allow)}, Allow},
		{"deny beats allow", []Authorizer{fixed(Allow), fixed(Deny)}, Deny},
		{"deny beats allow, either order", []Authorizer{fixed(Deny), fixed(Allow)}, Deny},
		{"nil members are dropped", []Authorizer{nil, fixed(Allow), nil}, Allow},
	}
	for _, c := range cases {
		got, err := DenyOverrides(c.azs...).Authorize(context.Background(), Attributes{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDenyOverridesPropagatesError(t *testing.T) {
	dec, err := DenyOverrides(boom(), fixed(Allow)).Authorize(context.Background(), Attributes{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if dec != Deny {
		t.Errorf("decision = %v, want Deny", dec)
	}
}

func TestDenyOverridesNests(t *testing.T) {
	inner := DenyOverrides(fixed(Abstain), fixed(Abstain))
	dec, err := DenyOverrides(inner, fixed(Allow)).Authorize(context.Background(), Attributes{})
	if err != nil || dec != Allow {
		t.Fatalf("nested set = %v (%v), want Allow", dec, err)
	}
}

func TestPermittedFailsClosed(t *testing.T) {
	ctx := context.Background()
	p := testPrincipal{"u1"}

	cases := []struct {
		name string
		az   Authorizer
		p    Principal
		want bool
	}{
		{"nil authorizer", nil, p, false},
		{"nil principal", fixed(Allow), nil, false},
		{"abstain", fixed(Abstain), p, false},
		{"deny", fixed(Deny), p, false},
		{"error", boom(), p, false},
		{"allow", fixed(Allow), p, true},
	}
	for _, c := range cases {
		got, _ := permitted(ctx, c.az, Attributes{Principal: c.p})
		if got != c.want {
			t.Errorf("%s: permitted = %v, want %v", c.name, got, c.want)
		}
	}
}

type scopeAZ struct{ cs []Constraint }

func (scopeAZ) Authorize(context.Context, Attributes) (Decision, error) { return Abstain, nil }
func (s scopeAZ) Scope(context.Context, Attributes) ([]Constraint, error) {
	return s.cs, nil
}

func TestDenyOverridesScopePropagatesError(t *testing.T) {
	failing := scopeErrAZ{}
	set := DenyOverrides(fixed(Allow), failing, scopeAZ{[]Constraint{Eq("org_id", 1)}}).(Scoper)
	if _, err := set.Scope(context.Background(), Attributes{}); !errors.Is(err, errBoom) {
		t.Fatalf("Scope error = %v, want errBoom", err)
	}
}

type scopeErrAZ struct{}

func (scopeErrAZ) Authorize(context.Context, Attributes) (Decision, error) { return Abstain, nil }
func (scopeErrAZ) Scope(context.Context, Attributes) ([]Constraint, error) { return nil, errBoom }

func TestDenyOverridesScopeEmptyWhenNoneScope(t *testing.T) {
	set := DenyOverrides(fixed(Allow), fixed(Deny))
	sc, ok := set.(Scoper)
	if !ok {
		t.Fatal("a DenyOverrides set always implements Scoper")
	}
	cs, err := sc.Scope(context.Background(), Attributes{})
	if err != nil || cs != nil {
		t.Errorf("empty-scope set = %v, %v; want nil, nil", cs, err)
	}
}

func TestDenyOverridesScopeConcatenates(t *testing.T) {
	set := DenyOverrides(
		fixed(Allow),
		scopeAZ{[]Constraint{Eq("org_id", 7)}},
		scopeAZ{[]Constraint{Ne("role", "owner")}},
	)
	sc, ok := set.(Scoper)
	if !ok {
		t.Fatal("a DenyOverrides set must implement Scoper")
	}
	cs, err := sc.Scope(context.Background(), Attributes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].column != "org_id" || cs[1].column != "role" {
		t.Fatalf("constraints = %+v", cs)
	}
}

func TestAllowAllIsNotAScoper(t *testing.T) {
	if _, ok := any(AllowAll).(Scoper); ok {
		t.Error("AllowAll must not implement Scoper")
	}
	if _, err := scopeOf(context.Background(), AllowAll, Attributes{}); err != nil {
		t.Error(err)
	}
}

func scopeRes() *Resource {
	org := &introspect.Column{Name: "org_id", Position: 1, DataType: "int8", Category: introspect.CatNumeric}
	blob := &introspect.Column{Name: "payload", Position: 2, DataType: "jsonb", Category: introspect.CatJSON}
	tbl := introspect.NewTable("public", "docs", false, true, "",
		[]*introspect.Column{org, blob}, []*introspect.Column{org}, nil)
	return &Resource{name: "docs", table: tbl}
}

func TestResolveConstraintsFailsClosed(t *testing.T) {
	res := scopeRes()

	if _, err := resolveConstraints(res, []Constraint{Eq("nope", 1)}); !errors.Is(err, ErrUnknownColumn) {
		t.Errorf("unknown column error = %v, want ErrUnknownColumn", err)
	}

	if _, err := resolveConstraints(res, []Constraint{Eq("payload", 1)}); !errors.Is(err, query.ErrOperatorNotAllowed) {
		t.Errorf("category error = %v, want ErrOperatorNotAllowed", err)
	}
}

func TestResolveConstraintsMapsOperators(t *testing.T) {
	res := scopeRes()
	fs, err := resolveConstraints(res, []Constraint{
		Eq("org_id", 7),
		Ne("org_id", 9),
		In("org_id", 1, 2, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []query.Operator{query.OpEq, query.OpNe, query.OpIn}
	for i, f := range fs {
		if f.Op != want[i] {
			t.Errorf("filter %d op = %q, want %q", i, f.Op, want[i])
		}
		if f.Col.Name != "org_id" {
			t.Errorf("filter %d resolved to column %q", i, f.Col.Name)
		}
	}
	if got := len(fs[2].Values); got != 3 {
		t.Errorf("In should carry 3 values, got %d", got)
	}

	if fs, err := resolveConstraints(res, nil); fs != nil || err != nil {
		t.Errorf("empty scope = %v, %v", fs, err)
	}
}

func TestWithScopeDoesNotAlias(t *testing.T) {
	res := scopeRes()
	col := res.table.Columns()[0]
	filters := make([]query.Filter, 1, 4)
	filters[0] = query.Filter{Col: col, Op: query.OpEq, Values: []any{1}}
	scope := []query.Filter{{Col: col, Op: query.OpEq, Values: []any{2}}}

	got := withScope(filters, scope)
	if len(got) != 2 {
		t.Fatalf("withScope len = %d, want 2", len(got))
	}
	if len(filters) != 1 {
		t.Error("withScope mutated the caller's filters")
	}
	if same := withScope(filters, nil); len(same) != 1 {
		t.Error("an empty scope must return the filters unchanged")
	}
}
