//go:build integration

package pgdesk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pgdesk/pgdesk"
)

func TestIntegrationResourceUseMiddlewareRuns(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS beh_a; DROP TABLE IF EXISTS beh_b;
		CREATE TABLE beh_a(id bigint PRIMARY KEY, name text);
		CREATE TABLE beh_b(id bigint PRIMARY KEY, name text);
		INSERT INTO beh_a VALUES (1,'a'); INSERT INTO beh_b VALUES (1,'b');`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	var aHits, bHits int32
	counting := func(counter *int32) pgdesk.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(counter, 1)
				next.ServeHTTP(w, r)
			})
		}
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("beh_a", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "name")
			r.Use(counting(&aHits))
		}),
		pgdesk.WithResource("beh_b", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "name")
			r.Use(counting(&bHits))
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if rec := do(admin, httptest.NewRequest("GET", "/admin/beh_a", nil)); rec.Code != 200 {
		t.Fatalf("beh_a list = %d", rec.Code)
	}
	if got := atomic.LoadInt32(&aHits); got != 1 {
		t.Errorf("beh_a middleware ran %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&bHits); got != 0 {
		t.Errorf("beh_b middleware ran %d times on a beh_a request, want 0 (isolation)", got)
	}
}

func TestIntegrationCSVExportNeutralizesFormula(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS beh_inj;
		CREATE TABLE beh_inj(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, note text NOT NULL);
		INSERT INTO beh_inj(note) VALUES ('=cmd|''/c calc''!A1'), ('+SUM(A1)'), ('safe');`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("beh_inj", func(r *pgdesk.Resource) { r.ListDisplay("id", "note") }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := do(admin, httptest.NewRequest("GET", "/admin/beh_inj/export.csv", nil))
	if rec.Code != 200 {
		t.Fatalf("export = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "'=cmd") || !strings.Contains(body, "'+SUM(A1)") {
		t.Errorf("formula cells not neutralized:\n%s", body)
	}
	if strings.Contains(body, ",=cmd") || strings.Contains(body, "\n=cmd") {
		t.Errorf("a raw formula cell reached the CSV:\n%s", body)
	}
}

func TestIntegrationWidgetAndLabelOverride(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS beh_form;
		CREATE TABLE beh_form(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, bio text);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("beh_form", func(r *pgdesk.Resource) {
			r.Widget("bio", pgdesk.WidgetTextarea)
			r.FieldLabel("bio", "Biography")
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := do(admin, httptest.NewRequest("GET", "/admin/beh_form/new", nil))
	if rec.Code != 200 {
		t.Fatalf("create form = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<textarea") {
		t.Errorf("Widget(WidgetTextarea) did not render a textarea:\n%s", body)
	}
	if !strings.Contains(body, "Biography") {
		t.Errorf("FieldLabel override not applied:\n%s", body)
	}
}

func TestIntegrationFlashCookieSecure(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS beh_flash;
		CREATE TABLE beh_flash(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("beh_flash", func(r *pgdesk.Resource) { r.ListDisplay("id", "name") }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	token, cookie, _ := capEditFormNew(t, admin, "beh_flash")
	form := url.Values{"name": {"x"}}
	form.Set("_pgdesk_csrf", token)
	req := httptest.NewRequest("POST", "/admin/beh_flash/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: cookie})
	rec := do(admin, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	var flashCookie string
	for _, sc := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(sc, "pgdesk_flash=") {
			flashCookie = sc
		}
	}
	if flashCookie == "" {
		t.Fatalf("no flash cookie set on mutation redirect; headers: %v", rec.Header().Values("Set-Cookie"))
	}
	if !strings.Contains(flashCookie, "Secure") {
		t.Errorf("flash cookie missing Secure attribute: %q", flashCookie)
	}
}

func capEditFormNew(t *testing.T, admin *pgdesk.Admin, resource string) (string, string, string) {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", "/admin/"+resource+"/new", nil))
	if rec.Code != 200 {
		t.Fatalf("%s create form status = %d\n%s", resource, rec.Code, rec.Body.String())
	}
	fm := reCapCSRF.FindStringSubmatch(rec.Body.String())
	cm := reCapCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	if fm == nil || cm == nil {
		t.Fatalf("missing token/cookie in %s create form:\n%s", resource, rec.Body.String())
	}
	return fm[1], cm[1], ""
}
