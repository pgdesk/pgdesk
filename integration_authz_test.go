//go:build integration

// Authorization regressions for two defects that share one shape: a rule the UI
// honours but a second path does not.
//
//   - CapAccessAdmin was checked only on the index page, so denying it hid the nav
//     and left every resource route serving data.
//   - The foreign-key picker offered only in-scope rows, but the write path
//     accepted any key the operator typed.
package pgdesk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgdesk/pgdesk"
)

// denyCap builds an authorizer that denies one capability for one resource and
// allows everything else -- the shape a host writes when it means "this operator
// is not an admin" or "this operator cannot list users".
func denyCap(cap pgdesk.Capability, resource string) pgdesk.Authorizer {
	return pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		if attrs.Capability == cap && attrs.Resource == resource {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
}

// CapAccessAdmin is the front door. Denying it must lock the whole admin, not
// just hide the index -- otherwise a host that writes the most natural possible
// "not an admin" rule still serves every row to that operator.
func TestIntegrationAccessAdminGatesEveryRoute(t *testing.T) {
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(denyCap(pgdesk.CapAccessAdmin, "")))

	paths := []string{
		"/admin/",
		"/admin/fk_users",
		"/admin/fk_users/1",
		"/admin/fk_users/1/edit",
		"/admin/fk_users/new",
		"/admin/fk_users/export.csv",
		"/admin/fk_users/options.json",
	}
	for _, path := range paths {
		rec := do(admin, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "ada@example.com") {
			t.Errorf("%s served row data to an operator denied admin access", path)
		}
	}
}

// Static assets stay reachable: the 403 page itself loads the stylesheet, and an
// asset carries no row data.
func TestIntegrationAccessAdminStillServesAssets(t *testing.T) {
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(denyCap(pgdesk.CapAccessAdmin, "")))

	rec := do(admin, httptest.NewRequest("GET", "/admin/_static/pgdesk.css", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("stylesheet status = %d, want 200 so the 403 page renders", rec.Code)
	}
}

// Granting admin access leaves the per-route capabilities in charge: the gate
// must narrow nothing else.
func TestIntegrationAccessAdminGrantedIsUnaffected(t *testing.T) {
	admin, _ := fkAdmin(t)

	for _, path := range []string{"/admin/", "/admin/fk_users", "/admin/fk_users/1"} {
		if rec := do(admin, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

// scopedFKAdmin confines fk_users to the acme tenant while leaving fk_orders
// fully editable, so the only thing standing between the operator and an
// out-of-scope reference is the write path's own check.
func scopedFKAdmin(t *testing.T) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), fkSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	acmeOnly := pgdesk.ScopeOnly(func(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		if attrs.Resource == "fk_users" {
			return []pgdesk.Constraint{pgdesk.Eq("tenant", "acme")}, nil
		}
		return nil, nil
	})
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, acmeOnly)),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("fk_users", func(r *pgdesk.Resource) { r.LabelColumn("email") }),
		pgdesk.WithResource("fk_orders", func(r *pgdesk.Resource) { r.ListDisplay("id", "user_id", "note") }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return admin, pool
}

// authzTokens fetches a form and returns its CSRF cookie token and version token,
// for use with the shared postForm helper (which puts the same token in both the
// cookie and the field).
func authzTokens(t *testing.T, admin *pgdesk.Admin, path string) (token, version string) {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d", path, rec.Code)
	}
	k := reCapCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	if k == nil {
		t.Fatalf("%s set no csrf cookie", path)
	}
	if v := reCapVersion.FindStringSubmatch(rec.Body.String()); v != nil {
		version = v[1]
	}
	return k[1], version
}

// The picker withholds out-of-scope rows; the write path must refuse the same
// keys. Otherwise the scope is advisory -- a hand-written POST reassigns a row to
// a referenced record the operator cannot see, and the accept/reject answer
// itself discloses which keys exist.
func TestIntegrationUpdateRejectsOutOfScopeForeignKey(t *testing.T) {
	admin, pool := scopedFKAdmin(t)

	// user 3 (alan, tenant=other) is never offered.
	_, opts := fetchOptions(t, admin, "/admin/fk_users/options.json")
	for _, l := range labels(opts) {
		if l == "alan@example.com" {
			t.Fatalf("fixture wrong: picker offered the out-of-scope row: %v", labels(opts))
		}
	}

	token, version := authzTokens(t, admin, "/admin/fk_orders/1/edit")
	rec := postForm(admin, "/admin/fk_orders/1/edit", token, url.Values{
		"_version": {version}, "user_id": {"3"}, "note": {"reassigned"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("save status = %d, want 422", rec.Code)
	}

	var got int64
	if err := pool.QueryRow(context.Background(),
		"SELECT user_id FROM fk_orders WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != 1 {
		t.Errorf("fk_orders.user_id = %d, want 1 unchanged", got)
	}
}

// The same rule on INSERT: a scoped operator must not create a row pointing at a
// referenced record outside their scope.
func TestIntegrationCreateRejectsOutOfScopeForeignKey(t *testing.T) {
	admin, pool := scopedFKAdmin(t)

	token, _ := authzTokens(t, admin, "/admin/fk_orders/new")
	rec := postForm(admin, "/admin/fk_orders/new", token, url.Values{
		"user_id": {"3"}, "note": {"smuggled"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("create status = %d, want 422", rec.Code)
	}

	var n int64
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM fk_orders WHERE user_id = 3").Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 0 {
		t.Errorf("%d row(s) created pointing outside the operator's scope", n)
	}
}

// An in-scope key still saves: the check must reject only what the picker would
// have withheld.
func TestIntegrationWriteAcceptsInScopeForeignKey(t *testing.T) {
	admin, pool := scopedFKAdmin(t)

	token, version := authzTokens(t, admin, "/admin/fk_orders/1/edit")
	rec := postForm(admin, "/admin/fk_orders/1/edit", token, url.Values{
		"_version": {version},
		"user_id":  {"2"}, "note": {"adam now"}, // adam, tenant=acme -- offered
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	var got int64
	if err := pool.QueryRow(context.Background(),
		"SELECT user_id FROM fk_orders WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != 2 {
		t.Errorf("fk_orders.user_id = %d, want 2", got)
	}
}

// With no scope on the referenced resource there is nothing to check, so the
// write path must not pay for a lookup or refuse a valid key.
func TestIntegrationWriteUnscopedForeignKeyUnaffected(t *testing.T) {
	admin, pool := fkAdmin(t) // AllowAll, no Scoper

	token, version := authzTokens(t, admin, "/admin/fk_orders/1/edit")
	rec := postForm(admin, "/admin/fk_orders/1/edit", token, url.Values{
		"_version": {version},
		"user_id":  {"3"}, "note": {"fine"}, // out of nobody's scope
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	var got int64
	if err := pool.QueryRow(context.Background(),
		"SELECT user_id FROM fk_orders WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != 3 {
		t.Errorf("fk_orders.user_id = %d, want 3", got)
	}
}

// roleOp is a Principal that declares roles, so pgdesk.Roles can read them.
type roleOp struct{ roles []string }

func (roleOp) SubjectID() string   { return "tester" }
func (roleOp) DisplayName() string { return "Tester" }
func (o roleOp) Roles() []string   { return o.roles }

func withRoles(roles ...string) pgdesk.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := pgdesk.WithPrincipal(r.Context(), roleOp{roles: roles})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// The shipped Roles authorizer end to end: a viewer reads, an editor writes, and
// a role without CapAccessAdmin is locked out of the admin entirely.
func TestIntegrationRolesAuthorizer(t *testing.T) {
	grants := pgdesk.Roles{
		"viewer": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView},
		"editor": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView, pgdesk.CapUpdate},
		"nobody": {pgdesk.CapList}, // deliberately lacks CapAccessAdmin
	}
	build := func(t *testing.T, roles ...string) (*pgdesk.Admin, *pgxpool.Pool) {
		t.Helper()
		pool := capPool(t)
		if _, err := pool.Exec(context.Background(), fkSchema); err != nil {
			t.Fatalf("schema: %v", err)
		}
		admin, err := pgdesk.New(pool,
			pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
			pgdesk.WithAuthorizer(grants),
			pgdesk.WithMiddleware(withRoles(roles...)),
			pgdesk.WithResource("fk_users", func(r *pgdesk.Resource) {
				r.ListDisplay("id", "email")
			}),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = admin.Close() })
		return admin, pool
	}

	t.Run("viewer reads but cannot write", func(t *testing.T) {
		admin, _ := build(t, "viewer")
		if rec := do(admin, httptest.NewRequest("GET", "/admin/fk_users", nil)); rec.Code != http.StatusOK {
			t.Errorf("list = %d, want 200", rec.Code)
		}
		// The edit form requires CapUpdate, which viewer lacks.
		if rec := do(admin, httptest.NewRequest("GET", "/admin/fk_users/1/edit", nil)); rec.Code != http.StatusForbidden {
			t.Errorf("edit form = %d, want 403", rec.Code)
		}
	})

	t.Run("editor may reach the edit form", func(t *testing.T) {
		admin, _ := build(t, "editor")
		if rec := do(admin, httptest.NewRequest("GET", "/admin/fk_users/1/edit", nil)); rec.Code != http.StatusOK {
			t.Errorf("edit form = %d, want 200", rec.Code)
		}
	})

	t.Run("a role without CapAccessAdmin is locked out", func(t *testing.T) {
		admin, _ := build(t, "nobody")
		for _, path := range []string{"/admin/", "/admin/fk_users"} {
			if rec := do(admin, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusForbidden {
				t.Errorf("%s = %d, want 403 -- CapAccessAdmin gates every route", path, rec.Code)
			}
		}
	})

	t.Run("no roles grants nothing", func(t *testing.T) {
		admin, _ := build(t)
		if rec := do(admin, httptest.NewRequest("GET", "/admin/fk_users", nil)); rec.Code != http.StatusForbidden {
			t.Errorf("roleless principal = %d, want 403", rec.Code)
		}
	})
}
