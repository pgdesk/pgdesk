//go:build integration

package pgdesk_test

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

// Every row link on a list page must open that row's detail page, whatever the
// key's column types and arity.
func TestIntegrationRowLinksOpenDetailForEveryKeyShape(t *testing.T) {
	_, pool := setup(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS it_keys_uuid, it_keys_pair, it_keys_text, it_keys_ts;
		CREATE TABLE it_keys_uuid (id uuid PRIMARY KEY, note text);
		CREATE TABLE it_keys_pair (a uuid, b uuid, note text, PRIMARY KEY (a, b));
		CREATE TABLE it_keys_text (code text PRIMARY KEY, note text);
		CREATE TABLE it_keys_ts (at timestamptz, n int, note text, PRIMARY KEY (at, n));
		INSERT INTO it_keys_uuid VALUES ('0190a0b1-0000-7000-8000-000000000001', 'u');
		INSERT INTO it_keys_pair VALUES ('0190a0b1-0000-7000-8000-000000000002', '0190a0b1-0000-7000-8000-000000000003', 'p');
		INSERT INTO it_keys_text VALUES ('instago/a b', 't');
		INSERT INTO it_keys_ts VALUES ('2026-09-22 10:11:12.345678+00', 7, 's');`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS it_keys_uuid, it_keys_pair, it_keys_text, it_keys_ts`)
	})
	admin, err := pgdesk.New(pool, pgdesk.WithAuthorizer(pgdesk.AllowAll), pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAutoRegister())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	for _, table := range []string{"it_keys_uuid", "it_keys_pair", "it_keys_text", "it_keys_ts"} {
		t.Run(table, func(t *testing.T) {
			list := do(admin, httptest.NewRequest("GET", "/admin/"+table, nil))
			var rows []string
			for _, m := range regexp.MustCompile(`href="(/admin/`+table+`/([^"/?]+))"`).FindAllStringSubmatch(list.Body.String(), -1) {
				if seg := m[2]; seg != "new" && seg != "export.csv" && !strings.HasSuffix(seg, ".json") {
					rows = append(rows, strings.ReplaceAll(m[1], "&amp;", "&"))
				}
			}
			if len(rows) == 0 {
				t.Fatalf("no row link:\n%s", list.Body)
			}
			for _, href := range rows {
				if rec := do(admin, httptest.NewRequest("GET", href, nil)); rec.Code != 200 {
					t.Errorf("GET %s: %d", href, rec.Code)
				}
			}
		})
	}
}
