package pgdesk

import (
	"errors"
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
	// The result is a concrete []int64 -- the whole point, since a []any fails to
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
	if !errors.Is(err, ErrKeyTypeMismatch) {
		t.Errorf("Int64s on text key: err = %v, want errors.Is ErrKeyTypeMismatch", err)
	}

	// Strings on an integer key likewise.
	k = Keys{cols: []*introspect.Column{numCol("id")}, vals: [][]any{{int64(1)}}}
	if _, err := k.Strings(); err == nil {
		t.Error("Strings on integer key should error")
	} else if !errors.Is(err, ErrKeyTypeMismatch) {
		t.Errorf("Strings on integer key: err = %v, want errors.Is ErrKeyTypeMismatch", err)
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

func TestKeysAccessorErrorSentinels(t *testing.T) {
	composite := Keys{
		cols: []*introspect.Column{numCol("org_id"), textCol("slug")},
		vals: [][]any{{int64(1), "a"}, {int64(2), "b"}},
	}

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "Int64s on composite key wraps ErrKeyShapeMismatch",
			err:  errIgnoreValue(composite.Int64s()),
			want: ErrKeyShapeMismatch,
		},
		{
			name: "Strings on composite key wraps ErrKeyShapeMismatch",
			err:  errIgnoreValue(composite.Strings()),
			want: ErrKeyShapeMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(tt.err, tt.want) {
				t.Errorf("err = %v, want errors.Is %v", tt.err, tt.want)
			}
		})
	}
}

// errIgnoreValue discards a (value, error) accessor result down to just the
// error, so table-driven cases can share one shape regardless of the
// accessor's return type.
func errIgnoreValue[T any](_ T, err error) error { return err }

func TestKeysStringsWrongTypeWrapsErrKeyTypeMismatch(t *testing.T) {
	// The column claims text, but the decoded value is not a string --
	// e.g. a driver decode mismatch. Strings must still surface a sentinel.
	k := Keys{cols: []*introspect.Column{textCol("slug")}, vals: [][]any{{42}}}
	_, err := k.Strings()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrKeyTypeMismatch) {
		t.Errorf("err = %v, want errors.Is ErrKeyTypeMismatch", err)
	}
}

func TestKeysColumn(t *testing.T) {
	k := Keys{
		cols: []*introspect.Column{numCol("org_id"), textCol("slug")},
		vals: [][]any{{int64(1), "a"}, {int64(2), "b"}, {int64(3), "c"}},
	}

	orgIDs, err := k.Column("org_id")
	if err != nil {
		t.Fatalf("Column(org_id): %v", err)
	}
	if want := []any{int64(1), int64(2), int64(3)}; !equalAnySlices(orgIDs, want) {
		t.Errorf("Column(org_id) = %v, want %v", orgIDs, want)
	}

	slugs, err := k.Column("slug")
	if err != nil {
		t.Fatalf("Column(slug): %v", err)
	}
	if want := []any{"a", "b", "c"}; !equalAnySlices(slugs, want) {
		t.Errorf("Column(slug) = %v, want %v", slugs, want)
	}

	if _, err := k.Column("nope"); err == nil {
		t.Error("Column on unknown name should error")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Errorf("Column on unknown name: %v", err)
	}
}

func equalAnySlices(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
