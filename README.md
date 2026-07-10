# pgdesk

**A PostgreSQL-first admin framework for Go.** Django-Admin-like leverage for
internal tools and operations dashboards -- schema introspection, declarative
resource config in Go, a server-rendered HTML-first UI, and fail-closed safe
defaults for internal production use.

> Usable in minutes, extensible in weeks.

```go
pool, _ := pgxpool.New(ctx, dsn)
defer pool.Close()

admin, err := pgdesk.New(pool,
    pgdesk.WithTitle("Operations Admin"),
    pgdesk.WithBasePath("/admin"),
    pgdesk.WithSchemas("public"),
    pgdesk.WithSecretKey(secret),            // required if mutations enabled (D5)
    pgdesk.WithLogger(slog.Default()),       // structured logs (O5)
    pgdesk.WithQueryTimeout(15*time.Second), // bounded queries (O2)
    pgdesk.WithLoginURL("/login"),
    pgdesk.WithResource("users", func(r *pgdesk.Resource) {
        r.Label = "Users"
        r.ListDisplay("id", "email", "status", "created_at")
        r.SearchFields("email", "full_name")
        r.Filters("status", "created_at")
        r.Readonly("id", "created_at", "updated_at")
        r.DefaultSort("-created_at")
    }),
)
if err != nil {
    log.Fatal(err) // includes resource misconfiguration -- New never panics
}
defer admin.Close() // orderly teardown (O3); does NOT close the pool

mux := http.NewServeMux()
admin.Mount(mux)
```

### Bulk actions, CSV export, and durable audit

```go
pgdesk.WithResource("users", func(r *pgdesk.Resource) {
    r.ListDisplay("id", "email", "status")
    r.Filters("status")

    // A bulk action runs against the selected rows inside the mutation's
    // transaction, audited and CSRF-protected. Returning an error rolls back.
    r.Action("suspend", "Suspend selected",
        func(ctx context.Context, tx pgx.Tx, keys [][]any) (string, error) {
            ids := make([]any, len(keys))
            for i, k := range keys { ids[i] = k[0] }
            tag, err := tx.Exec(ctx, "UPDATE users SET status='suspended' WHERE id = ANY($1)", ids)
            if err != nil { return "", err }
            return fmt.Sprintf("Suspended %d users.", tag.RowsAffected()), nil
        },
        pgdesk.WithConfirm("Suspend the selected users?"),
    )
})  // pass to pgdesk.New(pool, ...)
```

Every list has a **CSV export** link (`/admin/users/export.csv`) that streams the
current filtered/sorted view through the same authorizer and query builder.

For a **durable audit trail**, implement `TxAuditLogger` -- it writes inside the
mutation's transaction, so a mutation cannot commit without its audit record (O4):

```go
type auditLogger struct{}
func (auditLogger) LogAuditTx(ctx context.Context, tx pgx.Tx, e pgdesk.AuditEvent) error {
    _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, resource, row_key, at)
                            VALUES ($1,$2,$3,$4,$5)`,
        e.ActorID, e.Action, e.Resource, e.Key, e.At)
    return err
}
// pgdesk.New(pool, pgdesk.WithTxAuditLogger(auditLogger{}), ...)
```

See [examples/basic](examples/basic) for a minimal, single-file runnable app on
`net/http` -- connect a pool, declare one resource, mount, serve.

### Auto-registration (opt-in convenience)

Introspect everything, expose nothing until named -- or opt into convenience
auto-registration, which exposes every keyed table (skipping keyless tables and,
by default, views), honors an exclude list, and **logs the full exposed set at
startup** so you can immediately spot a leaked PII or system table:

```go
admin, err := pgdesk.New(pool,
    pgdesk.WithSecretKey(secret),
    pgdesk.WithAutoRegister(
        pgdesk.ExcludeTables("schema_migrations", "audit_log"),
        pgdesk.IncludeViews("active_users"),
    ),
)
// Explicit WithResource("users", ...) declarations always win over auto-registered ones.
```

## Why pgdesk

- **PostgreSQL-first, pgx-first.** Not database-agnostic. It reads `pg_catalog`
  to understand your schema and speaks pgx natively. PostgreSQL is the only
  supported backend in v1.
- **Standard library HTTP.** `net/http`, `http.Handler`, `http.ServeMux`,
  `context.Context`. No dependency on Chi/Gin/Echo/Fiber.
- **HTML-first.** `html/template` + embedded assets. No mandatory Node build.
- **Minimal dependency surface.** Runtime deps: `github.com/jackc/pgx/v5` and the
  standard library. Nothing else.
- **Safe by default.** Fail-closed exposure, no request string ever reaching SQL
  as anything but `$N`, no lost updates under concurrent edits, bounded resource
  use, full auditability, clean shutdown, observable internals.

## Security & scope boundary

pgdesk is hardened for a **trusted-operator internal tool**. It defends against
stored XSS from DB content, CSRF, clickjacking, open redirects, SQL injection,
lost updates, resource exhaustion, and audit gaps.

It intentionally does **not** defend against a malicious authenticated operator
(they have legitimate DB access by design), and it **delegates** TLS termination,
network isolation, rate limiting, WAF, and session/auth mechanics to the host
application and infrastructure -- providing clean contracts (`Principal`,
`Middleware`, `Authorizer`, `Metrics`) for each. **Do not deploy it raw to the
public internet expecting more than it claims.**

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full set of locked
decisions (D1-D7, F1-F8, O1-O8).

## API stability promise (SemVer)

pgdesk follows [Semantic Versioning](https://semver.org/). The module path
(`github.com/pgdesk/pgdesk`) is the stability contract. Exported symbols
documented as stable **do not break within a major version**. Anything
experimental lives under a clearly-marked `x/` package and says so in its GoDoc.
Pre-1.0, minor versions may still break; each break is called out in
[CHANGELOG.md](CHANGELOG.md).

## Requirements

- Go 1.25+ (floor set by `github.com/jackc/pgx/v5` v5.10; pgdesk itself only
  needs the `net/http` 1.22 method/path routing)
- PostgreSQL 12+

## Status

**v1 feature-complete**, pre-1.0 API polish ongoing. All planned phases are done:
schema introspection, declarative resources + auto-registration, the full query
planner (filters/search/sort/FK labels), complete CRUD with optimistic
concurrency, bulk actions, CSV export, durable audit, flash/dark-mode/keyboard
UX, and a 25-test real-PostgreSQL integration suite (199 tests total across the
module, race-clean). See [TASKS.md](TASKS.md) for
the phase-by-phase record. No fake stubs: if a feature is listed as done, it works
and is tested.

## License

MIT -- see [LICENSE](LICENSE).
