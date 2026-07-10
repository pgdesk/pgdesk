package query

import (
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func TestBuildListParameterized(t *testing.T) {
	tbl := testTable()
	cols := []*introspect.Column{col(tbl, "id"), col(tbl, "email")}

	sql, args, err := BuildList(tbl, ListParams{
		Columns:  cols,
		Sort:     col(tbl, "created_at"),
		SortDesc: true,
		Limit:    51,
		Offset:   100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `SELECT "id", "email" FROM "public"."users"`) {
		t.Errorf("unexpected select: %s", sql)
	}
	if !strings.Contains(sql, `ORDER BY "created_at" DESC`) {
		t.Errorf("missing sort: %s", sql)
	}
	if !strings.Contains(sql, "LIMIT $1 OFFSET $2") {
		t.Errorf("limit/offset not parameterized: %s", sql)
	}
	if len(args) != 2 || args[0] != 51 || args[1] != 100 {
		t.Errorf("args = %v, want [51 100]", args)
	}
}

// TestEmitFilterByOperator asserts each operator renders a parameterized clause
// with a quoted catalog identifier -- never a request string in the SQL (D3).
func TestEmitFilterByOperator(t *testing.T) {
	tbl := testTable()
	cases := []struct {
		name    string
		f       Filter
		wantSQL string
		wantN   int
	}{
		{"eq", Filter{Col: col(tbl, "status"), Op: OpEq, Values: []any{"active"}}, `"status" = $1`, 1},
		{"ilike", Filter{Col: col(tbl, "email"), Op: OpILike, Values: []any{"%a%"}}, `"email" ILIKE $1`, 1},
		{"lt", Filter{Col: col(tbl, "created_at"), Op: OpLt, Values: []any{"x"}}, `"created_at" < $1`, 1},
		{"gt", Filter{Col: col(tbl, "created_at"), Op: OpGt, Values: []any{"x"}}, `"created_at" > $1`, 1},
		{"between", Filter{Col: col(tbl, "created_at"), Op: OpBetween, Values: []any{"a", "b"}}, `"created_at" BETWEEN $1 AND $2`, 2},
		{"in", Filter{Col: col(tbl, "status"), Op: OpIn, Values: []any{"a", "b", "c"}}, `"status" IN ($1, $2, $3)`, 3},
		{"isnull true", Filter{Col: col(tbl, "email"), Op: OpIsNull, Values: []any{true}}, `"email" IS NULL`, 0},
		{"isnull false", Filter{Col: col(tbl, "email"), Op: OpIsNull, Values: []any{false}}, `"email" IS NOT NULL`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := &Args{}
			got := emitFilter(args, tc.f)
			if got != tc.wantSQL {
				t.Errorf("emitFilter = %q, want %q", got, tc.wantSQL)
			}
			if args.Len() != tc.wantN {
				t.Errorf("placeholder count = %d, want %d", args.Len(), tc.wantN)
			}
		})
	}
}

func TestBuildListWithFiltersAndSearch(t *testing.T) {
	tbl := testTable()
	sql, args, err := BuildList(tbl, ListParams{
		Columns: []*introspect.Column{col(tbl, "id")},
		Filters: []Filter{
			{Col: col(tbl, "status"), Op: OpEq, Values: []any{"active"}},
			{Col: col(tbl, "is_admin"), Op: OpEq, Values: []any{true}},
		},
		Search: &SearchSpec{Cols: []*introspect.Column{col(tbl, "email"), col(tbl, "full_name")}, Term: "%ada%"},
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `WHERE "status" = $1 AND "is_admin" = $2 AND ("email" ILIKE $3 OR "full_name" ILIKE $3)`) {
		t.Fatalf("unexpected WHERE: %s", sql)
	}
	// $3 shared by the search group; $4/$5 are limit/offset.
	if len(args) != 5 || args[0] != "active" || args[1] != true || args[2] != "%ada%" {
		t.Fatalf("args = %v", args)
	}
}

func TestBuildFKLabels(t *testing.T) {
	ref := testTable() // reuse as a stand-in referenced table
	sql, args := BuildFKLabels(ref, col(ref, "id"), col(ref, "email"), []any{int64(1), int64(2)}, nil)
	// Individual $N placeholders, not "= ANY($1)": a []any array arg fails to
	// encode under pgx's PgBouncer-compatible modes.
	if !strings.Contains(sql, `SELECT "id", "email" FROM "public"."users" WHERE "id" IN ($1, $2)`) {
		t.Fatalf("unexpected FK label SQL: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("FK label args = %v, want one per key", args)
	}
}

func TestParseScalarTypeAware(t *testing.T) {
	tbl := testTable()
	if v, err := ParseScalar(col(tbl, "id"), "42"); err != nil || v != int64(42) {
		t.Errorf("numeric parse: %v %v", v, err)
	}
	if _, err := ParseScalar(col(tbl, "id"), "'; drop"); err == nil {
		t.Error("numeric parse should reject injection-shaped input")
	}
	if _, err := ParseScalar(col(tbl, "status"), "bogus"); err == nil {
		t.Error("enum parse should reject a non-label")
	}
	if v, err := ParseScalar(col(tbl, "status"), "active"); err != nil || v != "active" {
		t.Errorf("enum parse: %v %v", v, err)
	}
}
