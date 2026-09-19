package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

type Renderer struct {
	base *template.Template
}

func New(fsys fs.FS, staticFuncs template.FuncMap, overrideFS ...fs.FS) (*Renderer, error) {
	funcs := template.FuncMap{

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

type RequestFuncs struct {
	CSRFToken string

	Nonce string
}

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

func csrfField(token string) template.HTML {
	return template.HTML(`<input type="hidden" name="` +
		template.HTMLEscapeString(csrfFieldName) + `" value="` +
		template.HTMLEscapeString(token) + `">`)
}

const csrfFieldName = "_pgdesk_csrf"
