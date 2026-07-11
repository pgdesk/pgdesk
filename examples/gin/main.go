// Command gin mounts the pgdesk admin inside a Gin router.
//
// pgdesk is a plain net/http.Handler, so it drops under any router without an
// adapter. The one integration point worth seeing is authentication: a Gin
// middleware authenticates the request and attaches a pgdesk.Principal to the
// request context; pgdesk reads it for every authorization decision. Swap the
// demo middleware for your real auth and you are done.
//
//	docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
//	  go run .
//
// Then open http://localhost:8080/admin/
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
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

	// A small table with a few rows so there is something to administer.
	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatal(err)
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithTitle("Users Admin (behind Gin)"),
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithResource("users", func(r *pgdesk.Resource) {
			r.Label = "Users"
			r.ListDisplay("id", "email", "active", "created_at")
			r.SearchFields("email")
			r.Filters("active")
			r.Readonly("id", "created_at")
			r.DefaultSort("-created_at")
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer admin.Close()

	r := gin.New()
	r.Use(gin.Recovery())

	// Gin middleware authenticates and hands pgdesk a Principal via the request
	// context. Replace demoOperator with your real user lookup.
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			pgdesk.WithPrincipal(c.Request.Context(), demoOperator{}),
		)
		c.Next()
	})

	// pgdesk is an http.Handler; gin.WrapH mounts it under the base path. The
	// wildcard serves /admin/ and everything below; the bare /admin redirects to
	// the trailing-slash root (what admin.Mount does for you on a net/http mux).
	r.GET("/admin", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/admin/")
	})
	r.Any("/admin/*any", gin.WrapH(admin))

	log.Println("pgdesk behind Gin: http://localhost:8080/admin/")
	log.Fatal(http.ListenAndServe(":8080", r))
}

// demoOperator is a stand-in principal. Your real auth middleware would build one
// from the authenticated session/token.
type demoOperator struct{}

func (demoOperator) SubjectID() string   { return "demo" }
func (demoOperator) DisplayName() string { return "Demo Operator" }

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
