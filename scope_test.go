package pgdesk

import (
	"context"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

func TestScopeOnly(t *testing.T) {
	fn := func(_ context.Context, a Attributes) ([]Constraint, error) {
		return []Constraint{Eq("org_id", a.Principal.SubjectID())}, nil
	}
	az := ScopeOnly(fn)

	dec, err := az.Authorize(context.Background(), Attributes{})
	if err != nil || dec != Abstain {
		t.Fatalf("ScopeOnly authorizer = %v (%v), want Abstain", dec, err)
	}

	sc, ok := az.(Scoper)
	if !ok {
		t.Fatal("ScopeOnly result must implement Scoper")
	}
	cs, err := sc.Scope(context.Background(), Attributes{Principal: testPrincipal{"u7"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("scope = %+v", cs)
	}

	var s stateful
	if _, ok := ScopeOnly(s.Scope).(Scoper); !ok {
		t.Error("ScopeOnly must accept a method value")
	}
}

type stateful struct{}

func (stateful) Scope(context.Context, Attributes) ([]Constraint, error) { return nil, nil }

func writeTable() *introspect.Table {
	org := &introspect.Column{Name: "org_id", Position: 1, DataType: "int8", Category: introspect.CatNumeric}
	name := &introspect.Column{Name: "name", Position: 2, DataType: "text", Category: introspect.CatText}
	id := &introspect.Column{Name: "id", Position: 3, DataType: "int8", Category: introspect.CatNumeric}
	return introspect.NewTable("public", "docs", false, true, "",
		[]*introspect.Column{org, name, id}, []*introspect.Column{id}, nil)
}

func f(col *introspect.Column, op query.Operator, v any) query.Filter {
	return query.Filter{Col: col, Op: op, Values: []any{v}}
}

func TestEnforceScopeOverwritesSubmitted(t *testing.T) {
	tbl := writeTable()
	orgCol, _ := tbl.Column("org_id")
	nameCol, _ := tbl.Column("name")

	cols := []*introspect.Column{orgCol, nameCol}
	vals := []any{int64(2), "x"}
	scope := []query.Filter{f(orgCol, query.OpEq, int64(1))}

	gotCols, gotVals := enforceScope(cols, vals, scope)
	if len(gotCols) != 2 {
		t.Fatalf("cols grew unexpectedly: %v", gotCols)
	}
	if gotVals[0] != int64(1) {
		t.Errorf("org_id = %v, want the scope value 1 (submitted 2 was overridden)", gotVals[0])
	}
	if gotVals[1] != "x" {
		t.Errorf("unscoped name = %v, want x", gotVals[1])
	}

	if vals[0] != int64(2) {
		t.Error("enforceScope mutated the caller's values")
	}
}

func TestEnforceScopeAddsMissing(t *testing.T) {
	tbl := writeTable()
	orgCol, _ := tbl.Column("org_id")
	nameCol, _ := tbl.Column("name")

	cols := []*introspect.Column{nameCol}
	vals := []any{"x"}
	scope := []query.Filter{f(orgCol, query.OpEq, int64(1))}

	gotCols, gotVals := enforceScope(cols, vals, scope)
	if len(gotCols) != 2 || gotCols[1].Name != "org_id" || gotVals[1] != int64(1) {
		t.Fatalf("org_id was not appended: cols=%v vals=%v", gotCols, gotVals)
	}
}

func TestEnforceScopeIgnoresNonEquality(t *testing.T) {
	tbl := writeTable()
	idCol, _ := tbl.Column("id")
	nameCol, _ := tbl.Column("name")

	cols := []*introspect.Column{nameCol}
	vals := []any{"x"}
	scope := []query.Filter{f(idCol, query.OpNe, int64(7))}

	gotCols, gotVals := enforceScope(cols, vals, scope)
	if len(gotCols) != 1 || gotVals[0] != "x" {
		t.Errorf("Ne scope should not touch the write set: cols=%v vals=%v", gotCols, gotVals)
	}
}
