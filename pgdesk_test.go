package pgdesk

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pgdesk/pgdesk/internal/csrf"
	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/render"
)

// syntheticCatalog builds a small catalog covering the auto-register decision
// cases: a keyed writable table, an excluded table, a keyless table, and a view.
func syntheticCatalog() *introspect.Catalog {
	idCol := &introspect.Column{Name: "id", Position: 1, DataType: "int8", Category: introspect.CatNumeric}
	nameCol := &introspect.Column{Name: "name", Position: 2, DataType: "text", Category: introspect.CatText}

	users := introspect.NewTable("public", "users", false, true, "",
		[]*introspect.Column{idCol, nameCol}, []*introspect.Column{idCol}, nil)
	audit := introspect.NewTable("public", "audit", false, true, "",
		[]*introspect.Column{idCol}, []*introspect.Column{idCol}, nil)
	nopk := introspect.NewTable("public", "junction", false, true, "",
		[]*introspect.Column{nameCol}, nil, nil) // no primary key
	view := introspect.NewTable("public", "active_users", true, false, "",
		[]*introspect.Column{idCol}, []*introspect.Column{idCol}, nil)

	return introspect.NewCatalog([]string{"public"},
		[]*introspect.Table{users, audit, nopk, view})
}

func testAdmin(t *testing.T, cfgFns ...func(*config)) *Admin {
	t.Helper()
	cfg := defaultConfig()
	for _, fn := range cfgFns {
		fn(cfg)
	}
	signer, err := csrf.NewSigner([]byte("unit-test-secret-key-000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.New(sub, render.StaticFuncs())
	if err != nil {
		t.Fatal(err)
	}
	return &Admin{cfg: cfg, signer: signer, renderer: renderer}
}

func TestDBErrorClassification(t *testing.T) {
	a := testAdmin(t)

	// A deadline (also fired by pool saturation) -> 503 with Retry-After (O2).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/users", nil)
	a.dbError(rec, req, "list", context.DeadlineExceeded)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("deadline -> status %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 should set Retry-After")
	}

	// Any other DB error -> generic 500, no internal detail leaked (F5).
	rec = httptest.NewRecorder()
	a.dbError(rec, req, "list", errors.New("relation \"secret_table\" does not exist"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("generic error -> status %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret_table") {
		t.Fatal("500 page leaked internal error detail")
	}
}

func TestBuildStateAutoRegister(t *testing.T) {
	cat := syntheticCatalog()

	a := testAdmin(t, func(c *config) {
		c.autoRegister = &autoRegisterConfig{
			excludeTables: map[string]bool{"audit": true},
			includeViews:  map[string]bool{},
		}
	})
	st, err := a.buildState(cat)
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	if _, ok := st.resource("users"); !ok {
		t.Error("keyed table 'users' should be auto-registered")
	}
	if _, ok := st.resource("audit"); ok {
		t.Error("excluded table 'audit' must not be registered")
	}
	if _, ok := st.resource("junction"); ok {
		t.Error("keyless table 'junction' must be skipped")
	}
	if _, ok := st.resource("active_users"); ok {
		t.Error("view must be skipped unless included")
	}
}

func TestBuildStateAutoRegisterIncludeViews(t *testing.T) {
	a := testAdmin(t, func(c *config) {
		c.autoRegister = &autoRegisterConfig{
			excludeTables: map[string]bool{},
			includeViews:  map[string]bool{"active_users": true},
		}
	})
	st, err := a.buildState(syntheticCatalog())
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	if _, ok := st.resource("active_users"); !ok {
		t.Error("view named in IncludeViews should be registered")
	}
}

func TestBuildStateExplicitWinsOverAuto(t *testing.T) {
	a := testAdmin(t, func(c *config) {
		c.autoRegister = &autoRegisterConfig{excludeTables: map[string]bool{}, includeViews: map[string]bool{}}
	})
	a.cfg.resources = []resourceReg{{name: "users", fn: func(r *Resource) { r.LabelPlural = "People" }}}
	st, err := a.buildState(syntheticCatalog())
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	res, ok := st.resource("users")
	if !ok || res.LabelPlural != "People" {
		t.Fatalf("explicit config should win over auto-register: %+v", res)
	}
}

func TestBuildStateUnknownColumnReturnsError(t *testing.T) {
	// A resource referencing a column that doesn't exist is a configuration error
	// returned by New (via buildState) -- not a panic (the WithResource contract).
	a := testAdmin(t)
	a.cfg.resources = []resourceReg{{name: "users", fn: func(r *Resource) {
		r.ListDisplay("no_such_column")
	}}}
	if _, err := a.buildState(syntheticCatalog()); err == nil {
		t.Fatal("expected an error for an unknown column, got nil")
	}
}

func TestBuildStateWritableWithoutSecretFails(t *testing.T) {
	// No signer + a writable auto-registered table -> fail closed (D5).
	a := &Admin{cfg: defaultConfig()}
	a.cfg.autoRegister = &autoRegisterConfig{excludeTables: map[string]bool{}, includeViews: map[string]bool{}}
	if _, err := a.buildState(syntheticCatalog()); !errors.Is(err, ErrSecretRequired) {
		t.Fatalf("want ErrSecretRequired, got %v", err)
	}
}

func TestSafeRedirect(t *testing.T) {
	a := &Admin{cfg: defaultConfig()} // basePath "/admin"
	cases := map[string]string{
		"/admin/users":        "/admin/users",
		"/admin":              "/admin",
		"/admin/users/42":     "/admin/users/42",
		"":                    "/admin/",
		"//evil.com":          "/admin/",
		"https://evil.com":    "/admin/",
		"/etc/passwd":         "/admin/", // outside base path
		"/adminX/y":           "/admin/", // prefix trick, not under /admin/
		"javascript:alert(1)": "/admin/", // no leading slash
		"/admin/a\\b":         "/admin/", // backslash
		"http://x/admin/y":    "/admin/", // absolute URL
	}
	for in, want := range cases {
		if got := a.safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapPgError(t *testing.T) {
	msgs := map[string]string{"chk_total_positive": "Total must be positive."}

	// Unique lookup that maps a constraint name to its column (as the catalog would).
	uniq := func(name string) ([]string, bool) {
		if name == "users_email_key" {
			return []string{"email"}, true
		}
		return nil, false
	}

	// 23505 with a resolvable constraint -> per-field "must be unique" (D7).
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}
	if me := mapPgError(unique, nil, uniq); me.fieldErrors["email"] != "must be unique" {
		t.Errorf("23505 per-field mapping wrong: %+v", me)
	}
	// 23505 with an unknown constraint -> form-level fallback.
	if me := mapPgError(&pgconn.PgError{Code: "23505", ConstraintName: "mystery"}, nil, uniq); me.formError == "" {
		t.Errorf("23505 fallback should be a form error: %+v", me)
	}

	notNull := &pgconn.PgError{Code: "23502", ColumnName: "name"}
	if me := mapPgError(notNull, nil, nil); me.fieldErrors["name"] != "is required" {
		t.Errorf("23502 mapping wrong: %+v", me)
	}

	check := &pgconn.PgError{Code: "23514", ConstraintName: "chk_total_positive"}
	if me := mapPgError(check, msgs, nil); me.formError != "Total must be positive." {
		t.Errorf("constraint message override not applied: %+v", me)
	}

	// Non-Postgres error -> generic form error, no internal detail leaked.
	if me := mapPgError(errors.New("boom: schema secret"), nil, nil); me.formError == "" || strings.Contains(me.formError, "secret") {
		t.Errorf("non-pg error should be generic: %+v", me)
	}
}

func TestSanitizeRequestID(t *testing.T) {
	if got := sanitizeRequestID("abc-123_XYZ.9"); got != "abc-123_XYZ.9" {
		t.Errorf("clean id rejected: %q", got)
	}
	// Injection-shaped inbound IDs are replaced with a generated one.
	for _, bad := range []string{"", "a b", "a\nb", "a;b", strings.Repeat("x", 200)} {
		got := sanitizeRequestID(bad)
		if got == bad {
			t.Errorf("bad id %q was not replaced", bad)
		}
		if got == "" {
			t.Errorf("replacement id is empty for %q", bad)
		}
	}
}

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"users":      "Users",
		"full_name":  "Full Name",
		"created_at": "Created At",
		"":           "",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClampPageSize(t *testing.T) {
	a := &Admin{cfg: defaultConfig()} // default 50, max 200
	if got := a.clampPageSize(0); got != 50 {
		t.Errorf("zero -> default: got %d", got)
	}
	if got := a.clampPageSize(10_000); got != 200 {
		t.Errorf("huge -> max: got %d", got)
	}
	if got := a.clampPageSize(25); got != 25 {
		t.Errorf("in-range unchanged: got %d", got)
	}
}

// TestEmbeddedTemplatesParseAndExecute guards the shipped templates: they must
// parse at construction (F5) and execute for each page shape with escaping
// intact (F1). It needs no database.
func TestEmbeddedTemplatesParseAndExecute(t *testing.T) {
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.New(sub, render.StaticFuncs())
	if err != nil {
		t.Fatalf("templates failed to parse: %v", err)
	}

	base := baseView{Title: "T", SiteTitle: "Site", BasePath: "/admin"}
	xss := `<script>alert(1)</script>`

	pages := []struct {
		name string
		data any
	}{
		{"index", indexView{Base: base, Resources: []resourceNav{{Name: "users", LabelPlural: "Users"}}}},
		{"list", listView{
			Base: base, Resource: resourceMeta{Name: "users", LabelPlural: "Users"},
			Headers:       []sortHeader{{Label: "Email", URL: "/admin/users?sort=email"}},
			Rows:          []rowView{{Cells: []cellView{{Value: xss}}, Key: "1"}},
			InlineFilters: []filterField{{Label: "Status", Kind: "select", ParamKey: "f_status", Options: []string{"", "active"}}},
			HasFilters:    true, HasDetail: true, Page: 1,
		}},
		{"detail", detailView{
			Base: base, Resource: resourceMeta{Name: "users", Label: "User"},
			Key: "1", CanEdit: true,
			Fields: []detailField{{Label: "Email", Value: xss}},
		}},
		{"form", formView{
			Base: base, Resource: resourceMeta{Name: "users", Label: "User"},
			Key: "1", Version: "42",
			Fields: []formField{{Name: "email", Label: "Email", Value: xss, ValueString: xss, Widget: "text"}},
		}},
		{"error", errorView{Base: base, Status: 500, StatusText: "Internal Server Error", Message: "oops", RequestID: "req1"}},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rf := render.RequestFuncs{CSRFToken: "tok.tok", Nonce: "n0nce"}
			if err := r.RenderPage(rec, 200, p.name, rf, p.data); err != nil {
				t.Fatalf("render %q: %v", p.name, err)
			}
			body := rec.Body.String()
			if strings.Contains(body, "<script>alert(1)") {
				t.Fatalf("page %q leaked unescaped XSS payload", p.name)
			}
			// Every page carries the nonce'd theme-init script and toggle (F2).
			if !strings.Contains(body, `nonce="n0nce"`) {
				t.Errorf("page %q missing per-request nonce on script", p.name)
			}
			if !strings.Contains(body, "data-theme-toggle") {
				t.Errorf("page %q missing theme toggle", p.name)
			}
		})
	}
}
