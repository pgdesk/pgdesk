//go:build integration

package pgdesk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgdesk/pgdesk"
)

const searchSchema = `
DROP TABLE IF EXISTS srch_orders;
DROP TABLE IF EXISTS srch_customers;
DROP TABLE IF EXISTS srch_numeric;
CREATE TABLE srch_customers (
    id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name   text NOT NULL,
    tenant text NOT NULL
);
CREATE TABLE srch_numeric (id bigint PRIMARY KEY, qty bigint NOT NULL);
CREATE TABLE srch_orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL REFERENCES srch_customers(id),
    numeric_id  bigint REFERENCES srch_numeric(id),
    note        text NOT NULL,
    -- Shares a name with srch_customers.tenant, so a scope predicate that bound
    -- to the outer table instead of the referenced one would match nothing.
    tenant      text NOT NULL DEFAULT 'orders-side'
);
INSERT INTO srch_customers (name, tenant) VALUES
    ('Ada Lovelace', 'acme'),
    ('Adam Smith', 'acme'),
    ('Alan Turing', 'other'),
    ('100% Discount Ltd', 'acme');
INSERT INTO srch_numeric VALUES (1, 10);
INSERT INTO srch_orders (customer_id, numeric_id, note) VALUES
    (1, 1, 'first-order'),
    (2, NULL, 'second-order'),
    (3, NULL, 'third-order'),
    (4, NULL, 'fourth-order'),
    (3, NULL, 'gift for ada');
`

func searchAdmin(t *testing.T, extra ...pgdesk.Option) *pgdesk.Admin {
	t.Helper()
	admin, err := newSearchAdmin(t, func(r *pgdesk.Resource) {
		r.ListDisplay("id", "customer_id", "note")
		r.SearchFields("customer_id", "note")
	}, extra...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return admin
}

func newSearchAdmin(t *testing.T, orders func(*pgdesk.Resource), extra ...pgdesk.Option) (*pgdesk.Admin, error) {
	t.Helper()
	pool := searchPool(t)
	opts := []pgdesk.Option{
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("srch_customers", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "name")
		}),
		pgdesk.WithResource("srch_numeric", nil),
		pgdesk.WithResource("srch_orders", orders),
	}
	admin, err := pgdesk.New(pool, append(opts, extra...)...)
	if err == nil {
		t.Cleanup(func() { _ = admin.Close() })
	}
	return admin, err
}

func searchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), searchSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return pool
}

func searchNotes(t *testing.T, admin *pgdesk.Admin, url string) string {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d; body: %s", url, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func assertNotes(t *testing.T, body string, want, notWant []string) {
	t.Helper()
	for _, n := range want {
		if !strings.Contains(body, n) {
			t.Errorf("result should contain %q", n)
		}
	}
	for _, n := range notWant {
		if strings.Contains(body, n) {
			t.Errorf("result must not contain %q", n)
		}
	}
}

func TestIntegrationSearchThroughForeignKeyLabel(t *testing.T) {
	admin := searchAdmin(t)
	body := searchNotes(t, admin, "/admin/srch_orders?q=ada")

	assertNotes(t, body,
		[]string{"first-order", "second-order", "gift for ada"},
		[]string{"third-order", "fourth-order"})
	if !strings.Contains(body, `name="q"`) {
		t.Error("the list must render a search box")
	}
}

func TestIntegrationSearchThroughForeignKeyRespectsReferencedScope(t *testing.T) {
	scoped := pgdesk.ScopeOnly(func(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		if attrs.Resource == "srch_customers" {
			return []pgdesk.Constraint{pgdesk.Eq("tenant", "acme")}, nil
		}
		return nil, nil
	})
	admin := searchAdmin(t, pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, scoped)))

	body := searchNotes(t, admin, "/admin/srch_orders?q=alan")
	assertNotes(t, body, nil, []string{"third-order", "gift for ada"})

	body = searchNotes(t, admin, "/admin/srch_orders?q=adam")
	assertNotes(t, body, []string{"second-order"}, nil)
}

func TestIntegrationSearchThroughForeignKeyRequiresViewOnReference(t *testing.T) {
	noCustomers := pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		if attrs.Resource == "srch_customers" {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
	admin := searchAdmin(t, pgdesk.WithAuthorizer(noCustomers))

	body := searchNotes(t, admin, "/admin/srch_orders?q=ada")
	assertNotes(t, body, []string{"gift for ada"}, []string{"first-order", "second-order"})
}

func TestIntegrationSearchThroughForeignKeyEscapesLikeWildcards(t *testing.T) {
	admin := searchAdmin(t)
	body := searchNotes(t, admin, "/admin/srch_orders?q=%25")
	assertNotes(t, body, []string{"fourth-order"}, []string{"first-order", "third-order"})
}

func TestIntegrationSearchThroughForeignKeyAppliesToExport(t *testing.T) {
	admin := searchAdmin(t)
	rec := do(admin, httptest.NewRequest("GET", "/admin/srch_orders/export.csv?q=adam", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d; body: %s", rec.Code, rec.Body.String())
	}
	assertNotes(t, rec.Body.String(), []string{"second-order"}, []string{"first-order", "third-order"})
}

func TestIntegrationSearchScopeFailureIsForbidden(t *testing.T) {
	broken := pgdesk.ScopeOnly(func(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		if attrs.Resource == "srch_customers" {
			return nil, errors.New("policy store down")
		}
		return nil, nil
	})
	admin := searchAdmin(t, pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, broken)))

	rec := do(admin, httptest.NewRequest("GET", "/admin/srch_orders?q=ada", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when the referenced scope cannot be resolved", rec.Code)
	}
}

func TestIntegrationSearchSkipsReferenceWithoutTextLabel(t *testing.T) {
	admin, err := newSearchAdmin(t, func(r *pgdesk.Resource) {
		r.ListDisplay("id", "note")
		r.SearchFields("numeric_id", "note")
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := searchNotes(t, admin, "/admin/srch_orders?q=1")
	assertNotes(t, body, nil, []string{"first-order"})

	body = searchNotes(t, admin, "/admin/srch_orders?q=first")
	assertNotes(t, body, []string{"first-order"}, nil)
}

func TestIntegrationSearchWithNothingSearchableMatchesNothing(t *testing.T) {
	noCustomers := pgdesk.AuthorizerFunc(func(ctx context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
		if attrs.Resource == "srch_customers" {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
	admin, err := newSearchAdmin(t, func(r *pgdesk.Resource) {
		r.ListDisplay("id", "note")
		r.SearchFields("customer_id")
	}, pgdesk.WithAuthorizer(noCustomers))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := searchNotes(t, admin, "/admin/srch_orders?q=zzz")
	assertNotes(t, body, nil, []string{"first-order", "second-order", "third-order"})
	if strings.Contains(body, `name="q"`) {
		t.Error("no search box when no search field is usable")
	}

	body = searchNotes(t, admin, "/admin/srch_orders")
	assertNotes(t, body, []string{"first-order"}, nil)
}

func TestIntegrationSearchSkipsHiddenOrRedactedLabel(t *testing.T) {
	for name, conceal := range map[string]func(*pgdesk.Resource, ...string){
		"hidden":   (*pgdesk.Resource).Hidden,
		"redacted": (*pgdesk.Resource).Redact,
	} {
		t.Run(name, func(t *testing.T) {
			admin := searchAdmin(t, pgdesk.WithResource("srch_customers", func(r *pgdesk.Resource) {
				r.LabelColumn("name")
				conceal(r, "name")
			}))
			body := searchNotes(t, admin, "/admin/srch_orders?q=lovelace")
			assertNotes(t, body, nil, []string{"first-order"})
		})
	}
}

func TestIntegrationSearchFieldsRejectsUnsearchableColumn(t *testing.T) {
	_, err := newSearchAdmin(t, func(r *pgdesk.Resource) {
		r.SearchFields("id")
	})
	if !errors.Is(err, pgdesk.ErrUnsearchableColumn) {
		t.Fatalf("New error = %v, want ErrUnsearchableColumn", err)
	}
	if err != nil && !strings.Contains(err.Error(), `"id"`) {
		t.Errorf("the error should name the column: %v", err)
	}
}
