package pgdesk

import (
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func numCol(name string) *introspect.Column {
	return &introspect.Column{Name: name, DataType: "int8", Category: introspect.CatNumeric}
}
func textCol(name string) *introspect.Column {
	return &introspect.Column{Name: name, DataType: "text", Category: introspect.CatText}
}
func uuidCol(name string) *introspect.Column {
	return &introspect.Column{Name: name, DataType: "uuid", Category: introspect.CatUUID}
}

func TestKeysInt64s(t *testing.T) {
	k := Keys{
		cols: []*introspect.Column{numCol("id")},
		vals: [][]any{{int64(1)}, {int64(2)}, {int64(3)}},
	}
	if k.Len() != 3 {
		t.Fatalf("Len = %d, want 3", k.Len())
	}
	ids, err := k.Int64s()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("Int64s = %v", ids)
	}
	// The result is a concrete []int64 — the whole point, since a []any fails to
	// encode under pgx's PgBouncer-compatible modes.
	if _, ok := any(ids).([]int64); !ok {
		t.Error("Int64s must return a concrete []int64")
	}
}

func TestKeysStrings(t *testing.T) {
	for _, col := range []*introspect.Column{textCol("slug"), uuidCol("id")} {
		k := Keys{cols: []*introspect.Column{col}, vals: [][]any{{"a"}, {"b"}}}
		ss, err := k.Strings()
		if err != nil {
			t.Fatalf("%s: %v", col.DataType, err)
		}
		if len(ss) != 2 || ss[0] != "a" {
			t.Errorf("%s: Strings = %v", col.DataType, ss)
		}
	}
}

func TestKeysWrongAccessorErrors(t *testing.T) {
	// Asking for Int64s on a text key names the actual column type.
	k := Keys{cols: []*introspect.Column{textCol("slug")}, vals: [][]any{{"a"}}}
	_, err := k.Int64s()
	if err == nil || !strings.Contains(err.Error(), "slug") || !strings.Contains(err.Error(), "text") {
		t.Errorf("Int64s on text key: %v", err)
	}

	// Strings on an integer key likewise.
	k = Keys{cols: []*introspect.Column{numCol("id")}, vals: [][]any{{int64(1)}}}
	if _, err := k.Strings(); err == nil {
		t.Error("Strings on integer key should error")
	}
}

func TestKeysCompositeUsesRaw(t *testing.T) {
	k := Keys{
		cols: []*introspect.Column{numCol("org_id"), textCol("slug")},
		vals: [][]any{{int64(1), "a"}, {int64(2), "b"}},
	}
	// A typed accessor refuses a composite key and says to use Raw.
	if _, err := k.Int64s(); err == nil || !strings.Contains(err.Error(), "Raw") {
		t.Errorf("Int64s on composite key: %v", err)
	}
	raw := k.Raw()
	if len(raw) != 2 || raw[1][1] != "b" {
		t.Errorf("Raw = %v", raw)
	}
}
