package render

import (
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// FormatValue renders a scanned DB value as a plain display string. The template
// escapes the result, so this returns text and never markup (F1). SQL NULL (nil)
// renders as an empty string.
//
// pgx decodes many PostgreSQL types into wrapper structs rather than Go scalars
// (numeric, time, interval, bit, point), into a byte array (uuid), or into Go
// maps and slices (json, jsonb). Go's default formatting of those is a struct
// dump, which is both unreadable and -- because the edit form pre-fills from this
// same function -- not re-submittable, making such a row unsavable. Each is
// rendered as PostgreSQL's own text form instead, so what an operator reads is
// also what they can send back.
func FormatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return formatBytes(x)
	case time.Time:
		return formatTime(x)
	case [16]byte:
		// uuid: an array, not a slice, so it misses the []byte path above.
		return formatUUID(x)
	case map[string]any, []any:
		// json/jsonb decode to Go containers; render them as JSON text.
		return formatJSON(x)
	case fmt.Stringer:
		return x.String()
	case driver.Valuer:
		return formatValuer(x)
	default:
		return fmt.Sprint(x)
	}
}

// formatBytes renders a byte slice. bytea and text-ish blobs both arrive this
// way: valid UTF-8 renders as its text, while genuine binary is hex-encoded with
// a leading \x (the PostgreSQL bytea convention) so it never dumps raw bytes into
// HTML or corrupts CSV.
func formatBytes(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return `\x` + hex.EncodeToString(b)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// formatUUID renders pgx's [16]byte uuid in the canonical 8-4-4-4-12 form.
func formatUUID(u [16]byte) string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// formatJSON marshals a decoded json/jsonb container back to JSON text. On a
// marshal failure the Go rendering is returned rather than an empty cell: showing
// something imperfect beats silently hiding a stored value.
func formatJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// formatValuer renders a pgtype wrapper through its driver.Valuer, which yields
// the value's canonical PostgreSQL text form (10.50, 12:34:56.000000, (1,2)).
// An invalid wrapper is SQL NULL and yields nil, hence the empty string. A
// Valuer that errors falls back to the Go rendering for the same reason
// formatJSON does -- the operator still sees that a value is present.
func formatValuer(v driver.Valuer) string {
	dv, err := v.Value()
	if err != nil {
		return fmt.Sprint(v)
	}
	// driver.Value is a closed set of scalars, so this cannot re-enter the
	// Valuer case and recurse.
	switch d := dv.(type) {
	case nil:
		return ""
	case string:
		return d
	case []byte:
		return formatBytes(d)
	case time.Time:
		return formatTime(d)
	default:
		return fmt.Sprint(d)
	}
}
