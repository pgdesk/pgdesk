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

var (
	ErrBadKey = errors.New("pgdesk/query: key does not decode to the resource key type")

	ErrKeyArity = errors.New("pgdesk/query: key arity mismatch")

	ErrNoKey = errors.New("pgdesk/query: resource has no key")
)

const compositePrefix = "~"

var keyEnc = base64.RawURLEncoding

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

var ErrBadFilterValue = errors.New("pgdesk/query: filter value does not match column type")

var errParse = errors.New("value does not match column type")

func decodeValue(col *introspect.Column, raw string) (any, error) {
	v, err := parseScalar(col, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadKey, err)
	}
	return v, nil
}

func ParseScalar(col *introspect.Column, raw string) (any, error) {
	v, err := parseScalar(col, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadFilterValue, err)
	}
	return v, nil
}

func parseScalar(col *introspect.Column, raw string) (any, error) {
	switch col.Category {
	case introspect.CatNumeric:

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

		if hasNUL(raw) {
			return nil, fmt.Errorf("%w: text contains NUL", errParse)
		}
		return raw, nil
	default:

		return nil, fmt.Errorf("%w: %s is not a supported scalar type", errParse, col.DataType)
	}
}

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

func parseTimestamp(raw string) (time.Time, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, ErrBadKey
}
