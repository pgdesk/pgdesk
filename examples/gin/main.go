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

	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			pgdesk.WithPrincipal(c.Request.Context(), demoOperator{}),
		)
		c.Next()
	})

	r.GET("/admin", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/admin/")
	})
	r.Any("/admin/*any", gin.WrapH(admin))

	log.Println("pgdesk behind Gin: http://localhost:8080/admin/")
	log.Fatal(http.ListenAndServe(":8080", r))
}

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
