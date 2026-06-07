package query

import (
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func TestInsertRowParameterized(t *testing.T) {
	tbl := testTable()
	cols := []*introspect.Column{col(tbl, "email"), col(tbl, "status")}
	sql, args, err := InsertRow(tbl, cols, []any{"a@b.com", "active"},
		[]*introspect.Column{col(tbl, "id"), col(tbl, "email")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `INSERT INTO "public"."users" ("email", "status") VALUES ($1, $2)`) {
		t.Errorf("unexpected insert: %s", sql)
	}
	if !strings.Contains(sql, `RETURNING "id", "email"`) {
		t.Errorf("missing RETURNING: %s", sql)
	}
	if len(args) != 2 || args[0] != "a@b.com" {
		t.Errorf("args = %v", args)
	}
}

func TestInsertRowDefaultValues(t *testing.T) {
	tbl := testTable()
	sql, args, err := InsertRow(tbl, nil, nil, []*introspect.Column{col(tbl, "id")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "DEFAULT VALUES") {
		t.Errorf("empty insert should use DEFAULT VALUES: %s", sql)
	}
	if len(args) != 0 {
		t.Errorf("no args expected, got %v", args)
	}
}

func TestInsertRowMismatch(t *testing.T) {
	tbl := testTable()
	if _, _, err := InsertRow(tbl, []*introspect.Column{col(tbl, "email")}, []any{}, nil); err != ErrColumnValueMismatch {
		t.Errorf("want ErrColumnValueMismatch, got %v", err)
	}
}

func TestDeleteRowParameterized(t *testing.T) {
	tbl := testTable()
	sql, args, err := DeleteRow(tbl, tbl.PrimaryKey, []any{int64(7)},
		[]*introspect.Column{col(tbl, "id"), col(tbl, "email")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `DELETE FROM "public"."users" WHERE "id" = $1`) {
		t.Errorf("unexpected delete: %s", sql)
	}
	if !strings.Contains(sql, `RETURNING "id", "email"`) {
		t.Errorf("missing RETURNING: %s", sql)
	}
	if len(args) != 1 || args[0] != int64(7) {
		t.Errorf("args = %v", args)
	}
}

func TestDeleteRowNoKey(t *testing.T) {
	tbl := testTable()
	if _, _, err := DeleteRow(tbl, nil, nil, nil); err != ErrNoKey {
		t.Errorf("want ErrNoKey, got %v", err)
	}
}
