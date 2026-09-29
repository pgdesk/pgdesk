# Contributing

Bug fixes, tests and small improvements are welcome as pull requests. For
anything bigger, open an issue first so we can agree on the shape before you
spend time on it.

Please report security issues [privately](https://github.com/pgdesk/pgdesk/security/advisories/new),
not in a public issue.

## Running the tests

You need Go 1.25 or later and Docker.

The unit tests need nothing else:

```sh
go test ./...
```

Most of pgdesk's behavior is tested against a real PostgreSQL. Start one and
point the integration tests at it:

```sh
docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=pgdesk postgres:16
PGDESK_TEST_DSN=postgres://postgres:pgdesk@localhost:5432/postgres?sslmode=disable \
    go test -tags=integration -race ./...
```

The tests drop and recreate their tables, so use a throwaway database, not
one you care about. If port 5432 is taken, use `-p 55432:5432` and change the
port in the DSN.

Without `-tags=integration` the integration tests aren't compiled, and without
`PGDESK_TEST_DSN` they're skipped. Either way `go test` passes without testing
much, so check that the output takes a few seconds, not a few milliseconds.

If you change a template and the rendered HTML changes, the golden tests fail.
Look at the diff, and if it's what you meant, regenerate them:

```sh
go test -run TestGolden -update .
```

`examples/gin` and `examples/gorm` are separate modules. Run `go build ./...`
inside them if you touch the public API.

## Before you open a pull request

CI runs these, so running them first saves a round trip:

```sh
gofmt -l .          # should print nothing
go vet ./...
staticcheck ./...   # go install honnef.co/go/tools/cmd/staticcheck@latest
```

Add a test for what you fixed or added. A bug fix should come with a test that
fails without it.

Commit messages are a single line in the
[Conventional Commits](https://www.conventionalcommits.org) style, for example
`fix: keep the page size when sorting`. Mark breaking changes with `!`, as in
`feat!: ...`.
