//go:build integration

// Regression tests for the schema-capability fixes: identity-always PK editing
// (issue #1), per-operation view updatability (issue #7), and the xmin-on-views
// version fallback (issue #5). Each spins up its own small schema so it is
// independent of the shared it_* fixtures.
package pgdesk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

func dsnOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PGDESK_TEST_DSN")
	if dsn == "" {
		t.Skip("PGDESK_TEST_DSN not set; skipping integration tests")
	}
	return dsn
}

func capPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := dsnOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

var (
	reCapVersion = regexp.MustCompile(`name="_version" value="([^"]*)"`)
	reCapCSRF    = regexp.MustCompile(`name="_pgdesk_csrf" value="([^"]+)"`)
	reCapCookie  = regexp.MustCompile(`__Host-pgdesk_csrf=([^;]+)`)
)

// TestIntegrationIdentityAlwaysEditableByDefault is the regression for issue #1:
// a GENERATED ALWAYS AS IDENTITY primary key must be editable WITHOUT the host
// having to mark it Readonly. Before the fix the identity column landed in the
// UPDATE SET list and every edit failed with SQLSTATE 428C9 (HTTP 422).
func TestIntegrationIdentityAlwaysEditableByDefault(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS cap_ident;
		CREATE TABLE cap_ident(
			id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			email  text    NOT NULL UNIQUE,
			active boolean NOT NULL DEFAULT true);
		INSERT INTO cap_ident(email) VALUES ('a@example.com');`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		// Deliberately NO Readonly("id") -- the whole point of the fix.
		pgdesk.WithResource("cap_ident", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "email", "active")
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	token, cookie, version := capEditForm(t, admin, "cap_ident", "1")
	form := url.Values{"email": {"a.updated@example.com"}}
	form.Set("_pgdesk_csrf", token)
	form.Set("_version", version)
	req := httptest.NewRequest("POST", "/admin/cap_ident/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: cookie})
	rec := do(admin, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("identity-PK edit status = %d, want 303 (was 422 before the fix)\n%s", rec.Code, rec.Body.String())
	}
	var email string
	if err := pool.QueryRow(ctx, "SELECT email FROM cap_ident WHERE id=1").Scan(&email); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if email != "a.updated@example.com" {
		t.Fatalf("edit did not persist: email=%q", email)
	}
}

// TestIntegrationUpdatableViewWithKey is the regression for issues #5 and #7: a
// keyed, auto-updatable view must support detail + edit without a 500. Before the
// fix the detail/edit SELECT emitted xmin::text, which does not exist on a view.
func TestIntegrationUpdatableViewWithKey(t *testing.T) {
	pool := capPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP VIEW IF EXISTS cap_uview;
		DROP TABLE IF EXISTS cap_base;
		CREATE TABLE cap_base(
			id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			label text NOT NULL);
		INSERT INTO cap_base(label) VALUES ('one');
		CREATE VIEW cap_uview AS SELECT id, label FROM cap_base;`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("cap_uview", func(r *pgdesk.Resource) {
			r.Key("id") // declare a key on the view to unlock detail/edit (D6)
			r.ListDisplay("id", "label")
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Detail must not 500 (previously: xmin does not exist on a view).
	if rec := do(admin, httptest.NewRequest("GET", "/admin/cap_uview/1", nil)); rec.Code != 200 {
		t.Fatalf("view detail status = %d, want 200\n%s", rec.Code, rec.Body.String())
	}

	token, cookie, version := capEditForm(t, admin, "cap_uview", "1")
	form := url.Values{"label": {"two"}}
	form.Set("_pgdesk_csrf", token)
	form.Set("_version", version)
	req := httptest.NewRequest("POST", "/admin/cap_uview/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: cookie})
	rec := do(admin, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("view edit status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	var label string
	if err := pool.QueryRow(ctx, "SELECT label FROM cap_base WHERE id=1").Scan(&label); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if label != "two" {
		t.Fatalf("view edit did not persist: label=%q", label)
	}
}

// capEditForm GETs an edit form and returns (formToken, cookieToken, version).
func capEditForm(t *testing.T, admin *pgdesk.Admin, resource, id string) (string, string, string) {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", "/admin/"+resource+"/"+id+"/edit", nil))
	if rec.Code != 200 {
		t.Fatalf("%s edit form status = %d\n%s", resource, rec.Code, rec.Body.String())
	}
	fm := reCapCSRF.FindStringSubmatch(rec.Body.String())
	cm := reCapCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	vm := reCapVersion.FindStringSubmatch(rec.Body.String())
	if fm == nil || cm == nil || vm == nil {
		t.Fatalf("missing token/cookie/version in %s edit form:\n%s", resource, rec.Body.String())
	}
	return fm[1], cm[1], vm[1]
}
