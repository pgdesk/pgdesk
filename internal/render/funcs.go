package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
)

func StaticFuncs() template.FuncMap {
	return template.FuncMap{
		"formatValue": FormatValue,
		"cellValue":   CellValue,
		"safeJSON":    SafeJSON,
		"dict":        dict,
		"hasPrefix":   strings.HasPrefix,
	}
}

func CellValue(v any) template.HTML {
	if b, ok := v.(bool); ok {
		if b {
			return `<span class="pg-badge pg-badge-ok">Yes</span>`
		}
		return `<span class="pg-badge pg-badge-off">No</span>`
	}
	return template.HTML(template.HTMLEscapeString(FormatValue(v)))
}

var scriptSafeReplacer = strings.NewReplacer(
	"<", "\\u003c",
	">", "\\u003e",
	"&", "\\u0026",
	"\u2028", "\\u2028",
	"\u2029", "\\u2029",
)

func SafeJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(scriptSafeReplacer.Replace(string(b)))
}

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
