//go:build integration

// Package pgdesk_test integration suite (O7). It runs only with the `integration`
// build tag and a live PostgreSQL reachable via PGDESK_TEST_DSN:
//
//	go test -tags=integration -race ./...
//
// It exercises the real product against a real database: introspection of every
// core type category, the list→detail→edit→save flow, the xmin lost-update
// conflict (O1), type-aware fail-closed key decoding (D6), the SQLSTATE error
// mapping (D7), and CSRF enforcement (D5). Nothing about Postgres is mocked.
package pgdesk_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

const schemaSQL = `
DROP TABLE IF EXISTS it_orders;
DROP TABLE IF EXISTS it_users;
DROP TABLE IF EXISTS it_audit;
DROP TABLE IF EXISTS it_nopk;
DROP TABLE IF EXISTS it_domains;
DROP DOMAIN IF EXISTS it_status_domain;
DROP TYPE IF EXISTS it_status;
CREATE TYPE it_status AS ENUM ('active', 'suspended', 'pending');
CREATE DOMAIN it_status_domain AS it_status;
CREATE TABLE it_domains (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    st it_status_domain NOT NULL DEFAULT 'active'
);
INSERT INTO it_domains (st) VALUES ('active');
CREATE TABLE it_users (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email       text        NOT NULL UNIQUE,
    full_name   text,
    status      it_status   NOT NULL DEFAULT 'pending',
    is_admin    boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);
-- A table auto-registration should be told to exclude.
CREATE TABLE it_audit (
    id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    detail text
);
-- A keyless table auto-registration must skip (no primary key).
CREATE TABLE it_nopk (
    a int,
    b int
);
INSERT INTO it_users (email, full_name, status) VALUES
    ('ada@example.com',  'Ada Lovelace', 'active'),
    ('alan@example.com', 'Alan Turing',  'pending');
-- A table with a foreign key, for batched FK-label lookups (D4).
CREATE TABLE it_orders (
    id      bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint  NOT NULL REFERENCES it_users(id),
    total   numeric NOT NULL DEFAULT 0
);
INSERT INTO it_orders (user_id, total) VALUES (1, 10), (1, 20), (2, 5);
-- Durable audit sink for the transactional-audit test (O4).
DROP TABLE IF EXISTS it_audit_log;
CREATE TABLE it_audit_log (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    action   text NOT NULL,
    resource text NOT NULL,
    row_key  text,
    actor_id text
);
`

type principal struct{}

func (principal) SubjectID() string   { return "tester" }
func (principal) DisplayName() string { return "Tester" }

func withPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(pgdesk.WithPrincipal(r.Context(), principal{})))
	})
}

func setup(t *testing.T) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PGDESK_TEST_DSN")
	if dsn == "" {
		t.Skip("PGDESK_TEST_DSN not set; skipping integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		t.Fatalf("load schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithBasePath("/admin"),
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll{}),
		pgdesk.WithMiddleware(withPrincipal),
	)
	if err != nil {
		pool.Close()
		t.Fatalf("New: %v", err)
	}
	admin.Resource("it_users", func(r *pgdesk.Resource) {
		r.LabelPlural = "Users"
		r.ListDisplay("id", "email", "status", "created_at")
		r.SearchFields("email", "full_name")
		r.Filters("status", "created_at")
		r.Readonly("id", "created_at")
		r.DefaultSort("-created_at")
		r.Action("activate", "Activate selected", activateUsers,
			pgdesk.WithConfirm("Activate the selected users?"))
	})
	t.Cleanup(func() { _ = admin.Close(); pool.Close() })
	return admin, pool
}

// activateUsers is a bulk action used by the integration tests.
func activateUsers(ctx context.Context, tx pgx.Tx, keys [][]any) (string, error) {
	ids := make([]any, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k[0])
	}
	tag, err := tx.Exec(ctx, "UPDATE it_users SET status='active' WHERE id = ANY($1)", ids)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Activated %d user(s).", tag.RowsAffected()), nil
}

func do(admin *pgdesk.Admin, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	return rec
}

var (
	reVersion = regexp.MustCompile(`name="_version" value="([^"]*)"`)
	reCookie  = regexp.MustCompile(`__Host-pgdesk_csrf=([^;]+)`)
)

func TestIntegrationListAndDetail(t *testing.T) {
	admin, _ := setup(t)

	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users", nil))
	if rec.Code != 200 {
		t.Fatalf("list status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Fatalf("list missing seeded row:\n%s", rec.Body.String())
	}
	// Security headers present (F2/F3).
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("missing CSP header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options: DENY")
	}

	rec = do(admin, httptest.NewRequest("GET", "/admin/it_users/1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Fatalf("detail failed: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationKeyDecodeFailsClosed(t *testing.T) {
	admin, _ := setup(t)
	// A non-numeric key for a bigint PK must be a 400, never a 500 (D6).
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/"+url.PathEscape("'; drop table it_users"), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad key status = %d, want 400", rec.Code)
	}
}

// editToken fetches the edit form and returns (cookieToken, version).
func editToken(t *testing.T, admin *pgdesk.Admin, id string) (string, string) {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/"+id+"/edit", nil))
	if rec.Code != 200 {
		t.Fatalf("edit form status = %d\n%s", rec.Code, rec.Body.String())
	}
	cm := reCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	if cm == nil {
		t.Fatalf("no CSRF cookie set: %q", rec.Header().Get("Set-Cookie"))
	}
	vm := reVersion.FindStringSubmatch(rec.Body.String())
	if vm == nil {
		t.Fatalf("no version field in form:\n%s", rec.Body.String())
	}
	return cm[1], vm[1]
}

func postEdit(admin *pgdesk.Admin, id, token, version string, form url.Values) *httptest.ResponseRecorder {
	form.Set("_pgdesk_csrf", token)
	form.Set("_version", version)
	req := httptest.NewRequest("POST", "/admin/it_users/"+id+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: token})
	return do(admin, req)
}

func TestIntegrationUpdateSucceeds(t *testing.T) {
	admin, pool := setup(t)
	token, version := editToken(t, admin, "1")

	form := url.Values{}
	form.Set("email", "ada.new@example.com")
	form.Set("full_name", "Ada L")
	form.Set("status", "suspended")
	rec := postEdit(admin, "1", token, version, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}

	var email, status string
	if err := pool.QueryRow(context.Background(),
		"SELECT email, status::text FROM it_users WHERE id=1").Scan(&email, &status); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if email != "ada.new@example.com" || status != "suspended" {
		t.Fatalf("row not updated: email=%q status=%q", email, status)
	}
}

func TestIntegrationOptimisticConflict(t *testing.T) {
	admin, _ := setup(t)
	token, staleVersion := editToken(t, admin, "1")

	// First update consumes the version, bumping xmin.
	f1 := url.Values{}
	f1.Set("email", "ada.v1@example.com")
	f1.Set("status", "active")
	if rec := postEdit(admin, "1", token, staleVersion, f1); rec.Code != http.StatusSeeOther {
		t.Fatalf("first update status = %d, want 303", rec.Code)
	}

	// Second update with the now-stale version must 409 (O1).
	f2 := url.Values{}
	f2.Set("email", "ada.v2@example.com")
	f2.Set("status", "pending")
	rec := postEdit(admin, "1", token, staleVersion, f2)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409 Conflict", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "changed by someone else") {
		t.Fatalf("conflict page missing notice:\n%s", rec.Body.String())
	}
}

func TestIntegrationDuplicateKeyMapped(t *testing.T) {
	admin, _ := setup(t)
	token, version := editToken(t, admin, "1")

	// Set row 1's email to row 2's → 23505 unique_violation mapped to a field
	// error, re-rendered form (D7), status 422.
	form := url.Values{}
	form.Set("email", "alan@example.com")
	form.Set("status", "active")
	rec := postEdit(admin, "1", token, version, form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate status = %d, want 422\n%s", rec.Code, rec.Body.String())
	}
	// With unique-constraint introspection (Phase 2), 23505 attaches "must be
	// unique" to the specific field (D7).
	body := rec.Body.String()
	if !strings.Contains(body, "must be unique") {
		t.Fatalf("missing per-field unique error:\n%s", body)
	}
	if !strings.Contains(body, "pg-has-error") {
		t.Fatalf("expected the email field flagged with an error class:\n%s", body)
	}
}

// setupWith builds an admin from the given options after loading the schema.
func setupWith(t *testing.T, opts ...pgdesk.Option) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PGDESK_TEST_DSN")
	if dsn == "" {
		t.Skip("PGDESK_TEST_DSN not set; skipping integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		t.Fatalf("load schema: %v", err)
	}
	base := []pgdesk.Option{
		pgdesk.WithBasePath("/admin"),
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll{}),
		pgdesk.WithMiddleware(withPrincipal),
	}
	admin, err := pgdesk.New(pool, append(base, opts...)...)
	if err != nil {
		pool.Close()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(); pool.Close() })
	return admin, pool
}

// TestIntegrationAutoRegister proves D2: auto-registration exposes keyed tables,
// honors the exclude list, skips keyless tables, and is driven entirely from the
// catalog (no explicit Resource calls).
func TestIntegrationAutoRegister(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithAutoRegister(pgdesk.ExcludeTables("it_audit")))
	// Force build (Mount finalizes the resource set).
	mux := http.NewServeMux()
	admin.Mount(mux)

	rec := do(admin, httptest.NewRequest("GET", "/admin/", nil))
	if rec.Code != 200 {
		t.Fatalf("index status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Match exact hrefs (a substring check would confuse it_audit / it_audit_log).
	if !strings.Contains(body, `href="/admin/it_users"`) {
		t.Fatalf("auto-register did not expose it_users:\n%s", body)
	}
	if strings.Contains(body, `href="/admin/it_audit"`) {
		t.Fatalf("excluded table it_audit was exposed:\n%s", body)
	}
	if strings.Contains(body, `href="/admin/it_nopk"`) {
		t.Fatalf("keyless table it_nopk should have been skipped:\n%s", body)
	}
	// The exposed resource is reachable.
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_users", nil)); rec.Code != 200 {
		t.Fatalf("auto-registered list status = %d", rec.Code)
	}
	// The excluded table is not routable.
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_audit", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("excluded table status = %d, want 404", rec.Code)
	}
}

// TestIntegrationReloadRebuildsResources proves D1: Reload rebuilds resources
// against the new catalog, so a schema change (a new column) is reflected without
// reconstructing the Admin.
func TestIntegrationReloadRebuildsResources(t *testing.T) {
	admin, pool := setup(t) // registers it_users with default detail columns
	mux := http.NewServeMux()
	admin.Mount(mux)

	// The new column must not exist yet in the edit form.
	if _, ver := editToken(t, admin, "1"); ver == "" {
		t.Fatal("expected a version token")
	}
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/1/edit", nil)); strings.Contains(rec.Body.String(), "nickname") {
		t.Fatal("nickname column should not exist before the migration")
	}

	if _, err := pool.Exec(context.Background(), "ALTER TABLE it_users ADD COLUMN nickname text"); err != nil {
		t.Fatalf("alter table: %v", err)
	}
	if err := admin.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/1/edit", nil))
	if !strings.Contains(rec.Body.String(), "nickname") {
		t.Fatalf("reload did not pick up the new column:\n%s", rec.Body.String())
	}
}

// TestIntegrationDomainOverEnum proves D3 domain resolution: a column typed as a
// DOMAIN over an enum is classified as an enum, so its form widget is a <select>
// populated with the enum labels.
func TestIntegrationDomainOverEnum(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithAutoRegister(pgdesk.ExcludeTables("it_audit")))
	mux := http.NewServeMux()
	admin.Mount(mux)

	rec := do(admin, httptest.NewRequest("GET", "/admin/it_domains/1/edit", nil))
	if rec.Code != 200 {
		t.Fatalf("edit form status = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<select id="f_st"`) {
		t.Fatalf("domain-over-enum column should render a <select>:\n%s", body)
	}
	for _, label := range []string{"active", "suspended", "pending"} {
		if !strings.Contains(body, ">"+label+"<") {
			t.Fatalf("enum option %q missing from select:\n%s", label, body)
		}
	}
}

func TestIntegrationSearch(t *testing.T) {
	admin, _ := setup(t) // it_users has SearchFields(email, full_name)
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?q=Lovelace", nil))
	if rec.Code != 200 {
		t.Fatalf("search status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ada@example.com") {
		t.Fatalf("search should match Ada:\n%s", body)
	}
	if strings.Contains(body, "alan@example.com") {
		t.Fatalf("search should exclude Alan:\n%s", body)
	}
}

func TestIntegrationFilterEnum(t *testing.T) {
	admin, _ := setup(t) // it_users has Filters(status, created_at)
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?f_status=active", nil))
	if rec.Code != 200 {
		t.Fatalf("filter status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ada@example.com") || strings.Contains(body, "alan@example.com") {
		t.Fatalf("enum filter did not restrict to active:\n%s", body)
	}
	// An invalid enum filter value fails closed (D3).
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?f_status=bogus", nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter status = %d, want 400", rec.Code)
	}
}

func TestIntegrationSort(t *testing.T) {
	admin, _ := setup(t)
	asc := do(admin, httptest.NewRequest("GET", "/admin/it_users?sort=email", nil)).Body.String()
	if strings.Index(asc, "ada@example.com") > strings.Index(asc, "alan@example.com") {
		t.Fatal("ascending email sort should place ada before alan")
	}
	desc := do(admin, httptest.NewRequest("GET", "/admin/it_users?sort=-email", nil)).Body.String()
	if strings.Index(desc, "alan@example.com") > strings.Index(desc, "ada@example.com") {
		t.Fatal("descending email sort should place alan before ada")
	}
	// A non-displayed sort column fails closed (D3).
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?sort=is_admin", nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("sort by hidden column status = %d, want 400", rec.Code)
	}
}

func TestIntegrationPageSizeClampedAndPaged(t *testing.T) {
	admin, _ := setup(t)
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?page_size=1", nil))
	body := rec.Body.String()
	// Two users seeded; page size 1 → a Next link must appear.
	if !strings.Contains(body, "Next") {
		t.Fatalf("expected pagination Next link at page_size=1:\n%s", body)
	}
}

// TestIntegrationFKLabels proves D4: a foreign-key column renders the referenced
// row's label (via one batched ANY($1) lookup) and links to its detail page.
func TestIntegrationFKLabels(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithAutoRegister(pgdesk.ExcludeTables("it_audit")))
	mux := http.NewServeMux()
	admin.Mount(mux)

	rec := do(admin, httptest.NewRequest("GET", "/admin/it_orders", nil))
	if rec.Code != 200 {
		t.Fatalf("orders list status = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// user_id 1 → ada's label (email, the first text column) linked to her detail.
	if !strings.Contains(body, `href="/admin/it_users/1"`) {
		t.Fatalf("FK cell should link to referenced detail:\n%s", body)
	}
	if !strings.Contains(body, "ada@example.com") {
		t.Fatalf("FK cell should show the referenced label:\n%s", body)
	}
}

// createToken fetches the create form and returns its CSRF cookie token.
func createToken(t *testing.T, admin *pgdesk.Admin) string {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/new", nil))
	if rec.Code != 200 {
		t.Fatalf("create form status = %d\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `action="/admin/it_users/new"`) {
		t.Fatalf("create form missing create action:\n%s", rec.Body.String())
	}
	cm := reCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	if cm == nil {
		t.Fatal("no CSRF cookie on create form")
	}
	return cm[1]
}

func TestIntegrationCreate(t *testing.T) {
	admin, pool := setup(t)
	token := createToken(t, admin)

	form := url.Values{}
	form.Set("email", "grace@example.com")
	form.Set("full_name", "Grace Hopper")
	form.Set("status", "active")
	// id/created_at are readonly/generated → omitted; DB defaults apply.
	rec := postForm(admin, "/admin/it_users/new", token, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/it_users/") {
		t.Fatalf("create should redirect to the new detail, got %q", loc)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM it_users WHERE email='grace@example.com'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row not inserted, count=%d", count)
	}
}

func TestIntegrationCreateDuplicateMapped(t *testing.T) {
	admin, _ := setup(t)
	token := createToken(t, admin)
	form := url.Values{}
	form.Set("email", "ada@example.com") // already exists
	form.Set("status", "active")
	rec := postForm(admin, "/admin/it_users/new", token, form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate create status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "must be unique") {
		t.Fatalf("duplicate create should flag the email field:\n%s", rec.Body.String())
	}
}

func TestIntegrationDelete(t *testing.T) {
	admin, pool := setup(t)
	// Insert an unreferenced user so the delete isn't blocked by an FK.
	var id int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO it_users(email,status) VALUES('temp@example.com','active') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(id, 10)
	token, _ := editToken(t, admin, idStr)

	rec := postForm(admin, "/admin/it_users/"+idStr+"/delete", token, url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/it_users" {
		t.Fatalf("delete should redirect to list, got %q", loc)
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM it_users WHERE id=$1", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("row not deleted, count=%d", count)
	}
}

// TestIntegrationDeleteBlockedByFK proves a foreign-key-restricted delete surfaces
// a clean 409 rather than a 500 (D7): user 1 is referenced by it_orders.
func TestIntegrationDeleteBlockedByFK(t *testing.T) {
	admin, _ := setup(t)
	token, _ := editToken(t, admin, "1")
	rec := postForm(admin, "/admin/it_users/1/delete", token, url.Values{})
	if rec.Code != http.StatusConflict {
		t.Fatalf("FK-blocked delete status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "other records depend on it") {
		t.Fatalf("missing FK-block message:\n%s", rec.Body.String())
	}
}

func TestIntegrationDeleteRequiresCSRF(t *testing.T) {
	admin, _ := setup(t)
	req := httptest.NewRequest("POST", "/admin/it_users/1/delete", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := do(admin, req); rec.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF status = %d, want 403", rec.Code)
	}
}

// postForm submits a form with a valid CSRF cookie+field derived from token.
func postForm(admin *pgdesk.Admin, path, token string, form url.Values) *httptest.ResponseRecorder {
	form.Set("_pgdesk_csrf", token)
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: token})
	return do(admin, req)
}

// TestIntegrationFlashAfterMutation proves the signed one-shot flash survives the
// post-mutation redirect and renders (escaped) on the next GET, then is cleared.
func TestIntegrationFlashAfterMutation(t *testing.T) {
	admin, _ := setup(t)
	token, version := editToken(t, admin, "1")

	form := url.Values{}
	form.Set("email", "ada.flash@example.com")
	form.Set("status", "active")
	rec := postEdit(admin, "1", token, version, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d", rec.Code)
	}
	// Grab the flash cookie the redirect set.
	var flash *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pgdesk_flash" {
			flash = c
		}
	}
	if flash == nil {
		t.Fatal("mutation did not set a flash cookie")
	}

	// Follow to the detail page carrying the flash cookie.
	req := httptest.NewRequest("GET", "/admin/it_users/1", nil)
	req.AddCookie(flash)
	got := do(admin, req)
	if !strings.Contains(got.Body.String(), "pg-flash") || !strings.Contains(got.Body.String(), "saved") {
		t.Fatalf("flash not rendered on next GET:\n%s", got.Body.String())
	}
	// The flash cookie must be cleared by the display request.
	cleared := false
	for _, c := range got.Result().Cookies() {
		if c.Name == "pgdesk_flash" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("flash cookie was not cleared after display")
	}
}

func TestIntegrationBulkAction(t *testing.T) {
	admin, pool := setup(t)
	// Both seeded users → pending for alan, active for ada; activate both.
	token, _ := editToken(t, admin, "1")

	form := url.Values{}
	form.Set("_action", "activate")
	form.Add("key", "1")
	form.Add("key", "2")
	rec := postForm(admin, "/admin/it_users/action", token, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("action status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}
	var pending int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM it_users WHERE status <> 'active'").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("bulk action did not activate all users, %d still not active", pending)
	}
}

func TestIntegrationBulkActionRequiresCSRF(t *testing.T) {
	admin, _ := setup(t)
	form := url.Values{}
	form.Set("_action", "activate")
	form.Add("key", "1")
	req := httptest.NewRequest("POST", "/admin/it_users/action", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := do(admin, req); rec.Code != http.StatusForbidden {
		t.Fatalf("action without CSRF = %d, want 403", rec.Code)
	}
}

func TestIntegrationBulkActionUnknown(t *testing.T) {
	admin, _ := setup(t)
	token, _ := editToken(t, admin, "1")
	form := url.Values{}
	form.Set("_action", "nuke") // not registered
	form.Add("key", "1")
	if rec := postForm(admin, "/admin/it_users/action", token, form); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", rec.Code)
	}
}

func TestIntegrationExportCSV(t *testing.T) {
	admin, _ := setup(t)
	rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/export.csv", nil))
	if rec.Code != 200 {
		t.Fatalf("export status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Email") || !strings.Contains(body, "ada@example.com") {
		t.Fatalf("csv missing header or data:\n%s", body)
	}
	// Filters apply to the export too (D3/F7).
	filtered := do(admin, httptest.NewRequest("GET", "/admin/it_users/export.csv?f_status=active", nil)).Body.String()
	if !strings.Contains(filtered, "ada@example.com") || strings.Contains(filtered, "alan@example.com") {
		t.Fatalf("filtered export not applied:\n%s", filtered)
	}
}

// dbAudit is a transactional audit logger for the O4 durability test.
type dbAudit struct{}

func (dbAudit) LogAuditTx(ctx context.Context, tx pgx.Tx, e pgdesk.AuditEvent) error {
	_, err := tx.Exec(ctx,
		"INSERT INTO it_audit_log (action, resource, row_key, actor_id) VALUES ($1,$2,$3,$4)",
		string(e.Action), e.Resource, e.Key, e.ActorID)
	return err
}

// TestIntegrationTransactionalAudit proves O4: the audit row is written inside
// the mutation's transaction, so a successful update leaves exactly one audit row.
func TestIntegrationTransactionalAudit(t *testing.T) {
	dsn := os.Getenv("PGDESK_TEST_DSN")
	if dsn == "" {
		t.Skip("PGDESK_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll{}),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithTxAuditLogger(dbAudit{}),
	)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	admin.Resource("it_users", func(r *pgdesk.Resource) {
		r.ListDisplay("id", "email", "status")
		r.Readonly("id", "created_at")
	})
	t.Cleanup(func() { _ = admin.Close(); pool.Close() })

	token, version := editToken(t, admin, "1")
	form := url.Values{}
	form.Set("email", "ada.audited@example.com")
	form.Set("status", "active")
	if rec := postEdit(admin, "1", token, version, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d", rec.Code)
	}

	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM it_audit_log WHERE action='update' AND resource='it_users' AND actor_id='tester'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 audit row, got %d", n)
	}
}

func TestIntegrationCSRFRejected(t *testing.T) {
	admin, _ := setup(t)
	// POST with neither cookie nor form token must be rejected (D5).
	form := url.Values{}
	form.Set("email", "x@example.com")
	req := httptest.NewRequest("POST", "/admin/it_users/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := do(admin, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing-CSRF status = %d, want 403", rec.Code)
	}
}
