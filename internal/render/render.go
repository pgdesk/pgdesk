// Package render owns pgdesk's HTML templating (F1, F5).
//
//   - Every template is parsed once at construction; a parse error fails
//     construction, never a per-request lazy parse (F5).
//   - Rendering executes into a buffer first, so a mid-render error yields a
//     clean generic 500 with no partial or leaked output (F5).
//   - All dynamic values are escaped by html/template's contextual auto-escaping;
//     the template.HTML/JS/URL/CSS bypass types are never applied to
//     DB- or request-derived values (F1).
//   - The per-request CSRF token and CSP nonce are injected as template funcs by
//     cloning the base template per request, so JS never needs to read the cookie.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

// Renderer holds the compiled template set. It is immutable after New and safe
// for concurrent use (per-request state is applied to a clone).
type Renderer struct {
	base *template.Template
}

// New parses every *.html template under fsys once. staticFuncs are functions
// available to all templates at parse time (formatting helpers, plus stubs for
// the per-request csrfField/nonce funcs that RenderPage overrides).
//
// overrideFS is optional (an absent or nil entry is a no-op) and, when given,
// is parsed ON TOP of fsys: any *.html file in overrideFS whose name matches a
// built-in template (e.g. "list.html") replaces it, because html/template's
// Parse/ParseFS re-associates a template of the same name rather than merging
// it; templates absent from overrideFS keep their embedded default. A
// malformed override template fails New with an error, never a per-request
// panic (F5). It is variadic solely so existing two-argument call sites need
// not pass an explicit nil; passing more than one overrideFS is not supported
// and only the first is used.
func New(fsys fs.FS, staticFuncs template.FuncMap, overrideFS ...fs.FS) (*Renderer, error) {
	funcs := template.FuncMap{
		// Request-scoped stubs; real implementations are injected per request in
		// RenderPage via Clone. Present at parse time so templates reference them.
		"csrfField": func() template.HTML { return "" },
		"nonce":     func() string { return "" },
	}
	for k, v := range staticFuncs {
		funcs[k] = v
	}
	t := template.New("pgdesk").Funcs(funcs)
	t, err := t.ParseFS(fsys, "*.html")
	if err != nil {
		return nil, fmt.Errorf("pgdesk/render: parsing templates: %w", err)
	}
	if len(overrideFS) > 0 && overrideFS[0] != nil {
		t, err = t.ParseFS(overrideFS[0], "*.html")
		if err != nil {
			return nil, fmt.Errorf("pgdesk/render: parsing override templates: %w", err)
		}
	}
	return &Renderer{base: t}, nil
}

// RequestFuncs carries the per-request values that must be bound into a small
// number of template functions (never into DB data paths).
type RequestFuncs struct {
	// CSRFToken is the signed token to embed via {{ csrfField }} (D5).
	CSRFToken string
	// Nonce is the per-request CSP nonce for {{ nonce }} in script/style (F2).
	Nonce string
}

// RenderPage renders a named template with request-scoped csrfField/nonce funcs,
// buffering output so a render failure never produces partial/garbled HTML. On
// success it writes status and the buffered body. On failure it returns the
// error WITHOUT writing anything, so the caller can emit a clean generic 500.
func (r *Renderer) RenderPage(w http.ResponseWriter, status int, name string, rf RequestFuncs, data any) error {
	clone, err := r.base.Clone()
	if err != nil {
		return fmt.Errorf("pgdesk/render: cloning template: %w", err)
	}
	token := rf.CSRFToken
	nonce := rf.Nonce
	clone.Funcs(template.FuncMap{
		"csrfField": func() template.HTML { return csrfField(token) },
		"nonce":     func() string { return nonce },
	})

	var buf bytes.Buffer
	if err := clone.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("pgdesk/render: executing %q: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
	return nil
}

// csrfField builds the hidden CSRF input. The markup is static, developer
// authored; only the token value is interpolated and it is HTML-escaped as
// defense-in-depth even though the token charset (base64url + '.') is safe. This
// is the ONLY sanctioned use of template.HTML on a dynamic value in pgdesk, and
// the dynamic part is escaped before wrapping (F1).
func csrfField(token string) template.HTML {
	return template.HTML(`<input type="hidden" name="` +
		template.HTMLEscapeString(csrfFieldName) + `" value="` +
		template.HTMLEscapeString(token) + `">`)
}

// csrfFieldName mirrors internal/csrf.FormField. It is duplicated here to avoid
// an import cycle risk and is covered by a compile-time assertion in the caller.
const csrfFieldName = "_pgdesk_csrf"
