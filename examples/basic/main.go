package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

func main() {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatal(err)
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithTitle("Shop Admin"),
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(demoLogin),
		pgdesk.WithResource("orders", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "customer_id", "status", "total", "placed_at")
			r.SearchFields("customer_id")
			r.Filters("status", "placed_at")
			r.FieldLabel("customer_id", "Customer")
			r.DefaultSort("-placed_at")
		}),
		pgdesk.WithResource("customers", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "name", "email", "country")
			r.SearchFields("name", "email")
			r.Filters("country")
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer admin.Close()

	mux := http.NewServeMux()
	admin.Mount(mux)

	log.Println("pgdesk admin: http://localhost:8080/admin/")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func demoLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(pgdesk.WithPrincipal(r.Context(), operator{})))
	})
}

type operator struct{}

func (operator) SubjectID() string   { return "demo" }
func (operator) DisplayName() string { return "Demo Operator" }

const schema = `
CREATE TABLE IF NOT EXISTS customers (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL,
    email      text        NOT NULL UNIQUE,
    country    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint        NOT NULL REFERENCES customers (id),
    status      text          NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'paid', 'shipped', 'refunded')),
    total       numeric(10,2) NOT NULL CHECK (total >= 0),
    placed_at   timestamptz   NOT NULL DEFAULT now()
);

INSERT INTO customers (name, email, country) VALUES
    ('Ada Lovelace',     'ada@example.com',     'UK'),
    ('Alan Turing',      'alan@example.com',    'UK'),
    ('Grace Hopper',     'grace@example.com',   'US'),
    ('Linus Torvalds',   'linus@example.com',   'FI'),
    ('Margaret Hamilton','margaret@example.com','US'),
    ('Edsger Dijkstra',  'edsger@example.com',  'NL'),
    ('Barbara Liskov',   'barbara@example.com', 'US'),
    ('Rob Pike',         'rob@example.com',     'CA'),
    ('Ken Thompson',     'ken@example.com',     'US'),
    ('Donald Knuth',     'donald@example.com',  'US')
ON CONFLICT (email) DO NOTHING;

INSERT INTO orders (customer_id, status, total, placed_at)
SELECT c.id,
       (ARRAY['paid', 'shipped', 'pending', 'paid', 'refunded', 'shipped'])[g % 6 + 1],
       (19 + (g * 211) % 480) + 0.99,
       now() - g * interval '7 hours 13 minutes'
FROM generate_series(1, 40) AS g
JOIN customers c ON c.email = (ARRAY[
    'ada@example.com', 'alan@example.com', 'grace@example.com', 'linus@example.com',
    'margaret@example.com', 'edsger@example.com', 'barbara@example.com',
    'rob@example.com', 'ken@example.com', 'donald@example.com'])[(g * 7) % 10 + 1]
WHERE NOT EXISTS (SELECT 1 FROM orders)
ORDER BY g DESC;
`
