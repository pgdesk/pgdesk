package render

import (
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestFormatValueRendersPgtypeWrappers(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"numeric integral", pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}, "1"},
		{"numeric scaled", pgtype.Numeric{Int: big.NewInt(1050), Exp: -2, Valid: true}, "10.50"},
		{"numeric negative", pgtype.Numeric{Int: big.NewInt(-25), Exp: -1, Valid: true}, "-2.5"},
		{"numeric NaN", pgtype.Numeric{NaN: true, Valid: true}, "NaN"},
		{"time", pgtype.Time{Microseconds: 45296000000, Valid: true}, "12:34:56.000000"},
		{"interval", pgtype.Interval{Days: 1, Valid: true}, "1 day 00:00:00"},
		{"bits", pgtype.Bits{Bytes: []byte{160}, Len: 3, Valid: true}, "101"},
		{"point", pgtype.Point{P: pgtype.Vec2{X: 1, Y: 2}, Valid: true}, "(1,2)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(tt.in); got != tt.want {
				t.Errorf("FormatValue(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatValueInvalidWrapperIsEmpty(t *testing.T) {
	for _, in := range []any{pgtype.Numeric{}, pgtype.Time{}, pgtype.Interval{}, pgtype.Bits{}} {
		if got := FormatValue(in); got != "" {
			t.Errorf("FormatValue(%#v) = %q, want empty string for SQL NULL", in, got)
		}
	}
}

func TestFormatValueUUID(t *testing.T) {
	raw := [16]byte{0x48, 0x3f, 0x50, 0x6f, 0x5d, 0x40, 0x4c, 0xa5, 0x8a, 0x65, 0x7f, 0x6d, 0x99, 0x7b, 0xe5, 0xba}
	want := "483f506f-5d40-4ca5-8a65-7f6d997be5ba"
	if got := FormatValue(raw); got != want {
		t.Errorf("FormatValue(uuid) = %q, want %q", got, want)
	}
}

func TestFormatValueJSON(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"object", map[string]any{"a": float64(1)}, `{"a":1}`},
		{"array", []any{float64(1), "two"}, `[1,"two"]`},
		{"numeric inside json", []any{pgtype.Numeric{Int: big.NewInt(15), Exp: -1, Valid: true}}, `[1.5]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(tt.in); got != tt.want {
				t.Errorf("FormatValue(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
