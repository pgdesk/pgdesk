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

type User struct {
	ID        uint   `gorm:"primaryKey"`
	Email     string `gorm:"uniqueIndex;not null"`
	FullName  string
	Active    bool `gorm:"not null;default:true"`
	CreatedAt time.Time
}

func main() {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	gdb, err := gorm.Open(
		postgres.New(postgres.Config{Conn: stdlib.OpenDBFromPool(pool)}),
		&gorm.Config{},
	)
	if err != nil {
		log.Fatal(err)
	}

	if err := gdb.AutoMigrate(&User{}); err != nil {
		log.Fatal(err)
	}
	seed(gdb)

	admin, err := pgdesk.New(pool,
		pgdesk.WithTitle("Users Admin (schema by GORM)"),
		pgdesk.WithSecretKey([]byte("dev-only-secret-change-me")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(demoLogin),
		pgdesk.WithResource("users", func(r *pgdesk.Resource) {
			r.Label = "Users"
			r.ListDisplay("id", "email", "full_name", "active", "created_at")
			r.SearchFields("email", "full_name")
			r.Filters("active")
			r.Readonly("id", "created_at")
			r.DefaultSort("-created_at")

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

func seed(gdb *gorm.DB) {
	users := []User{
		{Email: "ada@example.com", FullName: "Ada Lovelace", Active: true},
		{Email: "alan@example.com", FullName: "Alan Turing", Active: true},
		{Email: "grace@example.com", FullName: "Grace Hopper", Active: false},
	}
	for _, u := range users {
		row := u

		gdb.Where(User{Email: row.Email}).FirstOrCreate(&row)
	}

	gdb.Exec(`UPDATE users SET active = false WHERE email = ?`, "grace@example.com")
}

func demoLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(pgdesk.WithPrincipal(r.Context(), operator{})))
	})
}

type operator struct{}

func (operator) SubjectID() string   { return "demo" }
func (operator) DisplayName() string { return "Demo Operator" }
