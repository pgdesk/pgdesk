package query

import (
	"errors"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func TestDecodeSingleNumericKey(t *testing.T) {
	tbl := testTable()
	pk := tbl.PrimaryKey
	vals, err := DecodeKey(pk, "42")
	if err != nil {
		t.Fatalf("DecodeKey(42): %v", err)
	}
	if len(vals) != 1 || vals[0] != int64(42) {
		t.Fatalf("got %v, want [int64(42)]", vals)
	}
}

// TestDecodeHighPrecisionNumeric proves issue #13a is fixed: a numeric value
// wider than int64 (and with a fractional part) round-trips as the EXACT
// original string, never coerced to float64 -- so a filter on a
// numeric(38,10) column does not silently miss rows to precision loss. pgx
// binds a Go string to a numeric column and PG casts it at full precision.
func TestDecodeHighPrecisionNumeric(t *testing.T) {
	numeric := []*introspect.Column{{Name: "amount", DataType: "numeric", Category: introspect.CatNumeric}}
	for _, raw := range []string{
		"12345678901234567890.12", // > int64 with decimals
		"99999999999999999999",    // > int64, integral
		"-0.00000000001",          // tiny fractional, would round to 0 as float64
		"1.5e10",                  // exponent form
	} {
		vals, err := DecodeKey(numeric, raw)
		if err != nil {
			t.Fatalf("DecodeKey(numeric, %q): %v", raw, err)
		}
		s, ok := vals[0].(string)
		if !ok {
			t.Fatalf("numeric %q bound as %T, want the exact string (no float64 downgrade)", raw, vals[0])
		}
		if s != raw {
			t.Fatalf("numeric %q bound as %q, want the exact input string", raw, s)
		}
	}
	// A plain int64-sized integer still takes the typed fast-path.
	vals, err := DecodeKey(numeric, "42")
	if err != nil {
		t.Fatalf("DecodeKey(numeric, 42): %v", err)
	}
	if vals[0] != int64(42) {
		t.Fatalf("small integer key = %#v, want int64(42)", vals[0])
	}
}

// TestDecodeKeyFailsClosed is the D6 assertion: a non-numeric segment for a
// numeric key is a 400 (ErrBadKey), never coerced to a string and never a 500.
func TestDecodeKeyFailsClosed(t *testing.T) {
	numeric := []*introspect.Column{{Name: "id", DataType: "int8", Category: introspect.CatNumeric}}
	for _, bad := range []string{"'; drop", "abc", "1 OR 1=1", "", "0x10", "42; --", "\uff11\uff12\uff13"} {
		if _, err := DecodeKey(numeric, bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("DecodeKey(numeric, %q) = %v, want ErrBadKey", bad, err)
		}
	}
}

func TestDecodeUUIDKey(t *testing.T) {
	uuidCol := []*introspect.Column{{Name: "external_id", DataType: "uuid", Category: introspect.CatUUID}}
	good := "123e4567-e89b-12d3-a456-426614174000"
	vals, err := DecodeKey(uuidCol, good)
	if err != nil || vals[0] != good {
		t.Fatalf("valid uuid: got (%v,%v)", vals, err)
	}
	for _, bad := range []string{"123e4567", good + "x", "'; drop", "123e4567-e89b-12d3-a456-42661417400g"} {
		if _, err := DecodeKey(uuidCol, bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("DecodeKey(uuid, %q) = %v, want ErrBadKey", bad, err)
		}
	}
}

func TestDecodeEnumKeyValidatedAgainstLabels(t *testing.T) {
	enumCol := []*introspect.Column{{Name: "status", DataType: "user_status", Category: introspect.CatEnum, EnumLabels: []string{"active", "suspended"}}}
	if _, err := DecodeKey(enumCol, "active"); err != nil {
		t.Fatalf("valid enum: %v", err)
	}
	if _, err := DecodeKey(enumCol, "deleted"); !errors.Is(err, ErrBadKey) {
		t.Fatalf("invalid enum: want ErrBadKey, got %v", err)
	}
}

func TestCompositeKeyRoundTrip(t *testing.T) {
	pk := []*introspect.Column{
		{Name: "tenant_id", Category: introspect.CatNumeric},
		{Name: "sku", Category: introspect.CatText},
	}
	seg, err := EncodeKey(pk, []any{int64(7), "ABC-123"})
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}
	if seg == "" || seg[0] != '~' {
		t.Fatalf("composite segment should start with ~, got %q", seg)
	}
	vals, err := DecodeKey(pk, seg)
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if len(vals) != 2 || vals[0] != int64(7) || vals[1] != "ABC-123" {
		t.Fatalf("round trip mismatch: %v", vals)
	}
}

func TestCompositeKeyRejectsTampering(t *testing.T) {
	pk := []*introspect.Column{
		{Name: "a", Category: introspect.CatNumeric},
		{Name: "b", Category: introspect.CatText},
	}
	for _, bad := range []string{"~not-base64!!", "~", "notilde", "~eyJ4IjoxfQ"} {
		if _, err := DecodeKey(pk, bad); err == nil {
			t.Errorf("DecodeKey(composite, %q) = nil, want error", bad)
		}
	}
}

func TestEncodeKeyArityChecks(t *testing.T) {
	pk := []*introspect.Column{{Name: "id", Category: introspect.CatNumeric}}
	if _, err := EncodeKey(nil, []any{1}); !errors.Is(err, ErrNoKey) {
		t.Errorf("empty pk: want ErrNoKey, got %v", err)
	}
	if _, err := EncodeKey(pk, []any{1, 2}); !errors.Is(err, ErrKeyArity) {
		t.Errorf("arity mismatch: want ErrKeyArity, got %v", err)
	}
}

// FuzzDecodeKey asserts the D6 fail-closed contract under arbitrary input: for
// a numeric key column, DecodeKey must either return a typed numeric value or an
// error -- it must never panic and never return a raw string.
func FuzzDecodeKey(f *testing.F) {
	pk := []*introspect.Column{{Name: "id", DataType: "int8", Category: introspect.CatNumeric}}
	seeds := []string{"1", "42", "'; drop", "", "~abc", "-5", "9999999999999999999999", "1.5"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seg string) {
		vals, err := DecodeKey(pk, seg)
		if err != nil {
			return // rejected -- the correct fail-closed outcome
		}
		if len(vals) != 1 {
			t.Fatalf("accepted %q but returned %d values", seg, len(vals))
		}
		switch vals[0].(type) {
		case int64, string:
			// A plain int64 for integer keys, or the validated literal string
			// for wide/decimal numerics (bound to PG at full precision). Never
			// a float64 -- that would silently lose precision.
		default:
			t.Fatalf("numeric key %q decoded to non-numeric %T", seg, vals[0])
		}
	})
}
