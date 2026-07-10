package pgdesk

import (
	"log/slog"
	"strings"
	"time"
)

// Option configures an Admin at construction. Options follow the functional
// options pattern; unknown or zero values fall back to safe defaults.
type Option func(*config)

// config is the resolved, unexported configuration. Defaults are set by
// defaultConfig and overridden by Options.
type config struct {
	title         string
	basePath      string
	schemas       []string
	loginURL      string
	queryTimeout  time.Duration
	exportTimeout time.Duration

	resources []resourceReg // resources declared via WithResource, applied by New

	secretPrimary []byte
	secretRetired [][]byte

	logger     *slog.Logger
	metrics    Metrics
	authorizer Authorizer
	audit      AuditLogger
	txAudit    TxAuditLogger
	middleware []Middleware

	defaultPageSize int
	maxPageSize     int
	maxBodyBytes    int64
	maxBulk         int
	maxExportRows   int

	autoRegister *autoRegisterConfig // nil unless WithAutoRegister is used
}

func defaultConfig() *config {
	return &config{
		title:           "pgdesk",
		basePath:        "/admin",
		schemas:         []string{"public"},
		loginURL:        "",
		queryTimeout:    15 * time.Second,
		exportTimeout:   5 * time.Minute,
		logger:          slog.New(slog.DiscardHandler),
		metrics:         nopMetrics{},
		authorizer:      nil, // nil -> fail-closed (all capabilities denied) until set
		defaultPageSize: 50,
		maxPageSize:     200,
		maxBodyBytes:    1 << 20, // 1 MiB
		maxBulk:         500,
		maxExportRows:   50000,
	}
}

// normalizeBasePath ensures the base path starts with "/" and has no trailing
// slash (except the root). It underpins the open-redirect defense.
func normalizeBasePath(p string) string {
	if p == "" {
		return "/admin"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// WithTitle sets the admin site title shown in the UI header.
func WithTitle(title string) Option {
	return func(c *config) { c.title = title }
}

// WithBasePath sets the URL prefix the admin mounts under (default "/admin").
func WithBasePath(path string) Option {
	return func(c *config) { c.basePath = normalizeBasePath(path) }
}

// WithSchemas sets the PostgreSQL schemas to introspect (default "public").
func WithSchemas(schemas ...string) Option {
	return func(c *config) {
		if len(schemas) > 0 {
			c.schemas = append([]string(nil), schemas...)
		}
	}
}

// WithResource declares an admin resource for the named table or view.
// Exposure is opt-in: only declared resources are reachable. configure runs
// against the introspected table, so setters like ListDisplay and Filters are
// validated against the real columns; an unknown table or column makes New
// return an error rather than panicking. configure may be nil to expose the
// table with introspected defaults.
//
//	admin, err := pgdesk.New(pool,
//	    pgdesk.WithSecretKey(secret),
//	    pgdesk.WithResource("users", func(r *pgdesk.Resource) {
//	        r.ListDisplay("id", "email", "status")
//	        r.Filters("status")
//	    }),
//	)
func WithResource(name string, configure func(*Resource)) Option {
	return func(c *config) {
		c.resources = append(c.resources, resourceReg{name: name, fn: configure})
	}
}

// WithSecretKey sets the CSRF signing key. The first key is primary (used to
// sign); any additional keys are accepted for verification to support rotation.
// A key is REQUIRED once mutations are possible or New returns an error.
func WithSecretKey(primary []byte, retired ...[]byte) Option {
	return func(c *config) {
		c.secretPrimary = append([]byte(nil), primary...)
		c.secretRetired = nil
		for _, k := range retired {
			c.secretRetired = append(c.secretRetired, append([]byte(nil), k...))
		}
	}
}

// WithLogger sets the structured logger. The default discards all output so
// the library is silent unless asked.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithQueryTimeout bounds every DB call with a context deadline (default 15s).
func WithQueryTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.queryTimeout = d
		}
	}
}

// WithExportTimeout bounds a CSV export's streaming query with its own, longer
// deadline (default 5m). An export streams up to WithMaxExportRows rows and is a
// different workload from an interactive query, so it does not share the shorter
// WithQueryTimeout; a value below the query timeout is raised to it at use.
func WithExportTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.exportTimeout = d
		}
	}
}

// WithMiddleware adds middleware wrapping the entire admin handler.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

// WithLoginURL sets where unauthenticated GET requests are redirected . When
// empty, protected GET routes return 401 instead of redirecting.
func WithLoginURL(url string) Option {
	return func(c *config) { c.loginURL = url }
}

// WithAuthorizer sets the authorization policy. Without one, every
// capability is denied (fail-closed). Use AllowAll to permit an
// already-gated admin.
func WithAuthorizer(az Authorizer) Option {
	return func(c *config) { c.authorizer = az }
}

// WithAuditLogger sets a best-effort, out-of-band audit sink. For
// guaranteed durability use WithTxAuditLogger instead.
func WithAuditLogger(l AuditLogger) Option {
	return func(c *config) { c.audit = l }
}

// WithTxAuditLogger sets a transactional audit sink that writes inside the
// mutation's transaction, so a mutation cannot commit without its audit record.
func WithTxAuditLogger(l TxAuditLogger) Option {
	return func(c *config) { c.txAudit = l }
}

// WithMetrics sets the metrics hook. Default is a no-op.
func WithMetrics(m Metrics) Option {
	return func(c *config) {
		if m != nil {
			c.metrics = m
		}
	}
}

// WithMaxBodyBytes caps the size of any form POST body (default 1 MiB).
func WithMaxBodyBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBodyBytes = n
		}
	}
}

// WithMaxPageSize sets the hard upper bound on list page size regardless of the
// ?page_size= query parameter.
func WithMaxPageSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxPageSize = n
		}
	}
}

// WithMaxExportRows caps how many rows a CSV export streams, so an export can't
// force an unbounded scan (default 50000).
func WithMaxExportRows(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxExportRows = n
		}
	}
}
