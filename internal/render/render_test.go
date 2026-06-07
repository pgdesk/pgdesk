package render

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSafeJSONEscapesScriptBreakout(t *testing.T) {
	// A value that tries to close the script tag and inject markup.
	payload := map[string]string{"x": "</script><img src=x onerror=alert(1)>"}
	out := string(SafeJSON(payload))
	if strings.Contains(out, "</script>") {
		t.Fatalf("SafeJSON leaked a literal </script>: %s", out)
	}
	if strings.Contains(out, "<img") {
		t.Fatalf("SafeJSON leaked a literal <img: %s", out)
	}
	if !strings.Contains(out, `\u003c`) {
		t.Fatalf("SafeJSON did not escape '<': %s", out)
	}
}

func TestSafeJSONEscapesLineSeparators(t *testing.T) {
	out := string(SafeJSON("a b c"))
	if strings.ContainsRune(out, ' ') || strings.ContainsRune(out, ' ') {
		t.Fatalf("SafeJSON left raw line/paragraph separators: %q", out)
	}
	if !strings.Contains(out, `\u2028`) || !strings.Contains(out, `\u2029`) {
		t.Fatalf("SafeJSON did not escape separators: %q", out)
	}
}

func TestFormatValueNil(t *testing.T) {
	if FormatValue(nil) != "" {
		t.Fatal("nil should format as empty string")
	}
}

// TestRenderEscapesDynamicContent proves html/template auto-escaping is intact
// for DB/request-derived values (F1): an XSS payload placed in a cell renders as
// text, not markup.
func TestRenderEscapesDynamicContent(t *testing.T) {
	fsys := fstest.MapFS{
		"page.html": &fstest.MapFile{Data: []byte(
			`{{ define "page" }}<div>{{ .Cell }}</div><input value="{{ .Attr }}"><a href="{{ .Href }}">x</a>{{ csrfField }}{{ end }}`)},
	}
	r, err := New(fsys, StaticFuncs())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	data := struct {
		Cell, Attr, Href string
	}{
		Cell: `<script>alert('xss')</script>`,
		Attr: `" onmouseover="alert(1)`,
		Href: `javascript:alert(1)`,
	}
	err = r.RenderPage(rec, 200, "page", RequestFuncs{CSRFToken: "tok.tok"}, data)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Fatalf("unescaped <script> in output: %s", body)
	}
	if strings.Contains(body, `onmouseover="alert`) {
		t.Fatalf("attribute breakout in output: %s", body)
	}
	if strings.Contains(body, "javascript:alert") {
		t.Fatalf("dangerous href scheme survived: %s", body)
	}
	if !strings.Contains(body, `name="_pgdesk_csrf"`) {
		t.Fatalf("csrfField not rendered: %s", body)
	}
	if !strings.Contains(body, `value="tok.tok"`) {
		t.Fatalf("csrf token not embedded: %s", body)
	}
}

func TestRenderPageBuffersOnError(t *testing.T) {
	fsys := fstest.MapFS{
		// References a nonexistent nested template → execution error.
		"bad.html": &fstest.MapFile{Data: []byte(`{{ define "bad" }}{{ template "missing" . }}{{ end }}`)},
	}
	r, err := New(fsys, StaticFuncs())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := r.RenderPage(rec, 200, "bad", RequestFuncs{}, nil); err == nil {
		t.Fatal("expected render error")
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte{}) {
		t.Fatalf("failed render must not write a body, got: %q", rec.Body.String())
	}
}
