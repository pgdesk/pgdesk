//go:build integration

// Integration tests for the bounded foreign-key picker: the options endpoint
// (authorization, row scope, result cap, search escaping) and the form/detail
// rendering that drives it.
package pgdesk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgdesk/pgdesk"
)

const fkSchema = `
DROP TABLE IF EXISTS fk_orders;
DROP TABLE IF EXISTS fk_users;
CREATE TABLE fk_users (
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL UNIQUE,
    tenant text NOT NULL DEFAULT 'acme'
);
CREATE TABLE fk_orders (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES fk_users(id),
    note    text
);
INSERT INTO fk_users (email, tenant) VALUES
    ('ada@example.com', 'acme'),
    ('adam@example.com', 'acme'),
    ('alan@example.com', 'other'),
    ('grace@example.com', 'acme'),
    ('100%discount@example.com', 'acme');
INSERT INTO fk_orders (user_id, note) VALUES (1, 'first');
-- A composite key: no single form field can carry it, so it has no picker.
DROP TABLE IF EXISTS fk_composite;
CREATE TABLE fk_composite (a bigint NOT NULL, b bigint NOT NULL, PRIMARY KEY (a, b));
INSERT INTO fk_composite VALUES (1, 1), (2, 2);
-- A single-column key with no text column anywhere, so the label falls back to
-- the numeric key and there is nothing a ?q= term can be matched against.
DROP TABLE IF EXISTS fk_numeric;
CREATE TABLE fk_numeric (id bigint PRIMARY KEY, qty bigint NOT NULL);
INSERT INTO fk_numeric VALUES (1, 10), (2, 20);
`

// fkAdmin builds an admin over the fk_* fixture. Extra options are appended last
// so a test can override the authorizer.
func fkAdmin(t *testing.T, extra ...pgdesk.Option) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), fkSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	opts := []pgdesk.Option{
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("fk_users", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "email")
			r.SearchFields("email")
			r.LabelColumn("email")
		}),
		pgdesk.WithResource("fk_orders", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "user_id", "note")
		}),
		pgdesk.WithResource("fk_composite", func(r *pgdesk.Resource) {
			r.ListDisplay("a", "b")
		}),
		pgdesk.WithResource("fk_numeric", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "qty")
		}),
	}
	admin, err := pgdesk.New(pool, append(opts, extra...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return admin, pool
}

// optionsResponse mirrors the JSON contract the picker's client code consumes.
type optionsResponse struct {
	Options []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"options"`
	Truncated  bool `json:"truncated"`
	Searchable bool `json:"searchable"`
}

func fetchOptions(t *testing.T, admin *pgdesk.Admin, url string) (*httptest.ResponseRecorder, optionsResponse) {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", url, nil))
	var got optionsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %s: %v\nbody: %s", url, err, rec.Body.String())
		}
	}
	return rec, got
}

func labels(resp optionsResponse) []string {
	out := make([]string, len(resp.Options))
	for i, o := range resp.Options {
		out[i] = o.Label
	}
	return out
}

// The endpoint answers a search with (value, label) pairs drawn from the
// referenced resource, using the same label column the list page uses.
func TestIntegrationFKOptionsSearch(t *testing.T) {
	admin, _ := fkAdmin(t)

	rec, resp := fetchOptions(t, admin, "/admin/fk_users/options.json?q=ada")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// A search must never be cached: it is scoped to the principal.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	got := labels(resp)
	want := []string{"ada@example.com", "adam@example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("labels = %v, want %v (label-ordered)", got, want)
	}
	for _, o := range resp.Options {
		if o.Value == "" {
			t.Error("every option must carry the key to submit")
		}
	}
	if resp.Truncated {
		t.Error("a 2-row result under the cap must not be truncated")
	}

	// An empty term lists the first page of options rather than erroring.
	if _, all := fetchOptions(t, admin, "/admin/fk_users/options.json"); len(all.Options) != 5 {
		t.Errorf("empty term returned %d options, want all 5", len(all.Options))
	}
}

// The cap is what makes the picker safe on a large table: the endpoint returns at
// most maxOptions rows and says so, never the whole table.
func TestIntegrationFKOptionsIsBounded(t *testing.T) {
	admin, _ := fkAdmin(t, pgdesk.WithMaxOptions(2))

	_, resp := fetchOptions(t, admin, "/admin/fk_users/options.json")
	if len(resp.Options) != 2 {
		t.Errorf("returned %d options, want the 2-row cap", len(resp.Options))
	}
	if !resp.Truncated {
		t.Error("a capped result must report truncated so the UI can say so")
	}
}

// Reading another table's rows obeys that table's own authorization. Without
// CapList on the referenced resource there is no picker data, and the refusal is
// JSON so the client can degrade rather than parse an HTML error page.
func TestIntegrationFKOptionsRequiresListCapability(t *testing.T) {
	deny := pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		if attrs.Resource == "fk_users" && attrs.Capability == pgdesk.CapList {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(deny))

	rec, _ := fetchOptions(t, admin, "/admin/fk_users/options.json?q=ada")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want a JSON refusal", ct)
	}
	if strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Error("a refused request leaked row data")
	}
}

// The picker is a list query, so the referenced resource's row scope applies: an
// operator must not be able to discover rows they could not list.
func TestIntegrationFKOptionsRespectsRowScope(t *testing.T) {
	scoped := pgdesk.ScopeOnly(func(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		if attrs.Resource == "fk_users" {
			return []pgdesk.Constraint{pgdesk.Eq("tenant", "acme")}, nil
		}
		return nil, nil
	})
	admin, _ := fkAdmin(t, pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, scoped)))

	_, resp := fetchOptions(t, admin, "/admin/fk_users/options.json?q=a")
	for _, l := range labels(resp) {
		if l == "alan@example.com" {
			t.Errorf("out-of-scope row leaked into the picker: %v", labels(resp))
		}
	}
	if len(resp.Options) == 0 {
		t.Error("in-scope rows must still be offered")
	}
}

// A search term is data, not syntax: LIKE metacharacters must match literally
// rather than turning the query into a wildcard scan.
func TestIntegrationFKOptionsEscapesSearchTerm(t *testing.T) {
	admin, _ := fkAdmin(t)

	rec, resp := fetchOptions(t, admin, "/admin/fk_users/options.json?q=%25")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	got := labels(resp)
	if len(got) != 1 || got[0] != "100%discount@example.com" {
		t.Errorf("q=%% matched %v, want only the row containing a literal %%", got)
	}
}

// Nothing is exposed until it is named: an unregistered table has no resource, so
// no authorizer and no row scope govern it, and it has no options endpoint.
func TestIntegrationFKOptionsUnknownResourceIs404(t *testing.T) {
	admin, _ := fkAdmin(t)

	if rec, _ := fetchOptions(t, admin, "/admin/pg_class/options.json"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The form field stays a real, submittable input (so the admin still works with
// no JavaScript) and carries the hooks the picker needs: the referenced resource
// to search and the current value's label.
func TestIntegrationFKFormRendersPicker(t *testing.T) {
	admin, _ := fkAdmin(t)

	for _, path := range []string{"/admin/fk_orders/1/edit", "/admin/fk_orders/new"} {
		rec := do(admin, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		row := formRow(t, rec.Body.String(), "user_id")
		if !strings.Contains(row, `name="user_id"`) {
			t.Errorf("%s: the FK field must remain a submittable input:\n%s", path, row)
		}
		if !strings.Contains(row, `data-pg-fk="fk_users"`) {
			t.Errorf("%s: the FK field must name the resource to search:\n%s", path, row)
		}
	}

	// On the edit form the current value's label is shown, not just the raw key.
	rec := do(admin, httptest.NewRequest("GET", "/admin/fk_orders/1/edit", nil))
	row := formRow(t, rec.Body.String(), "user_id")
	if !strings.Contains(row, "ada@example.com") {
		t.Errorf("edit form does not show the current FK label:\n%s", row)
	}
	if got := formValue(t, rec.Body.String(), "user_id"); got != "1" {
		t.Errorf("FK input value = %q, want the raw key %q", got, "1")
	}
}

// The detail page showed raw foreign keys while the list page showed labels. Both
// now resolve the label, and link to the referenced row.
func TestIntegrationFKDetailShowsLabel(t *testing.T) {
	admin, _ := fkAdmin(t)

	rec := do(admin, httptest.NewRequest("GET", "/admin/fk_orders/1", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ada@example.com") {
		t.Errorf("detail page does not resolve the FK label:\n%s", body)
	}
	if !strings.Contains(body, fmt.Sprintf(`href="/admin/fk_users/%d"`, 1)) {
		t.Errorf("detail page does not link to the referenced row:\n%s", body)
	}
}

// noPrincipalAdmin is the same fixture WITHOUT the principal-attaching middleware,
// for asserting what an unauthenticated caller can learn.
func noPrincipalAdmin(t *testing.T) *pgdesk.Admin {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), fkSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithResource("fk_composite", func(r *pgdesk.Resource) { r.ListDisplay("a", "b") }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return admin
}

// Authorization is decided before anything about the resource's shape is
// reported. Otherwise the 404-vs-401 split tells an unauthenticated caller
// whether a resource's key is single- or multi-column, which is schema
// information they have not earned.
func TestIntegrationFKOptionsAuthorizesBeforeReportingShape(t *testing.T) {
	// Unauthenticated: 401, not the composite-key 404.
	rec := do(noPrincipalAdmin(t), httptest.NewRequest("GET", "/admin/fk_composite/options.json", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401 (not a shape-revealing 404)", rec.Code)
	}

	// Authenticated but not permitted to list: 403, not the composite-key 404.
	denied, _ := fkAdmin(t, pgdesk.WithAuthorizer(pgdesk.AuthorizerFunc(
		func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
			if attrs.Resource == "fk_composite" {
				return pgdesk.Deny, nil
			}
			return pgdesk.Allow, nil
		})))
	rec = do(denied, httptest.NewRequest("GET", "/admin/fk_composite/options.json", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("denied status = %d, want 403 (not a shape-revealing 404)", rec.Code)
	}
}

// A resource with no text column to match cannot honour ?q=. It still serves a
// bounded, browsable page -- but it must say the term was not applied, so the
// operator is never shown unfiltered rows that look like search results.
func TestIntegrationFKOptionsReportsUnsearchable(t *testing.T) {
	admin, _ := fkAdmin(t)

	rec, resp := fetchOptions(t, admin, "/admin/fk_numeric/options.json?q=zzz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if resp.Searchable {
		t.Error("a resource with no text column to match must report searchable=false")
	}
	if len(resp.Options) == 0 {
		t.Error("an unsearchable resource must still offer a browsable bounded page")
	}

	// A resource that can be searched says so.
	if _, users := fetchOptions(t, admin, "/admin/fk_users/options.json?q=ada"); !users.Searchable {
		t.Error("a resource with searchable text columns must report searchable=true")
	}
}

// A readonly foreign key still resolves its label. The detail page showed it
// while the form fell back to the raw key for the same column.
func TestIntegrationFKReadonlyStillShowsLabel(t *testing.T) {
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), fkSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("fk_users", func(r *pgdesk.Resource) { r.LabelColumn("email") }),
		pgdesk.WithResource("fk_orders", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "user_id", "note")
			r.Readonly("user_id")
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	rec := do(admin, httptest.NewRequest("GET", "/admin/fk_orders/1/edit", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	row := formRow(t, rec.Body.String(), "user_id")
	if !strings.Contains(row, "readonly") {
		t.Fatalf("expected a readonly input:\n%s", row)
	}
	if !strings.Contains(row, "ada@example.com") {
		t.Errorf("a readonly FK must still show its label:\n%s", row)
	}
}
