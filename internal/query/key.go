package query

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// Key encoding/decoding (D6). A resource's key is an ordered []*Column that may
// be empty. Detail/edit/delete only exist for keyed resources.
//
//   - Single-column key  -> readable segment: url.PathEscape(fmt.Sprint(v)).
//   - Composite key       -> opaque segment: "~" + base64url(json([]string vals)).
//
// Decoding is type-aware and fail-closed: each segment is parsed into its
// column's Go type. A parse failure is a 400 (ErrBadKey), never a 500 and never
// a fallback to string comparison -- so "'; drop" fails to decode as an integer
// key and never reaches the query builder.
var (
	// ErrBadKey indicates a URL key segment that could not be decoded into the
	// resource's key column types. Callers translate it to HTTP 400.
	ErrBadKey = errors.New("pgdesk/query: key does not decode to the resource key type")
	// ErrKeyArity indicates a segment whose component count does not match the
	// number of key columns.
	ErrKeyArity = errors.New("pgdesk/query: key arity mismatch")
	// ErrNoKey indicates key operations attempted on a keyless resource.
	ErrNoKey = errors.New("pgdesk/query: resource has no key")
)

const compositePrefix = "~"

var keyEnc = base64.RawURLEncoding

// EncodeKey renders the URL path segment for a row's primary-key values. len(pk)
// must equal len(vals) and be > 0.
func EncodeKey(pk []*introspect.Column, vals []any) (string, error) {
	if len(pk) == 0 {
		return "", ErrNoKey
	}
	if len(pk) != len(vals) {
		return "", ErrKeyArity
	}
	if len(pk) == 1 {
		return url.PathEscape(fmt.Sprint(vals[0])), nil
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprint(v)
	}
	blob, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("pgdesk/query: encoding composite key: %w", err)
	}
	return compositePrefix + keyEnc.EncodeToString(blob), nil
}

// DecodeKey parses a URL path segment (already unescaped by net/http PathValue)
// back into typed key values matching pk, fail-closed. The returned values are
// safe to pass as query args.
func DecodeKey(pk []*introspect.Column, seg string) ([]any, error) {
	if len(pk) == 0 {
		return nil, ErrNoKey
	}
	if len(pk) == 1 {
		v, err := decodeValue(pk[0], seg)
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	}

	if len(seg) == 0 || seg[0] != compositePrefix[0] {
		return nil, ErrBadKey
	}
	blob, err := keyEnc.DecodeString(seg[1:])
	if err != nil {
		return nil, ErrBadKey
	}
	var parts []string
	if err := json.Unmarshal(blob, &parts); err != nil {
		return nil, ErrBadKey
	}
	if len(parts) != len(pk) {
		return nil, ErrKeyArity
	}
	vals := make([]any, len(pk))
	for i, col := range pk {
		v, err := decodeValue(col, parts[i])
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

// ErrBadFilterValue indicates a filter value that does not parse into its
// column's type. Callers translate it to HTTP 400, fail-closed (D3).
var ErrBadFilterValue = errors.New("pgdesk/query: filter value does not match column type")

// errParse is the neutral sentinel returned by parseScalar; the key and filter
// wrappers re-wrap it with their own domain sentinel.
var errParse = errors.New("value does not match column type")

// decodeValue parses one raw key segment into its column's Go type (ErrBadKey on
// failure). It never coerces unparseable input to a raw string comparison.
func decodeValue(col *introspect.Column, raw string) (any, error) {
	v, err := parseScalar(col, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadKey, err)
	}
	return v, nil
}

// ParseScalar parses a filter value into its column's Go type, fail-closed
// (ErrBadFilterValue). Values travel as $N, so even on the text path the value is
// never interpolated (D3).
func ParseScalar(col *introspect.Column, raw string) (any, error) {
	v, err := parseScalar(col, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadFilterValue, err)
	}
	return v, nil
}

// parseScalar is the shared type-aware parser behind key decoding and filter
// value parsing.
func parseScalar(col *introspect.Column, raw string) (any, error) {
	switch col.Category {
	case introspect.CatNumeric:
		// A plain int64 fast-path keeps small integer keys as a typed integer
		// without precision loss. For anything wider or fractional (numeric,
		// money, decimal), do NOT downgrade to float64 -- that silently drops
		// precision so filters miss rows. Instead validate the literal and bind
		// the ORIGINAL STRING; pgx binds a Go string to a numeric column and PG
		// casts text->numeric with full precision (mirroring the write path).
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n, nil
		}
		if !isNumericLiteral(raw) {
			return nil, fmt.Errorf("%w: %q is not numeric", errParse, raw)
		}
		return raw, nil
	case introspect.CatBool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not boolean", errParse, raw)
		}
		return b, nil
	case introspect.CatUUID:
		if !isUUID(raw) {
			return nil, fmt.Errorf("%w: %q is not a uuid", errParse, raw)
		}
		return raw, nil
	case introspect.CatTimestamp:
		if t, err := parseTimestamp(raw); err == nil {
			return t, nil
		}
		return nil, fmt.Errorf("%w: %q is not a timestamp", errParse, raw)
	case introspect.CatEnum:
		if !col.ValidEnumLabel(raw) {
			return nil, fmt.Errorf("%w: %q is not a valid %s label", errParse, raw, col.DataType)
		}
		return raw, nil
	case introspect.CatText:
		// Accept any string; the value still travels as $N so it is never
		// interpolated. Reject NUL bytes, which PostgreSQL cannot store.
		if hasNUL(raw) {
			return nil, fmt.Errorf("%w: text contains NUL", errParse)
		}
		return raw, nil
	default:
		// CatJSON and anything else are not scalar-comparable in pgdesk.
		return nil, fmt.Errorf("%w: %s is not a supported scalar type", errParse, col.DataType)
	}
}

// isNumericLiteral reports whether s is a well-formed decimal numeric literal:
// an optional sign, digits with at most one decimal point, and an optional
// exponent. It is deliberately stricter than strconv.ParseFloat (no hex floats,
// no underscores, no Inf/NaN) so a malformed value fails closed with a clear
// error here rather than reaching SQL, while a valid one is bound verbatim to
// Postgres for full numeric precision.
func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	mantissaDigits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		mantissaDigits++
		i++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			mantissaDigits++
			i++
		}
	}
	if mantissaDigits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		expDigits := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			expDigits++
			i++
		}
		if expDigits == 0 {
			return false
		}
	}
	return i == len(s)
}

func hasNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// isUUID validates the canonical 8-4-4-4-12 hex form without allocating.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// parseTimestamp accepts a small set of unambiguous layouts.
func parseTimestamp(raw string) (time.Time, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, ErrBadKey
}
