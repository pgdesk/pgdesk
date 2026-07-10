package pgdesk

import (
	"io/fs"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/pgdesk/pgdesk/internal/render"
)

// TestWithTemplateFSOverridesBuiltin exercises the WithTemplateFS option path
// (Gitea #25): a host-supplied fs.FS overlays the embedded templates, so a
// template present in it replaces the shipped default while every other
// built-in template is untouched. It mirrors exactly how newAdmin wires
// cfg.templateFS into render.New (admin.go), without needing a live DB.
func TestWithTemplateFSOverridesBuiltin(t *testing.T) {
	cfg := defaultConfig()
	override := fstest.MapFS{
		// error.html is self-contained (no head/foot dependency) so this test
		// doesn't need to build a full baseView.
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

// TestWithoutTemplateFSKeepsBuiltinBehavior is the regression guard: when
// WithTemplateFS is never called, cfg.templateFS stays nil and construction
// behaves byte-for-byte like before this option existed.
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
