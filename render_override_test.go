package pgdesk

import (
	"io/fs"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/pgdesk/pgdesk/internal/render"
)

func TestWithTemplateFSOverridesBuiltin(t *testing.T) {
	cfg := defaultConfig()
	override := fstest.MapFS{

		"error.html": &fstest.MapFile{Data: []byte(
			`{{- define "error" -}}CUSTOM-BRANDED-ERROR: {{ .Status }}{{- end -}}`)},
	}
	WithTemplateFS(override)(cfg)

	if cfg.templateFS == nil {
		t.Fatal("WithTemplateFS did not set cfg.templateFS")
	}

	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.New(sub, render.StaticFuncs(), cfg.templateFS)
	if err != nil {
		t.Fatalf("render.New with override: %v", err)
	}

	rec := httptest.NewRecorder()
	data := struct{ Status int }{Status: 404}
	if err := r.RenderPage(rec, 404, "error", render.RequestFuncs{}, data); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if got := rec.Body.String(); got != "CUSTOM-BRANDED-ERROR: 404" {
		t.Fatalf("expected overridden error template, got %q", got)
	}
}

func TestWithoutTemplateFSKeepsBuiltinBehavior(t *testing.T) {
	cfg := defaultConfig()
	if cfg.templateFS != nil {
		t.Fatal("default config should not have a templateFS set")
	}

	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.New(sub, render.StaticFuncs(), cfg.templateFS)
	if err != nil {
		t.Fatalf("render.New with nil override: %v", err)
	}

	rec := httptest.NewRecorder()
	data := struct {
		Base                           baseView
		Status                         int
		StatusText, Message, RequestID string
	}{Base: baseView{Title: "T", SiteTitle: "Site", BasePath: "/admin"}, Status: 404, StatusText: "Not Found", Message: "nope"}
	if err := r.RenderPage(rec, 404, "error", render.RequestFuncs{}, data); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if got := rec.Body.String(); got == "" {
		t.Fatal("expected non-empty built-in error page output")
	}
}
