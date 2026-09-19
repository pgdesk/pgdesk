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

### Mounting under another router (Gin, chi, ...)

`Admin` is a plain `net/http.Handler`, so it mounts under any router with no
adapter. `Admin.Mount(mux)` is a convenience for the standard `*http.ServeMux`
that registers the admin subtree plus a redirect from the bare base path to its
trailing-slash root. Under a third-party router you use the handler directly and
add that one redirect yourself:

```go
// Gin: gin.WrapH turns the handler into a gin handler.
r.GET("/admin", func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/admin/") })
r.Any("/admin/*any", gin.WrapH(admin))

// chi: mount the subtree; Admin routes on the full path internally.
r.Handle("/admin/*", admin)
```

Attach the `Principal` with your router's own middleware (see
[examples/gin](../examples/gin)) or with `WithMiddleware` inside `pgdesk.New`.

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

### Foreign-key pickers

A foreign-key column is a picker, not an id box. On the list and detail pages the referenced
row's label is shown and linked; on a form the field searches the referenced resource as the
operator types, and stores the key.

There is nothing to configure -- pgdesk reads the constraint from the catalog -- but the
picker only appears when all of the following hold, and quietly falls back to a plain text
input otherwise:

- the foreign key is **single-column** (a composite key cannot be carried in one field);
- the referenced table is a **registered resource** (nothing is exposed until you name it,
  including data reached indirectly through a foreign key);
- the principal has **`CapView`** on that referenced resource.

Searching is a list query on the referenced resource, served by
`GET {basePath}/{resource}/options.json?q=`:

- it requires **`CapList`** on the referenced resource and applies that resource's **row
  scope**, so a picker can never reveal a row the operator could not have listed;
- it returns at most `WithMaxOptions(n)` rows (default 20) and reports `truncated` so the UI
  can say "keep typing to narrow" rather than implying the list is complete;
- it matches the referenced resource's `SearchFields` (falling back to the label column),
  case-insensitively, with `LIKE` metacharacters escaped. A resource with no text column to
  match reports `searchable: false`, and the picker says so rather than presenting the
  unfiltered first page as if those rows were matches;
- it refuses in JSON, never HTML, so the client can tell "denied" from "no matches".

```go
pgdesk.WithResource("users", func(r *pgdesk.Resource) {
    r.LabelColumn("email")        // what the picker shows
    r.SearchFields("email", "full_name") // what the picker searches
})
```

The form field remains a real `<input name="author_id">` carrying the key, so the admin still
works with JavaScript disabled: the operator sees the current row's label beside the field and
can type a key by hand. `pgdesk.js` upgrades the input in place into a WAI-ARIA combobox
(arrow keys, Enter, Escape); it adds no inline script and needs no build step.

To opt a column out of the picker, set any explicit widget on it -- an explicit
`r.Widget(...)` always wins over the foreign-key default.

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

### Roles (the short path)

Most hosts want "these roles may do these things". `pgdesk.Roles` is a plain map
and needs no adapter:

```go
pgdesk.WithAuthorizer(pgdesk.Roles{
    "viewer": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView},
    "editor": {pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView,
               pgdesk.CapCreate, pgdesk.CapUpdate},
    "owner":  pgdesk.AllCapabilities(),
})
```

It reads the principal's roles through the optional `RoleBearer` interface, so your
own user type opts in with one method:

```go
func (o operator) Roles() []string { return o.roles } // e.g. from a JWT or a header
```

Three things worth knowing:

- **Include `CapAccessAdmin` in every role that should reach the admin at all.** It
  gates every route, so a role without it is locked out entirely.
- **A principal that does not implement `RoleBearer` holds no roles and is granted
  nothing** -- forgetting the method fails closed rather than opening the admin.
- **Grants are admin-wide by design.** A rule that narrows one resource is a
  separate authorizer, so that adding it can only ever remove access:

```go
pgdesk.WithAuthorizer(pgdesk.DenyOverrides(roles, frozenLedger))
```

Behind a reverse proxy or an external authorization service that already resolved
the identity (Envoy `ext_authz`, oauth2-proxy, Authelia), the middleware is just a
header read -- and that is also how an external identity provider such as Keycloak
is supported without pgdesk carrying any OIDC code:

```go
pgdesk.WithMiddleware(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Only trust these when the proxy is the ONLY route to this handler and
        // it overwrites them on every request. A missing header must grant nothing.
        id, role := r.Header.Get("X-User-ID"), r.Header.Get("X-Workspace-Role")
        if id == "" || role == "" || role == "unknown" {
            next.ServeHTTP(w, r) // no principal -> denied downstream
            return
        }
        p := operator{id: id, roles: []string{role}}
        next.ServeHTTP(w, r.WithContext(pgdesk.WithPrincipal(r.Context(), p)))
    })
})
```

### Writing an authorizer directly



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

## Using pgdesk with an existing ORM (GORM, ent, sqlc, ...)

pgdesk integrates with your **database**, not your ORM. It never sees your Go
structs or model definitions -- it reads `pg_catalog` and runs its own SQL. So it
works, unchanged, alongside any data layer that produces tables: GORM, ent, sqlc,
bun, or hand-written migrations. There is no adapter to write and nothing to
"support" per-ORM; whatever created the tables, pgdesk introspects the result.

The only requirement is that pgdesk wants a native **`*pgxpool.Pool`** (not a
`database/sql` `*sql.DB`). If your app already uses pgx, hand pgdesk the pool you
have. If it doesn't, you can share a single pool between pgdesk and your ORM using
pgx's `database/sql` bridge, `stdlib.OpenDBFromPool`:

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/jackc/pgx/v5/stdlib"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

// One native pgx pool. pgdesk uses it directly.
pool, _ := pgxpool.New(ctx, dsn)

// GORM, backed by the SAME pool via the database/sql bridge -- one pool, not two.
gdb, _ := gorm.Open(postgres.New(postgres.Config{
    Conn: stdlib.OpenDBFromPool(pool),
}), &gorm.Config{})

// GORM owns the schema (models + migrations); pgdesk reads the live result.
gdb.AutoMigrate(&User{})
admin, _ := pgdesk.New(pool, /* ... */)
```

GORM manages your models and migrations; pgdesk introspects the schema those
migrations produced and serves the admin over it. A runnable version of exactly
this -- migrate, seed, and administer a GORM-owned `users` table on one shared
pool -- is in [examples/gorm](../examples/gorm).

**One rule for bulk actions:** an action receives a live `pgx.Tx` and should run
plain parameterized SQL on it. Do not open an ORM session inside an action -- an
ORM call runs outside pgdesk's transaction and forfeits the atomic
mutation-plus-audit guarantee.

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
