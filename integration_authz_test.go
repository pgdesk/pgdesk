//go:build integration

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

func denyCap(cap pgdesk.Capability, resource string) pgdesk.Authorizer {
	return pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		if attrs.Capability == cap && attrs.Resource == resource {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
}

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

func TestIntegrationAccessAdminStillServesAssets(t *testing.T) {
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(denyCap(pgdesk.CapAccessAdmin, "")))

	rec := do(admin, httptest.NewRequest("GET", "/admin/_static/pgdesk.css", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("stylesheet status = %d, want 200 so the 403 page renders", rec.Code)
	}
}

func TestIntegrationAccessAdminGrantedIsUnaffected(t *testing.T) {
	admin, _ := fkAdmin(t)

	for _, path := range []string{"/admin/", "/admin/fk_users", "/admin/fk_users/1"} {
		if rec := do(admin, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

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

func TestIntegrationUpdateRejectsOutOfScopeForeignKey(t *testing.T) {
	admin, pool := scopedFKAdmin(t)

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

func TestIntegrationWriteAcceptsInScopeForeignKey(t *testing.T) {
	admin, pool := scopedFKAdmin(t)

	token, version := authzTokens(t, admin, "/admin/fk_orders/1/edit")
	rec := postForm(admin, "/admin/fk_orders/1/edit", token, url.Values{
		"_version": {version},
		"user_id":  {"2"}, "note": {"adam now"},
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

func TestIntegrationWriteUnscopedForeignKeyUnaffected(t *testing.T) {
	admin, pool := fkAdmin(t)

	token, version := authzTokens(t, admin, "/admin/fk_orders/1/edit")
	rec := postForm(admin, "/admin/fk_orders/1/edit", token, url.Values{
		"_version": {version},
		"user_id":  {"3"}, "note": {"fine"},
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

func TestIntegrationRolesAuthorizer(t *testing.T) {
	grants := pgdesk.Roles{
		"viewer": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView},
		"editor": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView, pgdesk.CapUpdate},
		"nobody": {pgdesk.CapList},
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

func TestIntegrationNavHidesUnlistableResources(t *testing.T) {
	onlyUsers := pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		switch attrs.Capability {
		case pgdesk.CapAccessAdmin:
			return pgdesk.Allow, nil
		case pgdesk.CapList, pgdesk.CapView:
			if attrs.Resource == "fk_users" {
				return pgdesk.Allow, nil
			}
			return pgdesk.Deny, nil
		}
		return pgdesk.Abstain, nil
	})
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(onlyUsers))

	for _, path := range []string{"/admin/", "/admin/fk_users"} {
		rec := do(admin, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "/admin/fk_users") {
			t.Errorf("%s omits a resource the operator MAY list:\n%s", path, body)
		}
		for _, hidden := range []string{"/admin/fk_orders", "/admin/fk_numeric", "/admin/fk_composite"} {
			if strings.Contains(body, hidden) {
				t.Errorf("%s offers %s, which this operator may not list", path, hidden)
			}
		}
	}
}

func TestIntegrationNavShowsEverythingWhenPermitted(t *testing.T) {
	admin, _ := fkAdmin(t)

	rec := do(admin, httptest.NewRequest("GET", "/admin/", nil))
	body := rec.Body.String()
	for _, want := range []string{"/admin/fk_users", "/admin/fk_orders", "/admin/fk_numeric"} {
		if !strings.Contains(body, want) {
			t.Errorf("index omits %s for a fully permitted operator", want)
		}
	}
}
