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
	title        string
	basePath     string
	schemas      []string
	loginURL     string
	queryTimeout time.Duration

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

	autoRegister *autoRegisterConfig // nil unless WithAutoRegister is used (D2)
}

func defaultConfig() *config {
	return &config{
		title:           "pgdesk",
		basePath:        "/admin",
		schemas:         []string{"public"},
		loginURL:        "",
		queryTimeout:    15 * time.Second,
		logger:          slog.New(slog.DiscardHandler),
		metrics:         nopMetrics{},
		authorizer:      nil, // nil → fail-closed (all capabilities denied) until set
		defaultPageSize: 50,
		maxPageSize:     200,
		maxBodyBytes:    1 << 20, // 1 MiB
		maxBulk:         500,
		maxExportRows:   50000,
	}
}

// normalizeBasePath ensures the base path starts with "/" and has no trailing
// slash (except the root). It underpins the open-redirect defense (F4).
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

// WithSecretKey sets the CSRF signing key. The first key is primary (used to
// sign); any additional keys are accepted for verification to support rotation
// (D5). A key is REQUIRED once mutations are possible or New returns an error.
func WithSecretKey(primary []byte, retired ...[]byte) Option {
	return func(c *config) {
		c.secretPrimary = append([]byte(nil), primary...)
		c.secretRetired = nil
		for _, k := range retired {
			c.secretRetired = append(c.secretRetired, append([]byte(nil), k...))
		}
	}
}

// WithLogger sets the structured logger (O5). The default discards all output so
// the library is silent unless asked.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithQueryTimeout bounds every DB call with a context deadline (O2, default 15s).
func WithQueryTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.queryTimeout = d
		}
	}
}

// WithMiddleware adds middleware wrapping the entire admin handler.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

// WithLoginURL sets where unauthenticated GET requests are redirected (O6). When
// empty, protected GET routes return 401 instead of redirecting.
func WithLoginURL(url string) Option {
	return func(c *config) { c.loginURL = url }
}

// WithAuthorizer sets the authorization policy (O6). Without one, every
// capability is denied (fail-closed). Use AllowAll to permit an
// already-gated admin.
func WithAuthorizer(az Authorizer) Option {
	return func(c *config) { c.authorizer = az }
}

// WithAuditLogger sets a best-effort, out-of-band audit sink (O4). For
// guaranteed durability use WithTxAuditLogger instead.
func WithAuditLogger(l AuditLogger) Option {
	return func(c *config) { c.audit = l }
}

// WithTxAuditLogger sets a transactional audit sink that writes inside the
// mutation's transaction, so a mutation cannot commit without its audit record
// (O4).
func WithTxAuditLogger(l TxAuditLogger) Option {
	return func(c *config) { c.txAudit = l }
}

// WithMetrics sets the metrics hook (O5). Default is a no-op.
func WithMetrics(m Metrics) Option {
	return func(c *config) {
		if m != nil {
			c.metrics = m
		}
	}
}

// WithMaxBodyBytes caps the size of any form POST body (F6, default 1 MiB).
func WithMaxBodyBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBodyBytes = n
		}
	}
}

// WithMaxPageSize sets the hard upper bound on list page size regardless of the
// ?page_size= query parameter (F6).
func WithMaxPageSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxPageSize = n
		}
	}
}

// WithMaxExportRows caps how many rows a CSV export streams, so an export can't
// force an unbounded scan (F6, default 50000).
func WithMaxExportRows(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxExportRows = n
		}
	}
}
