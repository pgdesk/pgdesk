//go:build integration

package pgdesk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

// A detail page for a row that has a foreign key and a jsonb column. The jsonb value is a Go map, which cannot be a map
// key, and the page used to panic when it looked every column up for a label.
func TestDetailOfARowWithAForeignKeyAndAJSONColumnRenders(t *testing.T) {
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), `
		DROP TABLE IF EXISTS jb_notes; DROP TABLE IF EXISTS jb_owners;
		CREATE TABLE jb_owners (id bigint PRIMARY KEY, name text NOT NULL);
		CREATE TABLE jb_notes (id bigint PRIMARY KEY, owner_id bigint NOT NULL REFERENCES jb_owners(id), body jsonb NOT NULL);
		INSERT INTO jb_owners VALUES (1, 'Ada');
		INSERT INTO jb_notes VALUES (1, 1, '{"a": [1, 2], "b": {"c": true}}');`); err != nil {
		t.Fatal(err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("jb_owners", func(r *pgdesk.Resource) { r.LabelColumn("name") }),
		pgdesk.WithResource("jb_notes", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	rec := do(admin, httptest.NewRequest("GET", "/admin/jb_notes/1", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Ada") {
		t.Fatalf("%d: the page does not render the owner's label\n%s", rec.Code, rec.Body.String())
	}
}
