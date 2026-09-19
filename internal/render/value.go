package render

import (
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

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

		return formatUUID(x)
	case map[string]any, []any:

		return formatJSON(x)
	case fmt.Stringer:
		return x.String()
	case driver.Valuer:
		return formatValuer(x)
	default:
		return fmt.Sprint(x)
	}
}

func formatBytes(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return `\x` + hex.EncodeToString(b)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

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

func formatJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func formatValuer(v driver.Valuer) string {
	dv, err := v.Value()
	if err != nil {
		return fmt.Sprint(v)
	}

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
