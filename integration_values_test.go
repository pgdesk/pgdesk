//go:build integration

// Regression tests for value rendering on real PostgreSQL: pgx decodes numeric,
// uuid and json into wrapper types, and a Go-default rendering of those both
// displays nonsense and breaks the edit-form round-trip (the pre-filled value is
// rejected on submit, so the row cannot be saved at all).
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

const valuesSchema = `
DROP TABLE IF EXISTS val_rows;
CREATE TABLE val_rows (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ref     uuid        NOT NULL,
    amount  numeric(10,2) NOT NULL,
    qty     numeric     NOT NULL,
    meta    jsonb       NOT NULL,
    note    text
);
INSERT INTO val_rows (ref, amount, qty, meta, note) VALUES
    ('483f506f-5d40-4ca5-8a65-7f6d997be5ba', 10.50, 3, '{"a": 1}', 'hello');
`

func valuesAdmin(t *testing.T) (*pgdesk.Admin, *pgxpool.Pool) {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), valuesSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("val_rows", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "ref", "amount", "qty", "meta", "note")
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return admin, pool
}

// Every screen that shows a value must show the value, not a Go struct dump.
func TestIntegrationValuesRenderAsText(t *testing.T) {
	admin, _ := valuesAdmin(t)

	for _, path := range []string{"/admin/val_rows", "/admin/val_rows/1", "/admin/val_rows/1/edit", "/admin/val_rows/export.csv"} {
		rec := do(admin, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"10.50", "483f506f-5d40-4ca5-8a65-7f6d997be5ba"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not contain %q:\n%s", path, want, body)
			}
		}
		// A Go struct/slice dump of a pgtype wrapper or a raw byte list.
		for _, bad := range []string{"finite", "map[", "[72 63"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s leaks a Go-formatted value (%q):\n%s", path, bad, body)
			}
		}
	}
}

// The edit form pre-fills current values. Submitting them back unchanged -- the
// operator opens a row, edits one field, presses Save -- must succeed and leave
// the untouched columns exactly as they were.
func TestIntegrationEditFormRoundTripsValues(t *testing.T) {
	admin, pool := valuesAdmin(t)
	ctx := context.Background()

	rec := do(admin, httptest.NewRequest("GET", "/admin/val_rows/1/edit", nil))
	if rec.Code != 200 {
		t.Fatalf("edit form status = %d", rec.Code)
	}
	body := rec.Body.String()
	version := reCapVersion.FindStringSubmatch(body)
	csrf := reCapCSRF.FindStringSubmatch(body)
	cookie := reCapCookie.FindStringSubmatch(rec.Header().Get("Set-Cookie"))
	if version == nil || csrf == nil || cookie == nil {
		t.Fatalf("edit form missing version/csrf:\n%s", body)
	}

	// Resubmit exactly what the form pre-filled, changing only `note`.
	form := url.Values{"_pgdesk_csrf": {csrf[1]}, "_version": {version[1]}, "note": {"edited"}}
	for _, name := range []string{"ref", "amount", "qty", "meta"} {
		form.Set(name, formValue(t, body, name))
	}
	req := httptest.NewRequest("POST", "/admin/val_rows/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-pgdesk_csrf", Value: cookie[1]})

	rec = do(admin, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	var ref, amount, qty, meta, note string
	err := pool.QueryRow(ctx, `SELECT ref::text, amount::text, qty::text, meta::text, note
	                           FROM val_rows WHERE id = 1`).Scan(&ref, &amount, &qty, &meta, &note)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"ref", ref, "483f506f-5d40-4ca5-8a65-7f6d997be5ba"},
		{"amount", amount, "10.50"},
		{"qty", qty, "3"},
		{"meta", meta, `{"a": 1}`},
		{"note", note, "edited"},
	} {
		if c.got != c.want {
			t.Errorf("%s after save = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// An identity-always primary key must not be offered as an editable, required
// input: the write path drops it, so anything typed there is silently discarded.
func TestIntegrationIdentityPKRendersReadonly(t *testing.T) {
	admin, _ := valuesAdmin(t)

	for _, path := range []string{"/admin/val_rows/1/edit", "/admin/val_rows/new"} {
		rec := do(admin, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		row := formRow(t, rec.Body.String(), "id")
		if !strings.Contains(row, "readonly") {
			t.Errorf("%s renders the identity PK as a writable input:\n%s", path, row)
		}
		if strings.Contains(row, "pg-req") {
			t.Errorf("%s marks the identity PK required:\n%s", path, row)
		}
	}
}
