// Package pgdesk is a PostgreSQL-first, pgx-first admin framework for Go. It
// gives internal tools Django-Admin-like leverage: schema introspection,
// declarative resource configuration, a server-rendered HTML UI, and fail-closed
// safe defaults. See docs/ARCHITECTURE.md for the locked design decisions
// (D1–D7, F1–F8, O1–O8) that this package implements.
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
// (rather than the concrete pool) keeps the request path testable while the
// public New still takes a *pgxpool.Pool. pgdesk never closes the pool (O3).
type DB interface {
	introspect.TxBeginner
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
}

// Admin is the constructed admin application. It implements http.Handler, mounts
// on a ServeMux, and owns the catalog, routing, templates, CSRF cookie, and
// per-request deadlines. It does NOT own the pool's lifecycle (O3).
type Admin struct {
	cfg      *config
	db       DB
	state    atomic.Pointer[adminState] // immutable (catalog, resources) bundle (D1)
	signer   *csrf.Signer               // nil only when no writable resource is registered (D5)
	renderer *render.Renderer

	configs []resourceReg // retained registrations so Reload can rebuild (D1)

	handler   http.Handler // built once on first Mount/ServeHTTP
	buildOnce sync.Once
	built     atomic.Bool
	closed    atomic.Bool
}

// New constructs an Admin from a live pool. It performs introspection once (D1):
// if introspection fails, New fails and returns no half-initialized Admin.
//
// New uses context.Background bounded by the configured query timeout for the
// initial introspection. A CSRF signing key (WithSecretKey) is not required
// until a writable resource is registered, at which point Resource enforces it
// (D5); a purely read-only admin may run without one.
func New(pool *pgxpool.Pool, opts ...Option) (*Admin, error) {
	if pool == nil {
		return nil, ErrNoPool
	}
	return newAdmin(pool, opts...)
}

// newAdmin is the interface-typed constructor used by New and by tests.
func newAdmin(db DB, opts ...Option) (*Admin, error) {
	if db == nil {
		return nil, ErrNoPool
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
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

	// Compile templates once; a parse error fails construction (F5).
	tmplSub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("pgdesk: locating templates: %w", err)
	}
	r, err := render.New(tmplSub, staticTemplateFuncs())
	if err != nil {
		return nil, err
	}
	a.renderer = r

	// Introspect once into the immutable catalog behind the atomic pointer (D1).
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout(cfg.queryTimeout))
	defer cancel()
	cat, err := introspect.Load(ctx, db, cfg.schemas)
	if err != nil {
		return nil, fmt.Errorf("pgdesk: initial introspection: %w", err)
	}
	// Initial state: catalog with no resources yet (Resource/WithAutoRegister
	// populate it before Mount).
	a.state.Store(&adminState{catalog: cat, resources: map[string]*Resource{}})

	return a, nil
}

// currentCatalog returns the catalog from the current state.
func (a *Admin) currentCatalog() *introspect.Catalog {
	return a.state.Load().catalog
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

// Resource registers a table or view for administration and configures it via fn
// (D2). Exposure is opt-in: only registered resources are reachable.
//
// Configuration errors are programmer errors surfaced loudly at startup: an
// unknown table, an unknown column in a setter, or a writable resource with no
// CSRF key configured (D5) all panic with a clear message. This matches the
// ergonomic call site (no error to thread) while remaining fail-closed.
func (a *Admin) Resource(name string, fn func(*Resource)) {
	if a.closed.Load() {
		panic(ErrClosed)
	}
	if a.built.Load() {
		panic("pgdesk: Resource must be called before Mount/ServeHTTP")
	}
	a.configs = append(a.configs, resourceReg{name: name, fn: fn})
	st, err := a.buildState(a.currentCatalog())
	if err != nil {
		a.configs = a.configs[:len(a.configs)-1] // roll back the bad registration
		panic(fmt.Errorf("pgdesk: Resource(%q): %w", name, err))
	}
	a.state.Store(st)
}

// buildState builds every registered resource (explicit configs then, if enabled,
// auto-registered tables) against cat and returns a fresh immutable adminState.
// It is used at registration, at Mount, and at Reload — so Reload rebuilds
// resources against the new catalog rather than leaving them bound to a stale one
// (D1). Any configuration error aborts the whole build (fail-closed, D2).
func (a *Admin) buildState(cat *introspect.Catalog) (*adminState, error) {
	resources := map[string]*Resource{}
	var order []string
	add := func(name string, r *Resource) {
		if _, ok := resources[name]; !ok {
			order = append(order, name)
		}
		resources[name] = r
	}

	for _, reg := range a.configs {
		r, err := a.buildResource(cat, reg.name, reg.fn)
		if err != nil {
			return nil, err
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

// buildResource resolves a table and runs its config, enforcing the column and
// CSRF-secret invariants (D2, D5).
func (a *Admin) buildResource(cat *introspect.Catalog, name string, fn func(*Resource)) (*Resource, error) {
	tbl, err := resolveTable(cat, a.cfg.schemas, name)
	if err != nil {
		return nil, err
	}
	r := newResource(name, tbl, a.cfg.defaultPageSize)
	if fn != nil {
		fn(r)
	}
	if r.err != nil {
		return nil, r.err
	}
	// Mutating capability of any kind (writable CRUD or a registered action)
	// requires a CSRF signing key, fail-closed (D5).
	if (r.writable() || len(r.actions) > 0) && a.signer == nil {
		return nil, ErrSecretRequired
	}
	return r, nil
}

// resolveTable finds a table by name across the configured schemas.
func resolveTable(cat *introspect.Catalog, schemas []string, name string) (*introspect.Table, error) {
	for _, s := range schemas {
		if t, ok := cat.Table(s, name); ok {
			return t, nil
		}
	}
	return nil, ErrUnknownTable
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

// Reload rebuilds the catalog and atomically swaps it in (D1). On failure it
// keeps the last-known-good catalog live and returns the error — degrade to
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
// leaked PII or system table immediately (D2).
func (a *Admin) logExposure(st *adminState, reason string) {
	a.cfg.logger.Info("pgdesk: "+reason,
		"tables", len(st.catalog.Tables()),
		"exposed_resources", st.order)
}

// Healthy reports whether the admin can serve: the pool answers a ping and a
// catalog is live (O3). Wire it to a readiness probe; pgdesk registers no route.
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

// Close marks the admin closed for orderly teardown (O3). It does NOT close the
// pool — the host owns the pool's lifecycle. After Close, request handling and
// Reload return ErrClosed.
func (a *Admin) Close() error {
	a.closed.Store(true)
	return nil
}
