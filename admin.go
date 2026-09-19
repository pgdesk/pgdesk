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

type DB interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
}

type Admin struct {
	cfg      *config
	db       DB
	state    atomic.Pointer[adminState]
	signer   *csrf.Signer
	renderer *render.Renderer

	handler   http.Handler
	buildOnce sync.Once
	closed    atomic.Bool
}

func New(pool *pgxpool.Pool, opts ...Option) (*Admin, error) {
	if pool == nil {
		return nil, ErrNoPool
	}
	return newAdmin(pool, opts...)
}

func NewWithDB(db DB, opts ...Option) (*Admin, error) {
	if db == nil {
		return nil, ErrNoPool
	}
	return newAdmin(db, opts...)
}

func newAdmin(db DB, opts ...Option) (*Admin, error) {
	if db == nil {
		return nil, ErrNoPool
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	if !isSafeBasePath(cfg.basePath) {
		return nil, ErrUnsafeBasePath
	}

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

	tmplSub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("pgdesk: locating templates: %w", err)
	}
	r, err := render.New(tmplSub, staticTemplateFuncs(), cfg.templateFS)
	if err != nil {
		return nil, err
	}
	a.renderer = r

	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout(cfg.queryTimeout))
	defer cancel()
	cat, err := introspect.Load(ctx, db, cfg.schemas)
	if err != nil {
		return nil, fmt.Errorf("pgdesk: initial introspection: %w", err)
	}

	st, err := a.buildState(cat)
	if err != nil {
		return nil, fmt.Errorf("pgdesk: configuring resources: %w", err)
	}
	a.state.Store(st)
	a.logExposure(st, "admin initialized")

	return a, nil
}

func staticTemplateFuncs() template.FuncMap {
	return render.StaticFuncs()
}

func startupTimeout(query time.Duration) time.Duration {
	if query < 30*time.Second {
		return 30 * time.Second
	}
	return query
}

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

func (a *Admin) buildResource(cat *introspect.Catalog, name string, fn func(*Resource)) (*Resource, error) {
	tbl, err := resolveTable(cat, a.cfg.schemas, name)
	if err != nil {
		return nil, err
	}
	return a.buildResourceFrom(tbl, name, fn)
}

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

	if (r.writable() || len(r.actions) > 0) && a.signer == nil {
		return nil, ErrSecretRequired
	}
	return r, nil
}

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

func (a *Admin) Mount(mux *http.ServeMux) {
	h := a.buildHandler()

	prefix := a.cfg.basePath
	mux.Handle(prefix+"/", h)

	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
	})
}

func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.buildHandler().ServeHTTP(w, r)
}

func (a *Admin) Reload(ctx context.Context) error {
	if a.closed.Load() {
		return ErrClosed
	}
	cat, err := introspect.Load(ctx, a.db, a.cfg.schemas)
	if err != nil {
		return fmt.Errorf("pgdesk: reload introspection (keeping previous state): %w", err)
	}

	st, err := a.buildState(cat)
	if err != nil {
		return fmt.Errorf("pgdesk: reload rebuild (keeping previous state): %w", err)
	}
	a.state.Store(st)
	a.logExposure(st, "catalog reloaded")
	return nil
}

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

func (a *Admin) Close() error {
	a.closed.Store(true)
	return nil
}
