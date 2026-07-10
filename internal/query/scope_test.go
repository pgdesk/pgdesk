package query

import (
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func orgFilter(tbl *introspect.Table, v any) Filter {
	return Filter{Col: col(tbl, "id"), Op: OpNe, Values: []any{v}}
}

// A scope predicate must be ANDed into the WHERE clause with its own $N, sharing
// the placeholder counter with the key and SET values rather than colliding.
func TestScopeSharesPlaceholderNumbering(t *testing.T) {
	tbl := testTable()
	cols := tbl.Columns()
	scope := []Filter{orgFilter(tbl, int64(99))}

	sel, args, err := SelectRow(tbl, cols, tbl.PrimaryKey, []any{int64(42)}, VersionStrategy{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sel, `WHERE "id" = $1 AND "id" <> $2`) {
		t.Errorf("SelectRow scope not ANDed with fresh placeholder: %s", sel)
	}
	if len(args) != 2 || args[1] != int64(99) {
		t.Errorf("SelectRow args = %v", args)
	}

	upd, uargs, err := UpdateRow(tbl,
		[]*introspect.Column{col(tbl, "email")}, []any{"x@example.com"},
		tbl.PrimaryKey, []any{int64(42)}, "tok", VersionStrategy{}, nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	// SET is $1, key is $2, version is $3, so the scope must land on $4.
	if !strings.Contains(upd, `xmin::text = $3 AND "id" <> $4`) {
		t.Errorf("UpdateRow scope must follow the version guard: %s", upd)
	}
	if len(uargs) != 4 || uargs[3] != int64(99) {
		t.Errorf("UpdateRow args = %v", uargs)
	}

	del, dargs, err := DeleteRow(tbl, tbl.PrimaryKey, []any{int64(42)}, nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(del, `WHERE "id" = $1 AND "id" <> $2`) {
		t.Errorf("DeleteRow scope not ANDed: %s", del)
	}
	if len(dargs) != 2 {
		t.Errorf("DeleteRow args = %v", dargs)
	}
}

// ExistsRow answers "is this row reachable at all", so it carries the scope but
// never a version predicate -- that is the whole point of the 409-vs-404 probe.
func TestExistsRowHasScopeButNoVersion(t *testing.T) {
	tbl := testTable()
	sql, args, err := ExistsRow(tbl, tbl.PrimaryKey, []any{int64(1)}, []Filter{orgFilter(tbl, int64(2))})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "xmin") {
		t.Errorf("ExistsRow must not carry a version guard: %s", sql)
	}
	if !strings.Contains(sql, `SELECT 1 FROM "public"."users" WHERE "id" = $1 AND "id" <> $2`) {
		t.Errorf("unexpected probe: %s", sql)
	}
	if len(args) != 2 {
		t.Errorf("args = %v", args)
	}
	if _, _, err := ExistsRow(tbl, nil, nil, nil); err != ErrNoKey {
		t.Errorf("want ErrNoKey, got %v", err)
	}
}

// The bulk-action key check ORs the selected keys and ANDs the scope, so a key
// outside the scope simply is not counted.
func TestCountRowsInScope(t *testing.T) {
	tbl := testTable()
	// Single-column key: a DISTINCT count over an IN list (protocol-safe, and
	// DISTINCT so a duplicate or non-unique key cannot inflate the count).
	sql, args, err := CountRowsInScope(tbl, tbl.PrimaryKey,
		[][]any{{int64(1)}, {int64(2)}}, []Filter{orgFilter(tbl, int64(9))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `SELECT count(DISTINCT "id") FROM "public"."users" WHERE "id" IN ($1, $2) AND "id" <> $3`) {
		t.Errorf("unexpected count query: %s", sql)
	}
	if len(args) != 3 {
		t.Errorf("args = %v", args)
	}

	// Composite key falls back to OR-of-AND groups with a plain count(*).
	comp := twoColTable()
	csql, cargs, err := CountRowsInScope(comp, comp.PrimaryKey,
		[][]any{{int64(1), "a"}, {int64(2), "b"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csql, `SELECT count(*) FROM`) ||
		!strings.Contains(csql, `("a" = $1 AND "b" = $2) OR ("a" = $3 AND "b" = $4)`) {
		t.Errorf("unexpected composite count query: %s", csql)
	}
	if len(cargs) != 4 {
		t.Errorf("composite args = %v", cargs)
	}

	// A key tuple of the wrong arity is rejected.
	if _, _, err := CountRowsInScope(tbl, tbl.PrimaryKey, [][]any{{int64(1), int64(2)}}, nil); err != ErrKeyArity {
		t.Errorf("bad arity: want ErrKeyArity, got %v", err)
	}
	if _, _, err := CountRowsInScope(tbl, tbl.PrimaryKey, nil, nil); err != ErrKeyArity {
		t.Errorf("no keys: want ErrKeyArity, got %v", err)
	}
	if _, _, err := CountRowsInScope(tbl, nil, [][]any{{1}}, nil); err != ErrNoKey {
		t.Errorf("no key cols: want ErrNoKey, got %v", err)
	}
}

// A scope on the list rides in the same WHERE as the user's filters, so LIMIT
// applies after it and the n+1 lookahead stays correct.
func TestBuildListWithScope(t *testing.T) {
	tbl := testTable()
	sql, args, err := BuildList(tbl, ListParams{
		Columns: tbl.Columns(),
		Filters: []Filter{
			{Col: col(tbl, "email"), Op: OpILike, Values: []any{"%a%"}},
			orgFilter(tbl, int64(5)),
		},
		Limit: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `WHERE "email" ILIKE $1 AND "id" <> $2`) {
		t.Errorf("scope not ANDed into the list: %s", sql)
	}
	if !strings.Contains(sql, "LIMIT $3") {
		t.Errorf("LIMIT must follow the scope: %s", sql)
	}
	if len(args) != 4 { // ilike, scope, limit, offset
		t.Errorf("args = %v", args)
	}
}

// The <> operator must emit SQL, not fall through to the always-false default.
func TestEmitFilterNe(t *testing.T) {
	tbl := testTable()
	args := &Args{}
	got := emitFilter(args, orgFilter(tbl, int64(3)))
	if got != `"id" <> $1` {
		t.Errorf("emitFilter(ne) = %q", got)
	}
	if !OperatorAllowed(col(tbl, "email"), OpNe) {
		t.Error("ne must be permitted on text")
	}
	if _, err := ParseOperator(col(tbl, "email"), "ne"); err != nil {
		t.Errorf("ParseOperator(ne): %v", err)
	}
}
