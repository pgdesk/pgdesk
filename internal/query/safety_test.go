package query

import (
	"errors"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func TestIdentQuoting(t *testing.T) {
	cases := map[string]string{
		"email":         `"email"`,
		"user":          `"user"`,
		`a"b`:           `"a""b"`,
		`"; drop`:       `"""; drop"`,
		`col";--`:       `"col"";--"`,
		"weird\x00name": "\"weird\x00name\"",
	}
	for in, want := range cases {
		if got := Ident(in); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgsNumbering(t *testing.T) {
	a := &Args{}
	if got := a.Add("x"); got != "$1" {
		t.Fatalf("first placeholder = %q, want $1", got)
	}
	if got := a.Add(42); got != "$2" {
		t.Fatalf("second placeholder = %q, want $2", got)
	}
	if got := a.Add(true); got != "$3" {
		t.Fatalf("third placeholder = %q, want $3", got)
	}
	vals := a.Values()
	if len(vals) != 3 || vals[0] != "x" || vals[1] != 42 || vals[2] != true {
		t.Fatalf("values recorded out of order: %v", vals)
	}
}

func TestResolveColumnResolvesRealColumns(t *testing.T) {
	tbl := testTable()
	for _, name := range []string{"id", "email", "status", "created_at", "external_id"} {
		c, err := ResolveColumn(tbl, name)
		if err != nil {
			t.Fatalf("ResolveColumn(%q) failed: %v", name, err)
		}
		if c.Name != name {
			t.Fatalf("resolved wrong column: got %q want %q", c.Name, name)
		}
	}
}

func TestResolveColumnRejectsAdversarial(t *testing.T) {
	tbl := testTable()
	adversarial := []string{
		"",
		"nope",
		"id; drop table users",
		`"; drop`,
		"email OR 1=1",
		"email--",
		"email/*",
		"prefs->>'x'",
		"id::text",
		"EMAIL",
		" email ",
		"email\x00",
		"\u0442\u0435\u0441\u0442",
		"e\u2019",
		"1",
		"*",
		"count(*)",
	}
	for _, name := range adversarial {
		c, err := ResolveColumn(tbl, name)
		if !errors.Is(err, ErrUnknownColumn) {
			t.Errorf("ResolveColumn(%q) = (%v, %v), want ErrUnknownColumn", name, c, err)
		}
		if c != nil {
			t.Errorf("ResolveColumn(%q) returned a non-nil column on rejection", name)
		}
	}
}

func TestResolveColumnNilTable(t *testing.T) {
	if _, err := ResolveColumn(nil, "id"); !errors.Is(err, ErrUnknownColumn) {
		t.Fatalf("nil table: want ErrUnknownColumn, got %v", err)
	}
}

func TestOperatorWhitelistByCategory(t *testing.T) {
	all := []Operator{OpEq, OpILike, OpIn, OpLt, OpGt, OpBetween, OpIsNull}
	want := map[introspect.TypeCategory]map[Operator]bool{
		introspect.CatText:      {OpEq: true, OpILike: true, OpIn: true, OpIsNull: true},
		introspect.CatNumeric:   {OpEq: true, OpLt: true, OpGt: true, OpBetween: true, OpIn: true, OpIsNull: true},
		introspect.CatTimestamp: {OpEq: true, OpLt: true, OpGt: true, OpBetween: true, OpIsNull: true},
		introspect.CatBool:      {OpEq: true, OpIsNull: true},
		introspect.CatEnum:      {OpEq: true, OpIn: true, OpIsNull: true},
		introspect.CatUUID:      {OpEq: true, OpIn: true, OpIsNull: true},
		introspect.CatJSON:      {OpIsNull: true},
		introspect.CatOther:     {OpEq: true, OpIsNull: true},
	}
	for cat, allowed := range want {
		c := &introspect.Column{Name: "x", Category: cat}
		for _, op := range all {
			got := OperatorAllowed(c, op)
			if got != allowed[op] {
				t.Errorf("category %s op %s: OperatorAllowed=%v, want %v", cat, op, got, allowed[op])
			}
		}
	}
}

func TestParseOperatorRejectsGarbage(t *testing.T) {
	c := &introspect.Column{Name: "email", Category: introspect.CatText}
	for _, tok := range []string{"", "eq; drop", "DELETE", "=", "ilike ", "between", "lt"} {
		if _, err := ParseOperator(c, tok); !errors.Is(err, ErrOperatorNotAllowed) {
			t.Errorf("ParseOperator(text, %q) = %v, want ErrOperatorNotAllowed", tok, err)
		}
	}
	if op, err := ParseOperator(c, "ilike"); err != nil || op != OpILike {
		t.Errorf("ParseOperator(text, ilike) = (%v,%v), want (ilike,nil)", op, err)
	}
}

func TestEnumFilterValuesValidated(t *testing.T) {
	tbl := testTable()
	status := col(tbl, "status")
	if !status.ValidEnumLabel("active") {
		t.Fatal("active should be a valid label")
	}
	for _, bad := range []string{"ACTIVE", "deleted", "active'; drop", ""} {
		if status.ValidEnumLabel(bad) {
			t.Errorf("ValidEnumLabel(%q) = true, want false", bad)
		}
	}
}

func TestBuildRowAndUpdateAreParameterized(t *testing.T) {
	tbl := testTable()
	cols := tbl.Columns()

	sel, args, err := SelectRow(tbl, cols, tbl.PrimaryKey, []any{int64(42)}, VersionStrategy{}, nil)
	if err != nil {
		t.Fatalf("SelectRow: %v", err)
	}
	if !strings.Contains(sel, `"id" = $1`) {
		t.Errorf("SelectRow WHERE not parameterized: %s", sel)
	}
	if !strings.Contains(sel, "xmin::text AS __pgdesk_version") {
		t.Errorf("SelectRow missing xmin version token: %s", sel)
	}
	if len(args) != 1 || args[0] != int64(42) {
		t.Errorf("SelectRow args = %v, want [42]", args)
	}

	upd, uargs, err := UpdateRow(tbl,
		[]*introspect.Column{col(tbl, "email")}, []any{"new@example.com"},
		tbl.PrimaryKey, []any{int64(42)}, "12345", VersionStrategy{},
		[]*introspect.Column{col(tbl, "id"), col(tbl, "email")}, nil)
	if err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	if !strings.Contains(upd, `SET "email" = $1`) {
		t.Errorf("UpdateRow SET not parameterized: %s", upd)
	}
	if !strings.Contains(upd, `"id" = $2`) {
		t.Errorf("UpdateRow key predicate wrong: %s", upd)
	}
	if !strings.Contains(upd, "xmin::text = $3") {
		t.Errorf("UpdateRow missing xmin guard: %s", upd)
	}
	if !strings.Contains(upd, `RETURNING "id", "email"`) {
		t.Errorf("UpdateRow missing RETURNING: %s", upd)
	}
	if len(uargs) != 3 {
		t.Errorf("UpdateRow args = %v, want 3", uargs)
	}
}

func TestUpdateRowWithVersionColumn(t *testing.T) {
	tbl := testTable()
	verCol := &introspect.Column{Name: "version", Category: introspect.CatNumeric}
	upd, _, err := UpdateRow(tbl,
		[]*introspect.Column{col(tbl, "email")}, []any{"x"},
		tbl.PrimaryKey, []any{int64(1)}, "7", VersionStrategy{Column: verCol}, nil, nil)
	if err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	if !strings.Contains(upd, `"version"::text = $3`) {
		t.Errorf("expected version column guard, got: %s", upd)
	}
	if strings.Contains(upd, "xmin") {
		t.Errorf("should not use xmin when version column set: %s", upd)
	}
}
