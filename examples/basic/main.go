// Command basic is the smallest pgdesk app: it exposes a "users" table as an
// admin UI on the standard library's net/http. Read it top to bottom.
//
//	docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
//	  go run ./examples/basic
//
// Then open http://localhost:8080/admin/
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

	// Create a small table with a few rows so there's something to see.
	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatal(err)
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(demoLogin), // supplies a Principal; use real auth in production
		pgdesk.WithResource("users", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "email", "active", "created_at")
			r.SearchFields("email")
			r.Filters("active")
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

// demoLogin attaches a demo operator to every request so the admin is usable
// without a login system. Replace with your real authentication.
func demoLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(pgdesk.WithPrincipal(r.Context(), operator{})))
	})
}

type operator struct{}

func (operator) SubjectID() string   { return "demo" }
func (operator) DisplayName() string { return "Demo Operator" }

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text        NOT NULL UNIQUE,
    active     boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users (email, active) VALUES
    ('ada@example.com', true),
    ('alan@example.com', true),
    ('grace@example.com', false)
ON CONFLICT (email) DO NOTHING;
`
