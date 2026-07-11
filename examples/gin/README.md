# pgdesk behind Gin

The pgdesk admin mounted inside a [Gin](https://github.com/gin-gonic/gin) router.
pgdesk is a plain `net/http.Handler`, so it drops under any router with no
adapter -- `gin.WrapH(admin)` and a catch-all route is the whole integration.

The one part worth studying is **authentication**: a Gin middleware attaches a
`pgdesk.Principal` to the request context, and pgdesk reads it for every
authorization decision. Replace the demo middleware with your real auth and the
admin is wired.

```go
r := gin.New()

// Your Gin auth middleware supplies the pgdesk Principal.
r.Use(func(c *gin.Context) {
    c.Request = c.Request.WithContext(
        pgdesk.WithPrincipal(c.Request.Context(), currentUser(c)),
    )
    c.Next()
})

// Redirect the bare base path, then serve the subtree. (admin.Mount does this
// for you on a net/http mux; under Gin you add the one redirect yourself.)
r.GET("/admin", func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/admin/") })
r.Any("/admin/*any", gin.WrapH(admin))
```

This example is a **separate Go module** on purpose: Gin and its dependencies
stay out of pgdesk's own `go.mod`, which keeps pgdesk's runtime dependency tree
down to a single entry (`pgx`).

## Run it

```sh
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16

DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
  go run .
```

Then open <http://localhost:8080/admin/>.
