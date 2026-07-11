// Command gorm shows pgdesk serving a complete admin UI over a database whose
// schema is owned by GORM -- with both libraries sharing a SINGLE connection pool.
//
// The point: you already have a GORM app. You change nothing. GORM keeps defining
// your models and running your migrations; pgdesk reads the *live schema* those
// models produce and gives you list / search / filter / CRUD / bulk actions / CSV
// export / audit over it. pgdesk never sees your Go structs -- it reads the
// database -- so this exact pattern works the same for ent, sqlc, bun, or plain SQL.
//
// One pool, not two: GORM rides pgx's database/sql bridge (stdlib.OpenDBFromPool)
// over the same *pgxpool.Pool that pgdesk uses natively. There is a single
// connection budget to tune.
//
//	docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
//	  go run .
//
// Then open http://localhost:8080/admin/
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/pgdesk/pgdesk"
)

// User is an ordinary GORM model -- the source of truth for the schema. GORM's
// AutoMigrate turns it into a real "users" table; pgdesk then administers that
// table without ever being told the struct exists.
type User struct {
	ID        uint   `gorm:"primaryKey"`
	Email     string `gorm:"uniqueIndex;not null"`
	FullName  string
	Active    bool `gorm:"not null;default:true"`
	CreatedAt time.Time
}

func main() {
	ctx := context.Background()

	// One native pgx pool. pgdesk uses it directly; GORM uses it through pgx's
	// database/sql bridge -- so there is a single pool behind both.
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// GORM, backed by the SAME pool via stdlib.OpenDBFromPool.
	gdb, err := gorm.Open(
		postgres.New(postgres.Config{Conn: stdlib.OpenDBFromPool(pool)}),
		&gorm.Config{},
	)
	if err != nil {
		log.Fatal(err)
	}

	// GORM owns the schema: migrate and seed exactly as your app already does.
	if err := gdb.AutoMigrate(&User{}); err != nil {
		log.Fatal(err)
	}
	seed(gdb)

	// pgdesk reads the live schema GORM just produced. No models are handed to it;
	// it introspects pg_catalog and derives everything -- including that "id" is a
	// generated key it should keep out of the edit form.
	admin, err := pgdesk.New(pool,
		pgdesk.WithTitle("Users Admin (schema by GORM)"),
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(demoLogin), // supplies a Principal; use real auth in production
		pgdesk.WithResource("users", func(r *pgdesk.Resource) {
			r.Label = "Users"
			r.ListDisplay("id", "email", "full_name", "active", "created_at")
			r.SearchFields("email", "full_name")
			r.Filters("active")
			r.Readonly("id", "created_at")
			r.DefaultSort("-created_at")

			// A transactional bulk action over a GORM-owned table -- plain
			// parameterized SQL on the provided pgx.Tx (do NOT reach for gorm here:
			// a GORM call would run outside this transaction and lose the atomic
			// mutation+audit guarantee).
			r.Action("deactivate", "Deactivate selected",
				func(ctx context.Context, tx pgx.Tx, keys pgdesk.Keys) (string, error) {
					ids, err := keys.Int64s()
					if err != nil {
						return "", err
					}
					tag, err := tx.Exec(ctx, `UPDATE users SET active = false WHERE id = ANY($1)`, ids)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("Deactivated %d user(s).", tag.RowsAffected()), nil
				},
				pgdesk.WithConfirm("Deactivate the selected users?"),
			)
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer admin.Close()

	mux := http.NewServeMux()
	admin.Mount(mux)

	log.Println("pgdesk over a GORM schema: http://localhost:8080/admin/")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// seed inserts a few rows through GORM so the admin has something to show.
func seed(gdb *gorm.DB) {
	users := []User{
		{Email: "ada@example.com", FullName: "Ada Lovelace", Active: true},
		{Email: "alan@example.com", FullName: "Alan Turing", Active: true},
		{Email: "grace@example.com", FullName: "Grace Hopper", Active: false},
	}
	for _, u := range users {
		row := u // don't alias the loop variable
		// FirstOrCreate keeps the seed idempotent across restarts.
		gdb.Where(User{Email: row.Email}).FirstOrCreate(&row)
	}
	// Make one user inactive so the "active" filter has a mix to show. (A struct
	// insert can't persist Active=false: GORM treats false as a zero value and the
	// default:true tag wins -- so set it explicitly.)
	gdb.Exec(`UPDATE users SET active = false WHERE email = ?`, "grace@example.com")
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
