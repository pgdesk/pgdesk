package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// StaticFuncs returns the template functions available at parse time to every
// template. They are pure and request-independent. None of them applies a
// bypass type to unsanitized dynamic data (F1).
func StaticFuncs() template.FuncMap {
	return template.FuncMap{
		"formatValue": FormatValue,
		"cellValue":   CellValue,
		"safeJSON":    SafeJSON,
		"dict":        dict,
		"hasPrefix":   strings.HasPrefix,
	}
}

// CellValue renders a scanned value for display, upgrading booleans to a status
// pill (a common enterprise-admin affordance). It is F1-safe: the boolean path
// emits only fixed, developer-authored markup, and every other value is
// HTML-escaped before being returned as template.HTML — no dynamic/DB content is
// ever emitted unescaped.
func CellValue(v any) template.HTML {
	if b, ok := v.(bool); ok {
		if b {
			return `<span class="pg-badge pg-badge-ok">Yes</span>`
		}
		return `<span class="pg-badge pg-badge-off">No</span>`
	}
	return template.HTML(template.HTMLEscapeString(FormatValue(v)))
}

// FormatValue renders a scanned DB value as a plain display string. The template
// escapes the result, so this returns text and never markup (F1). SQL NULL
// (nil) renders as an empty string.
func FormatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// scriptSafeReplacer rewrites the bytes that could terminate or reinterpret a
// <script> context into their JavaScript unicode escapes. The replacement values
// are the literal 6-character sequences < etc., which JSON.parse restores to
// the original characters while the raw HTML parser cannot mistake them for tag
// boundaries. This is the allowlist sanitizer F1 requires before template.JS.
var scriptSafeReplacer = strings.NewReplacer(
	"<", "\\u003c",
	">", "\\u003e",
	"&", "\\u0026",
	" ", "\\u2028", // line separator: breaks JS string literals otherwise
	" ", "\\u2029", // paragraph separator: same
)

// SafeJSON marshals v and escapes the characters that could break out of a
// <script> context. The result is intended to be embedded inside a nonce'd
// <script> block (e.g. a JSON-editor widget's initial value) — preventing the
// classic </script> breakout admin XSS (F1).
func SafeJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(scriptSafeReplacer.Replace(string(b)))
}

// dict builds a map from alternating key/value pairs, for passing multiple
// values into a sub-template: {{ template "x" (dict "A" .A "B" .B) }}.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("pgdesk/render: dict requires an even number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("pgdesk/render: dict keys must be strings")
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}
