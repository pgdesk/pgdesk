package render

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestNewOverridesNamedTemplate proves the escape hatch (Gitea #25): a
// template present in the override FS replaces the built-in template of the
// same name, because a later ParseFS of a same-named template re-associates
// it rather than merging it.
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

// TestNewNoOverrideKeepsBuiltin is the regression case: with no override FS
// supplied (the zero-value, variadic-absent call), the embedded/base template
// renders unchanged. This also proves existing two-argument call sites are
// unaffected by the added override parameter.
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

// TestNewPartialOverrideFallsBackForUnmatched proves that overriding one
// template leaves sibling built-in templates intact -- the override is a
// per-name overlay, not a full replacement of the set.
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

// TestNewMalformedOverrideFailsConstruction proves a malformed override
// template fails New with an error at construction time -- never a
// per-request panic (F5).
func TestNewMalformedOverrideFailsConstruction(t *testing.T) {
	base := fstest.MapFS{
		"greeting.html": &fstest.MapFile{Data: []byte(
			`{{ define "greeting" }}BUILTIN{{ end }}`)},
	}
	malformed := fstest.MapFS{
		// Unclosed action -> parse error.
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
