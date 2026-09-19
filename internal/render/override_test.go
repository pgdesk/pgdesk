package render

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewOverridesNamedTemplate(t *testing.T) {
	base := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}BUILTIN{{ end }}`)},
	}
	override := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}OVERRIDDEN{{ end }}`)},
	}

	r, err := New(base, StaticFuncs(), override)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := r.RenderPage(rec, 200, "greeting", RequestFuncs{}, nil); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	body := rec.Body.String()
	if body != "OVERRIDDEN" {
		t.Fatalf("expected override to win, got %q", body)
	}
	if strings.Contains(body, "BUILTIN") {
		t.Fatalf("built-in template leaked through override: %q", body)
	}
}

func TestNewNoOverrideKeepsBuiltin(t *testing.T) {
	base := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}BUILTIN{{ end }}`)},
	}

	r, err := New(base, StaticFuncs())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := r.RenderPage(rec, 200, "greeting", RequestFuncs{}, nil); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if got := rec.Body.String(); got != "BUILTIN" {
		t.Fatalf("expected built-in default, got %q", got)
	}
}

func TestNewPartialOverrideFallsBackForUnmatched(t *testing.T) {
	base := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}BUILTIN-GREETING{{ end }}`)},
		"farewell.html": &fstest.MapFile{Data: []byte(
			`{{ define "farewell" }}BUILTIN-FAREWELL{{ end }}`)},
	}
	override := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}OVERRIDDEN-GREETING{{ end }}`)},
	}

	r, err := New(base, StaticFuncs(), override)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := r.RenderPage(rec, 200, "greeting", RequestFuncs{}, nil); err != nil {
		t.Fatalf("RenderPage greeting: %v", err)
	}
	if got := rec.Body.String(); got != "OVERRIDDEN-GREETING" {
		t.Fatalf("greeting: expected override, got %q", got)
	}

	rec = httptest.NewRecorder()
	if err := r.RenderPage(rec, 200, "farewell", RequestFuncs{}, nil); err != nil {
		t.Fatalf("RenderPage farewell: %v", err)
	}
	if got := rec.Body.String(); got != "BUILTIN-FAREWELL" {
		t.Fatalf("farewell: expected built-in fallback, got %q", got)
	}
}

func TestNewMalformedOverrideFailsConstruction(t *testing.T) {
	base := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}BUILTIN{{ end }}`)},
	}
	malformed := fstest.MapFS{

		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}{{ .Unclosed`)},
	}

	_, err := New(base, StaticFuncs(), malformed)
	if err == nil {
		t.Fatal("expected New to fail on malformed override template")
	}
	if !strings.Contains(err.Error(), "override") {
		t.Fatalf("expected error to mention override parsing, got: %v", err)
	}
}
