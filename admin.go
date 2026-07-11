// Package pgdesk is a PostgreSQL-first, pgx-first admin framework for Go. It
// gives internal tools Django-Admin-like leverage: schema introspection,
// declarative resource configuration, a server-rendered HTML UI, and fail-closed
// safe defaults. See docs/ARCHITECTURE.md for the design-decision log that
// documents the reasoning behind all major implementation choices.
package pgdesk

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk/internal/csrf"
	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/render"
)

// DB is the subset of *pgxpool.Pool that pgdesk uses. Accepting the interface
// (rather than the concrete pool) keeps pgdesk usable with a wrapped or
// instrumented pool and testable with a double; New takes a *pgxpool.Pool for the
// common case, NewWithDB takes any implementation. pgdesk never closes the connection.
//
// The method set is spelled out in full (rather than embedding an unexported
// helper interface) so it documents itself in go doc.
type DB interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
}

// Admin is the constructed admin application. It implements http.Handler, mounts
// on a ServeMux, and owns the catalog, routing, templates, CSRF cookie, and
// per-request deadlines. It does NOT own the pool's lifecycle.
type Admin struct {
	cfg      *config
	db       DB
	state    atomic.Pointer[adminState] // immutable (catalog, resources) bundle
	signer   *csrf.Signer               // nil only when no writable resource is registered
	renderer *render.Renderer

	handler   http.Handler // built once on first Mount/ServeHTTP
	buildOnce sync.Once
	closed    atomic.Bool
}

// New constructs an Admin from a live pool. It introspects the schema once
// and builds every resource declared via WithResource / WithAutoRegister against
// that catalog. Any failure -- introspection, template parsing, or a resource
// referencing an unknown table or column -- returns an error and no
// half-initialized Admin; New never panics on configuration.
//
// New uses context.Background bounded by the configured query timeout for the
// initial introspection. A CSRF signing key (WithSecretKey) is required only if a
// mutating resource is declared; a purely read-only admin may run without one.
func New(pool *pgxpool.Pool, opts ...Option) (*Admin, error) {
	if pool == nil {
		return nil, ErrNoPool
	}
	return newAdmin(pool, opts...)
}

// NewWithDB constructs an Admin from any DB implementation instead of a concrete
// *pgxpool.Pool. Use it to run pgdesk against an instrumented or wrapped pool, or
// to drive it with a test double. It behaves exactly like New otherwise (same
// introspection, same fail-closed configuration, never panics). pgdesk does not
// own the connection's lifecycle.
func NewWithDB(db DB, opts ...Option) (*Admin, error) {
	if db == nil {
		return nil, ErrNoPool
	}
	return newAdmin(db, opts...)
}

// newAdmin is the interface-typed constructor used by New, NewWithDB, and tests.
func newAdmin(db DB, opts ...Option) (*Admin, error) {
	if db == nil {
		return nil, ErrNoPool
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	// Validate the normalized base path before it can reach mux.Handle, which
	// would otherwise panic on a value containing '{', '}', whitespace, or
	// control characters (Go 1.22+ ServeMux pattern syntax). New never panics
	// on configuration.
	if !isSafeBasePath(cfg.basePath) {
		return nil, ErrUnsafeBasePath
	}

	// Every option has now run (including WithLogger), so it's safe to emit
	// notes about option values that were ignored in favor of a safe default.
	for _, note := range cfg.optionWarnings {
		cfg.logger.Warn(note)
	}

	a := &Admin{
		cfg: cfg,
		db:  db,
	}

	if len(cfg.secretPrimary) > 0 {
		s, err := csrf.NewSigner(cfg.secretPrimary, cfg.secretRetired...)
		if err != nil {
			return nil, fmt.Errorf("pgdesk: building CSRF signer: %w", err)
		}
		a.signer = s
	}

	// Compile templates once; a parse error fails construction. When
	// WithTemplateFS is set, cfg.templateFS is parsed on top of the embedded
	// set so same-named templates override the built-in default; it is nil
	// (no-op) otherwise.
	tmplSub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("pgdesk: locating templates: %w", err)
	}
	r, err := render.New(tmplSub, staticTemplateFuncs(), cfg.templateFS)
	if err != nil {
		return nil, err
	}
	a.renderer = r

	// Introspect once into the immutable catalog behind the atomic pointer.
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout(cfg.queryTimeout))
	defer cancel()
	cat, err := introspect.Load(ctx, db, cfg.schemas)
	if err != nil {
		return nil, fmt.Errorf("pgdesk: initial introspection: %w", err)
	}

	// Build the resource set against the catalog. Resources are declared as
	// WithResource / WithAutoRegister options, so any misconfiguration (unknown
	// table or column, or a mutating resource with no CSRF key) is returned here
	// as an error -- New never panics on configuration.
	st, err := a.buildState(cat)
	if err != nil {
		return nil, fmt.Errorf("pgdesk: configuring resources: %w", err)
	}
	a.state.Store(st)
	a.logExposure(st, "admin initialized")

	return a, nil
}

// staticTemplateFuncs merges render's pure helpers with a compile-time assertion
// that the CSRF form field name stays in sync between packages.
func staticTemplateFuncs() template.FuncMap {
	return render.StaticFuncs()
}

func startupTimeout(query time.Duration) time.Duration {
	if query < 30*time.Second {
		return 30 * time.Second
	}
	return query
}

// buildState builds every declared resource (WithResource options first, then,
// if enabled, auto-registered tables) against cat and returns a fresh immutable
// adminState. It runs at construction and at Reload -- so Reload rebuilds
// resources against the new catalog rather than leaving them bound to a stale one.
// Any configuration error aborts the whole build (fail-closed).
func (a *Admin) buildState(cat *introspect.Catalog) (*adminState, error) {
	resources := map[string]*Resource{}
	var order []string
	add := func(name string, r *Resource) {
		if _, ok := resources[name]; !ok {
			order = append(order, name)
		}
		resources[name] = r
	}

	for _, reg := range a.cfg.resources {
		r, err := a.buildResource(cat, reg.name, reg.fn)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", reg.name, err)
		}
		add(reg.name, r)
	}

	if a.cfg.autoRegister != nil {
		if err := a.autoRegister(cat, add, resources); err != nil {
			return nil, err
		}
	}

	return &adminState{catalog: cat, resources: resources, order: order}, nil
}

// buildResource resolves a table by name and runs its config.
func (a *Admin) buildResource(cat *introspect.Catalog, name string, fn func(*Resource)) (*Resource, error) {
	tbl, err := resolveTable(cat, a.cfg.schemas, name)
	if err != nil {
		return nil, err
	}
	return a.buildResourceFrom(tbl, name, fn)
}

// buildResourceFrom runs a resource's config against an already-resolved table,
// enforcing the name, column, and CSRF-secret invariants (D2, D5).
//
// autoRegister calls this directly. It already holds the table it discovered and
// must not re-resolve the bare name, which would bind the resource to whichever
// schema comes first in the configured list rather than to the discovered table.
func (a *Admin) buildResourceFrom(tbl *introspect.Table, name string, fn func(*Resource)) (*Resource, error) {
	if !isURLSafe(name) {
		return nil, fmt.Errorf("%w: %q", ErrUnsafeName, name)
	}
	r := newResource(name, tbl, a.cfg.defaultPageSize)
	if fn != nil {
		fn(r)
	}
	if r.err != nil {
		return nil, r.err
	}
	// Mutating capability of any kind (writable CRUD or a registered action)
	// requires a CSRF signing key, fail-closed.
	if (r.writable() || len(r.actions) > 0) && a.signer == nil {
		return nil, ErrSecretRequired
	}
	return r, nil
}

// resolveTable finds a table by name across the configured schemas.
//
// A name matching a table in more than one schema is ambiguous and fails the
// build. Returning the first match would silently bind the resource -- and
// every policy written against its name -- to whichever schema happened to be
// listed first, leaving the other table permanently unreachable.
func resolveTable(cat *introspect.Catalog, schemas []string, name string) (*introspect.Table, error) {
	var found *introspect.Table
	for _, s := range schemas {
		t, ok := cat.Table(s, name)
		if !ok {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: %q exists in both %q and %q",
				ErrAmbiguousTable, name, found.Schema, t.Schema)
		}
		found = t
	}
	if found == nil {
		return nil, ErrUnknownTable
	}
	return found, nil
}

// Mount registers the admin's routes on the given ServeMux under the configured
// base path. It builds the handler once; subsequent calls reuse it.
func (a *Admin) Mount(mux *http.ServeMux) {
	h := a.buildHandler()
	// Strip-prefix pattern: everything under basePath/ is handled by pgdesk.
	prefix := a.cfg.basePath
	mux.Handle(prefix+"/", h)
	// Redirect the bare base path to the trailing-slash root.
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
	})
}

// ServeHTTP lets Admin be used directly as an http.Handler without a ServeMux.
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.buildHandler().ServeHTTP(w, r)
}

// Reload rebuilds the catalog and atomically swaps it in. On failure it
// keeps the last-known-good catalog live and returns the error -- degrade to
// stale, never to broken. Wire it to SIGHUP or an authenticated route; it is not
// a magic built-in endpoint.
func (a *Admin) Reload(ctx context.Context) error {
	if a.closed.Load() {
		return ErrClosed
	}
	cat, err := introspect.Load(ctx, a.db, a.cfg.schemas)
	if err != nil {
		return fmt.Errorf("pgdesk: reload introspection (keeping previous state): %w", err)
	}
	// Rebuild resources against the new catalog. If a config no longer resolves
	// (e.g. a column was dropped), keep the previous state live and report.
	st, err := a.buildState(cat)
	if err != nil {
		return fmt.Errorf("pgdesk: reload rebuild (keeping previous state): %w", err)
	}
	a.state.Store(st)
	a.logExposure(st, "catalog reloaded")
	return nil
}

// logExposure logs the full set of exposed resources so an operator can spot a
// leaked PII or system table immediately. It also flags a construction
// state that looks like a mistake: no Authorizer configured while resources
// are exposed (fail-closed, but silently so) and any resource whose PageSize
// exceeds the admin's maxPageSize (harmless -- F6 clamps at request time --
// but worth a nudge).
func (a *Admin) logExposure(st *adminState, reason string) {
	authorizerState := "configured"
	if a.cfg.authorizer == nil {
		authorizerState = "none (deny-all)"
	}
	a.cfg.logger.Info("pgdesk: "+reason,
		"tables", len(st.catalog.Tables()),
		"exposed_resources", st.order,
		"authorizer", authorizerState,
	)

	if a.cfg.authorizer == nil && len(st.order) > 0 {
		a.cfg.logger.Warn("pgdesk: no Authorizer configured -- every capability is denied until WithAuthorizer is set (use pgdesk.AllowAll if the admin is gated by host middleware)")
	}

	a.warnOversizedPageSizes(st)
}

// warnOversizedPageSizes logs a WARN for every resource whose configured
// PageSize exceeds the admin's maxPageSize. The oversized value is harmless
// (list requests are still clamped to maxPageSize at request time, F6), but
// it usually means WithMaxPageSize and Resource.PageSize were set
// inconsistently.
func (a *Admin) warnOversizedPageSizes(st *adminState) {
	for _, name := range st.order {
		r := st.resources[name]
		if r.pageSize > a.cfg.maxPageSize {
			a.cfg.logger.Warn("pgdesk: resource PageSize exceeds WithMaxPageSize; requests are clamped to the max",
				"resource", name,
				"page_size", r.pageSize,
				"max_page_size", a.cfg.maxPageSize,
			)
		}
	}
}

// / Healthy reports whether the admin can serve: the pool answers a ping and a
// catalog is live. Wire it to a readiness probe; pgdesk registers no route.
func (a *Admin) Healthy(ctx context.Context) error {
	if a.closed.Load() {
		return ErrClosed
	}
	if a.state.Load() == nil {
		return fmt.Errorf("pgdesk: no catalog loaded")
	}
	ctx, cancel := context.WithTimeout(ctx, a.cfg.queryTimeout)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		return fmt.Errorf("pgdesk: pool ping failed: %w", err)
	}
	return nil
}

// Close marks the admin closed for orderly teardown. It does NOT close the
// pool -- the host owns the pool's lifecycle. After Close, request handling and
// Reload return ErrClosed. Close currently always returns nil; the error return
// is reserved for future teardown steps.
func (a *Admin) Close() error {
	a.closed.Store(true)
	return nil
}
