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
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(demoLogin),
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
