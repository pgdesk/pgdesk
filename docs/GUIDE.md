# pgdesk usage guide

Practical how-to for configuring and operating pgdesk. For the marketing overview
and quickstart, see the [README](../README.md). For the design rationale, see
[ARCHITECTURE.md](ARCHITECTURE.md) and [AUTHORIZATION.md](AUTHORIZATION.md).

## Getting started

```go
pool, _ := pgxpool.New(ctx, dsn)
defer pool.Close()

admin, err := pgdesk.New(pool,
    pgdesk.WithTitle("Operations Admin"),
    pgdesk.WithBasePath("/admin"),
    pgdesk.WithSchemas("public"),
    pgdesk.WithSecretKey(secret),            // required once any mutation is possible
    pgdesk.WithLogger(slog.Default()),       // optional: enable structured logging
    pgdesk.WithQueryTimeout(15*time.Second), // bound query execution time
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
defer admin.Close() // orderly teardown; does NOT close the pool

mux := http.NewServeMux()
admin.Mount(mux)
```

See [examples/basic](../examples/basic) for a minimal, single-file runnable app on
`net/http` -- connect a pool, declare one resource, mount, serve.

## Resource configuration

Each resource can be customized with field display, sorting, search, and foreign-key
labeling. When a resource references another resource as a foreign key, pgdesk shows a
label for the related row. By default, this is the first text column on the referenced table;
use `LabelColumn` to override it:

```go
pgdesk.WithResource("posts", func(r *pgdesk.Resource) {
    r.ListDisplay("id", "title", "author_id", "created_at")
    r.SearchFields("title", "content")
    r.Filters("created_at", "status")
})

pgdesk.WithResource("users", func(r *pgdesk.Resource) {
    r.Label = "Authors"
    r.ListDisplay("id", "email", "full_name")
    r.LabelColumn("full_name") // shows "full_name" in posts.author_id foreign-key labels
})
```

`Label`, `LabelPlural`, and `Description` are free-form display text set directly as struct
fields; they are not validated against the catalog. All other configuration goes through
setter methods (`ListDisplay`, `Filters`, `Key`, ...) that validate column names and fail
`New()` on a mistake.

## Bulk actions, CSV export, and durable audit

```go
pgdesk.WithResource("users", func(r *pgdesk.Resource) {
    r.ListDisplay("id", "email", "status")
    r.Filters("status")

    // A bulk action runs against the selected rows inside the mutation's
    // transaction, audited and CSRF-protected. Returning an error rolls back.
    r.Action("suspend", "Suspend selected",
        func(ctx context.Context, tx pgx.Tx, keys pgdesk.Keys) (string, error) {
            ids, err := keys.Int64s()
            if err != nil { return "", err }
            tag, err := tx.Exec(ctx, "UPDATE users SET status='suspended' WHERE id = ANY($1)", ids)
            if err != nil { return "", err }
            return fmt.Sprintf("Suspended %d users.", tag.RowsAffected()), nil
        },
        pgdesk.WithConfirm("Suspend the selected users?"),
    )
})  // pass to pgdesk.New(pool, ...)
```

The `Keys` type -- received by every action -- provides typed accessors like `Int64s()` and `Strings()` to hand back a typed slice ready to bind to `= ANY($n)`. This is both friendlier than raw `[][]any` and safer: a `[]any` bound as an argument fails to encode when pgx runs without a describe step (behind transaction-pooling proxies like PgBouncer), whereas a concrete `[]int64` or `[]string` encodes in every mode. For composite keys or exotic types, use `Keys.Raw()` or `Keys.Column(name)`.

Every list has a **CSV export** link (`/admin/users/export.csv`) that streams the
current filtered/sorted view through the same authorizer and query builder.

For a **durable audit trail**, implement `TxAuditLogger` -- it writes inside the
mutation's transaction, so a mutation cannot commit without its audit record:

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

## Authorization & row scoping

pgdesk separates authorization (which operations an operator may perform) from row scoping
(which rows they may see or modify). The host supplies an `Authorizer` that answers *may I
perform capability C?* and optionally implements `Scoper` to answer *which rows exist for
me?* Row constraints are pushed into the SQL `WHERE` clause, making them atomic and fast.

A concrete example: tenant scoping that restricts all resources to a single organization:

```go
func tenantScope(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
    org := orgOf(attrs.Principal)  // helper to extract org from Principal
    if org == "" {
        return nil, errors.New("no organization attached to principal")
    }
    return []pgdesk.Constraint{pgdesk.Eq("org_id", org)}, nil
}

admin, err := pgdesk.New(pool,
    pgdesk.WithSecretKey(secret),
    pgdesk.WithAuthorizer(pgdesk.ScopeOnly(tenantScope)),
    // ... other options
)
```

Every list, detail, edit, delete, and bulk action automatically gets `WHERE org_id = $N`
ANDed in. The constraint is checked during schema configuration, so an unknown column or
incompatible operator fails at startup.

See [AUTHORIZATION.md](AUTHORIZATION.md) for the full design, including how row scoping fixes
pagination, concurrency, and bulk-action safety. For a session/auth wiring pattern using
signed cookies, see [examples/session-auth](../examples/session-auth).

## Auto-registration (opt-in convenience)

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

## Customizing the UI

pgdesk's UI is built from embedded HTML templates. To rebrand, restructure the layout,
or change styling without maintaining a fork, use `WithTemplateFS` to overlay custom
templates. Any template file present in your filesystem (matched by name, e.g. `list.html`)
replaces the built-in one; files absent from your overlay fall back to pgdesk's defaults.
A malformed override template fails at `New()` time, never at request time:

```go
import "os"

admin, err := pgdesk.New(pool,
    pgdesk.WithSecretKey(secret),
    pgdesk.WithTemplateFS(os.DirFS("templates")),  // overlay directory with custom .html files
    // ... other options
)
if err != nil {
    log.Fatal(err)
}
```

Place any custom templates in your `templates/` directory. For example, `templates/list.html`
will replace the built-in list view template, while built-in templates like `detail.html`
will continue to be used if you don't provide an override.

## Production features

**Readiness probes:** `Admin.Healthy(ctx)` pings the pool and checks that a catalog
is loaded. Wire it to your `/healthz` endpoint:

```go
mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
    if err := admin.Healthy(r.Context()); err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        fmt.Fprintf(w, "Unhealthy: %v\n", err)
        return
    }
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "OK\n")
})
```

**Catalog reload:** `Admin.Reload(ctx)` rebuilds the catalog without restarting,
degrading to stale on failure. Wire it to `SIGHUP`:

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGHUP)
go func() {
    for range sigCh {
        if err := admin.Reload(context.Background()); err != nil {
            log.Printf("catalog reload failed (keeping previous): %v\n", err)
        }
    }
}()
```

**Timeouts:** `WithQueryTimeout` (default 15s, applied to all DB calls) and
`WithExportTimeout` (default 5m, applied to CSV exports) bound resource use:

```go
pgdesk.WithQueryTimeout(20*time.Second),
pgdesk.WithExportTimeout(10*time.Minute),
```

**CSRF key rotation:** `WithSecretKey` accepts a primary key and retired keys for
verification, so you can rotate without invalidating existing forms:

```go
pgdesk.WithSecretKey(newKey, oldKey1, oldKey2)  // primary, then retired
```

**Observability:** `WithMetrics` hooks into your metrics system (no external
dependencies). Implement the `Metrics` interface to observe request outcomes
and DB operation latencies:

```go
type myMetrics struct{}
func (m myMetrics) ObserveRequest(route string, status int, dur time.Duration) {
    // Emit to Prometheus, Datadog, etc.
}
func (m myMetrics) ObserveQuery(op string, dur time.Duration, err error) {
    // Track query latency and errors
}
// pgdesk.New(pool, pgdesk.WithMetrics(myMetrics{}), ...)
```

**Durable audit:** `WithTxAuditLogger` writes audit records inside the mutation
transaction, so they are guaranteed durably (see the audit example above).

## API stability (SemVer)

pgdesk follows [Semantic Versioning](https://semver.org/). The module path
(`github.com/pgdesk/pgdesk`) is the stability contract. Exported symbols
documented as stable **do not break within a major version**. Anything
experimental lives under a clearly-marked `x/` package and says so in its GoDoc.
Pre-1.0, minor versions may still break; each break is called out in
[CHANGELOG.md](../CHANGELOG.md).
