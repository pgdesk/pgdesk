//go:build integration

package pgdesk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgdesk/pgdesk"
)

const uiSchema = `
DROP TABLE IF EXISTS it_ui_child, it_ui_parent, it_ui_zeta, it_ui_alpha;
CREATE TABLE it_ui_parent (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL
);
CREATE TABLE it_ui_child (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id uuid REFERENCES it_ui_parent(id),
    scope     text NOT NULL DEFAULT 'all' CHECK (scope IN ('all', 'assigned')),
    kind      varchar(10) CHECK (kind IN ('a', 'b')),
    note      text CHECK (length(note) < 100),
    at        timestamptz,
    created   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE it_ui_zeta (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);
CREATE TABLE it_ui_alpha (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);
INSERT INTO it_ui_parent (id, name) VALUES ('0190a0b1-0000-7000-8000-00000000000a', 'Acme');
`

func uiAdmin(t *testing.T) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	_, pool := setup(t)
	if _, err := pool.Exec(context.Background(), uiSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS it_ui_child, it_ui_parent, it_ui_zeta, it_ui_alpha`)
	})
	admin, err := pgdesk.New(pool,
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAutoRegister(pgdesk.ExcludeTables("it_audit", "it_nopk", "it_domains", "it_keys_uuid", "it_keys_pair", "it_keys_text", "it_keys_ts")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return admin, pool
}

func page(t *testing.T, admin *pgdesk.Admin, path string) string {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d\n%s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func TestIntegrationCheckListColumnsRenderAsSelect(t *testing.T) {
	admin, _ := uiAdmin(t)
	body := page(t, admin, "/admin/it_ui_child/new")

	scope := regexp.MustCompile(`(?s)<select[^>]*name="scope"[^>]*>(.*?)</select>`).FindStringSubmatch(body)
	if scope == nil {
		t.Fatalf("scope is not a select:\n%s", body)
	}
	for _, v := range []string{`value="all"`, `value="assigned"`} {
		if !strings.Contains(scope[1], v) {
			t.Errorf("scope options lack %s", v)
		}
	}
	if !regexp.MustCompile(`<option value=""[^>]*>Default</option>`).MatchString(scope[1]) {
		t.Errorf("NOT NULL scope with a default should offer \"Default\" as its empty option: %s", scope[1])
	}
	kind := regexp.MustCompile(`(?s)<select[^>]*name="kind"[^>]*>(.*?)</select>`).FindStringSubmatch(body)
	if kind == nil || !strings.Contains(kind[1], `None`) || !strings.Contains(kind[1], `value="b"`) {
		t.Fatalf("nullable kind should be a select with an empty option:\n%v", kind)
	}
	if regexp.MustCompile(`<select[^>]*name="note"`).MatchString(body) {
		t.Error("a non-list CHECK must not become a select")
	}
}

func TestIntegrationForeignKeyFieldSubmitsOnlyAChosenKey(t *testing.T) {
	admin, _ := uiAdmin(t)
	body := page(t, admin, "/admin/it_ui_child/new")

	if !regexp.MustCompile(`<input type="hidden" name="parent_id"`).MatchString(body) {
		t.Fatalf("parent_id is not carried by a hidden input:\n%s", body)
	}
	visible := regexp.MustCompile(`<input[^>]*class="pg-fk-search"[^>]*>`).FindString(body)
	if visible == "" {
		t.Fatalf("no FK search box:\n%s", body)
	}
	if strings.Contains(visible, "name=") {
		t.Fatalf("the FK search box must not be submitted: %s", visible)
	}
}

func TestIntegrationCreateFormOmitsGeneratedKey(t *testing.T) {
	admin, _ := uiAdmin(t)
	body := page(t, admin, "/admin/it_ui_child/new")
	if strings.Contains(body, `name="id"`) {
		t.Fatal("create form asks for a key the database generates")
	}
}

func TestIntegrationTimestampFieldUsesAPickerAndSaves(t *testing.T) {
	admin, pool := uiAdmin(t)
	body := page(t, admin, "/admin/it_ui_child/new")
	if in := regexp.MustCompile(`<input[^>]*name="at"[^>]*>`).FindString(body); !strings.Contains(in, `type="datetime-local"`) {
		t.Fatalf("at is not a datetime picker:\n%s", body)
	}
	token := reCookie.FindStringSubmatch(do(admin, httptest.NewRequest("GET", "/admin/it_ui_child/new", nil)).Header().Get("Set-Cookie"))[1]
	rec := postForm(admin, "/admin/it_ui_child/new", token, url.Values{"scope": {"all"}, "at": {"2026-09-22T10:11"}, "created": {""}, "parent_id": {""}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", rec.Code, rec.Body)
	}
	var at time.Time
	if err := pool.QueryRow(context.Background(), `SELECT at FROM it_ui_child`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 22, 10, 11, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
}

func TestIntegrationSidebarIsAlphabeticalAndGroupedByLetter(t *testing.T) {
	admin, _ := uiAdmin(t)
	body := page(t, admin, "/admin/")
	nav := regexp.MustCompile(`(?s)<nav class="pg-nav".*?</nav>`).FindString(body)
	if nav == "" {
		t.Fatalf("no nav:\n%s", body)
	}
	if !strings.Contains(nav, `type="search"`) {
		t.Error("sidebar has no search box")
	}
	labels := regexp.MustCompile(`data-pg-nav-item[^>]*>([^<]+)</a>`).FindAllStringSubmatch(nav, -1)
	var got []string
	for _, l := range labels {
		got = append(got, strings.TrimSpace(l[1]))
	}
	for i := 1; i < len(got); i++ {
		if strings.ToLower(got[i-1]) > strings.ToLower(got[i]) {
			t.Fatalf("nav not alphabetical: %v", got)
		}
	}
	letters := regexp.MustCompile(`<h2 class="pg-nav-letter"[^>]*>([^<]+)</h2>`).FindAllStringSubmatch(nav, -1)
	seen := map[string]bool{}
	for _, l := range letters {
		seen[l[1]] = true
	}
	for _, want := range []string{"I"} {
		if !seen[want] {
			t.Errorf("letter %s missing: %v", want, letters)
		}
	}
	for _, absent := range []string{"Q", "X"} {
		if seen[absent] {
			t.Errorf("empty letter %s shown", absent)
		}
	}
}

// A database error on a column the form does not show must still reach the
// operator, or the save silently does nothing.
func TestIntegrationErrorOnUnshownColumnIsVisible(t *testing.T) {
	_, pool := setup(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS it_ui_secret; CREATE TABLE it_ui_secret (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text, token text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS it_ui_secret`) })
	admin, err := pgdesk.New(pool, pgdesk.WithAuthorizer(pgdesk.AllowAll), pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithResource("it_ui_secret", func(r *pgdesk.Resource) { r.Hidden("token") }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	token := reCookie.FindStringSubmatch(do(admin, httptest.NewRequest("GET", "/admin/it_ui_secret/new", nil)).Header().Get("Set-Cookie"))[1]
	rec := postForm(admin, "/admin/it_ui_secret/new", token, url.Values{"name": {"x"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	alert := regexp.MustCompile(`(?s)<div class="pg-alert pg-alert-error"[^>]*>(.*?)</div>`).FindStringSubmatch(rec.Body.String())
	if alert == nil || !strings.Contains(alert[1], "Token") {
		t.Fatalf("no visible error naming Token:\n%s", rec.Body)
	}
}
