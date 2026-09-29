<h1 align="center">pgdesk</h1>

<p align="center">
  <b>An admin panel for your PostgreSQL database, mounted inside your own Go server.</b><br>
  Hand it a <code>pgxpool</code>. It reads your schema and serves lists, search, filters, forms and CSV export.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/pgdesk/pgdesk"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/pgdesk/pgdesk.svg"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/screenshots/orders-dark.png">
  <img alt="pgdesk showing an orders table: customer names, status, totals and dates, with search, filters and CSV export" src=".github/screenshots/orders-light.png">
</picture>

## The whole integration

That screen is [`examples/basic`](examples/basic). This is all the admin code in it:

```go
admin, err := pgdesk.New(pool, // your existing *pgxpool.Pool
    pgdesk.WithTitle("Shop Admin"),
    pgdesk.WithSecretKey(secret),           // signs CSRF tokens
    pgdesk.WithMiddleware(yourAuth),        // who is signed in
    pgdesk.WithAuthorizer(pgdesk.AllowAll), // what they may do
    pgdesk.WithResource("orders", func(r *pgdesk.Resource) {
        r.ListDisplay("id", "customer_id", "status", "total", "placed_at")
        r.SearchFields("customer_id") // find orders by the customer's name
        r.Filters("status", "placed_at")
        r.FieldLabel("customer_id", "Customer")
        r.DefaultSort("-placed_at")
    }),
    pgdesk.WithResource("customers", func(r *pgdesk.Resource) {
        r.ListDisplay("id", "name", "email", "country")
        r.SearchFields("name", "email")
        r.Filters("country")
    }),
)

admin.Mount(mux) // live at /admin/
```

The code only names columns. pgdesk works out the rest from the database:

| In your schema | What you get |
|---|---|
| `customer_id REFERENCES customers` | Shows **Ada Lovelace** instead of `1`, links to her record, gives the form a searchable customer picker, and lets you search orders by customer name |
| `CHECK (status IN ('pending', 'paid', ...))` | A dropdown in the form and in the filter |
| `GENERATED ALWAYS AS IDENTITY`, `DEFAULT now()` | `id` is read-only; leave `placed_at` empty to get the default |
| `timestamptz` | A date-range filter |
| A view PostgreSQL can't update | No edit button |

<table>
  <tr>
    <td width="50%">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset=".github/screenshots/search-dark.png">
        <img alt="Searching orders for 'ada' returns Ada Lovelace's orders" src=".github/screenshots/search-light.png">
      </picture>
    </td>
    <td width="50%">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset=".github/screenshots/edit-dark.png">
        <img alt="Editing an order: customer picker, status dropdown, read-only id" src=".github/screenshots/edit-light.png">
      </picture>
    </td>
  </tr>
  <tr>
    <td align="center">Search follows the foreign key: "ada" finds her orders</td>
    <td align="center">The form comes from the column types and constraints</td>
  </tr>
</table>

## Try it

```sh
docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=pgdesk postgres:16
git clone https://github.com/pgdesk/pgdesk && cd pgdesk
DATABASE_URL=postgres://postgres:pgdesk@localhost:5432/postgres go run ./examples/basic
# open http://localhost:8080/admin/
```

## How it fits

```
browser ─▶ your Go server ─▶ your auth middleware ─▶ pgdesk (an http.Handler) ─▶ PostgreSQL
                             "who is this?"           schema from pg_catalog,
                                                      parameterized SQL only
```

- **Keep your stack.** It works with any router ([Gin](examples/gin)) and any ORM ([GORM](examples/gorm)), because pgdesk reads the database, not your models. Its only dependency is `pgx`.
- **Keep your auth.** pgdesk has no login page. Your middleware says *who* the user is ([example](examples/session-auth)). An `Authorizer` decides *what* they may do. An optional `Scoper` decides *which rows* they see, and pgdesk adds that to the SQL `WHERE`.
- **Nothing is exposed until you name it.** It shows only the tables you list, or all of them if you opt into `WithAutoRegister`.

It also includes bulk actions, an audit log written in the same transaction as each change, protection against lost updates from concurrent edits, CSRF and CSP, dark mode, and keyboard shortcuts.

pgdesk is built for internal tools used by trusted operators. Put it behind your auth and your network, not on the open internet.

## Requirements

Go 1.25+ and PostgreSQL 12+.

```sh
go get github.com/pgdesk/pgdesk@latest
```

The API may change before v1. The API reference is on [pkg.go.dev](https://pkg.go.dev/github.com/pgdesk/pgdesk). MIT licensed.
