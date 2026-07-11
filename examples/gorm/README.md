# pgdesk over a GORM schema

A full admin UI over a database whose models are owned by **GORM** — proving
pgdesk is **ORM-agnostic**. GORM defines the models and runs the migration;
pgdesk reads the *live schema* those models produce and serves list, search,
filter, CRUD, a transactional bulk action, and CSV export over it. pgdesk is
never handed a struct — it introspects the database — so the same pattern works
unchanged for ent, sqlc, bun, or hand-written SQL.

**One pool, not two.** GORM rides pgx's `database/sql` bridge
(`stdlib.OpenDBFromPool`) over the very same `*pgxpool.Pool` pgdesk uses natively:

```go
pool, _ := pgxpool.New(ctx, dsn)                                   // native pgx — pgdesk
gdb, _  := gorm.Open(postgres.New(postgres.Config{
    Conn: stdlib.OpenDBFromPool(pool),                             // same pool — GORM
}))
```

This example is a **separate Go module** on purpose: GORM and its dependencies
stay out of pgdesk's own `go.mod`, which keeps pgdesk's runtime dependency tree
down to a single entry (`pgx`).

## Run it

```sh
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16

DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
  go run .
```

Then open <http://localhost:8080/admin/>. GORM migrates and seeds a `users`
table; pgdesk administers it — no model glue in between.

## The one rule

Inside a pgdesk bulk action you get a live `pgx.Tx` and should run plain
parameterized SQL on it (see `main.go`). Don't reach for `gorm` there: a GORM
call would run outside that transaction and forfeit pgdesk's atomic
mutation-plus-audit guarantee.
