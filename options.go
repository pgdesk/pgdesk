package pgdesk

import (
	"fmt"
	"io/fs"
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

	templateFS fs.FS // nil unless WithTemplateFS is used; overlays the embedded templates

	autoRegister *autoRegisterConfig // nil unless WithAutoRegister is used

	// optionWarnings accumulates notes about option values that were ignored in
	// favor of a safe default (e.g. a non-positive timeout or size). Options run
	// before WithLogger is necessarily applied, so they cannot log directly;
	// newAdmin emits these as WARN lines once every option has run and the
	// logger is resolved.
	optionWarnings []string
}

// noteIgnoredOption records a construction-time note about an option value
// that was ignored in favor of a safe default. See optionWarnings.
func (c *config) noteIgnoredOption(note string) {
	c.optionWarnings = append(c.optionWarnings, note)
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

// isSafeBasePath reports whether p is safe to register as an http.ServeMux
// route prefix. A base path legitimately contains '/' as a segment separator,
// so this extends isURLSafe's allowed character set with '/' while still
// rejecting '{', '}', whitespace, and other control/punctuation characters
// that would make mux.Handle panic under Go 1.22+ ServeMux pattern syntax.
func isSafeBasePath(p string) bool {
	if p == "" {
		return false
	}
	for _, c := range p {
		switch {
		case c == '/':
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
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
// Calling it with no arguments is ignored (a WARN is logged at construction)
// and the previous schema list is kept.
func WithSchemas(schemas ...string) Option {
	return func(c *config) {
		if len(schemas) > 0 {
			c.schemas = append([]string(nil), schemas...)
			return
		}
		c.noteIgnoredOption("pgdesk: WithSchemas() called with no arguments; ignored, keeping " + strings.Join(c.schemas, ","))
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
//
// Calling WithSecretKey more than once REPLACES the key set rather than
// merging it: the last call wins, and its retired set replaces any earlier
// one entirely.
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
// A value <= 0 is ignored (a WARN is logged at construction) and the current
// timeout is kept.
func WithQueryTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.queryTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithQueryTimeout(%s) ignored: value must be > 0, keeping %s", d, c.queryTimeout))
	}
}

// WithExportTimeout bounds a CSV export's streaming query with its own, longer
// deadline (default 5m). An export streams up to WithMaxExportRows rows and is a
// different workload from an interactive query, so it does not share the shorter
// WithQueryTimeout; a value below the query timeout is raised to it at use. A
// value <= 0 is ignored (a WARN is logged at construction) and the current
// timeout is kept.
func WithExportTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.exportTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithExportTimeout(%s) ignored: value must be > 0, keeping %s", d, c.exportTimeout))
	}
}

// WithMiddleware adds middleware wrapping the entire admin handler.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

// WithLoginURL sets where unauthenticated GET requests are redirected. When
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
// guaranteed durability use WithTxAuditLogger instead. If both a best-effort
// and a transactional audit logger are configured, both are invoked for
// every mutation.
func WithAuditLogger(l AuditLogger) Option {
	return func(c *config) { c.audit = l }
}

// WithTxAuditLogger sets a transactional audit sink that writes inside the
// mutation's transaction, so a mutation cannot commit without its audit record.
// If both a best-effort and a transactional audit logger are configured, both
// are invoked for every mutation.
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

// WithMaxBodyBytes caps the size of any form POST body (default 1 MiB). A
// value <= 0 is ignored (a WARN is logged at construction) and the current
// limit is kept.
func WithMaxBodyBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBodyBytes = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBodyBytes(%d) ignored: value must be > 0, keeping %d", n, c.maxBodyBytes))
	}
}

// WithMaxPageSize sets the hard upper bound on list page size regardless of the
// ?page_size= query parameter. A value <= 0 is ignored (a WARN is logged at
// construction) and the current limit is kept.
func WithMaxPageSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxPageSize = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxPageSize(%d) ignored: value must be > 0, keeping %d", n, c.maxPageSize))
	}
}

// WithMaxExportRows caps how many rows a CSV export streams, so an export can't
// force an unbounded scan (default 50000). A value <= 0 is ignored (a WARN is
// logged at construction) and the current limit is kept.
func WithMaxExportRows(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxExportRows = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxExportRows(%d) ignored: value must be > 0, keeping %d", n, c.maxExportRows))
	}
}

// WithMaxBulk caps how many rows a single bulk action may operate on (default
// 500). A value <= 0 is ignored (a WARN is logged at construction) and the
// current limit is kept.
func WithMaxBulk(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBulk = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBulk(%d) ignored: value must be > 0, keeping %d", n, c.maxBulk))
	}
}

// WithTemplateFS overlays custom templates over the built-in set. Any template
// file present in fsys (matched by the same name as the embedded template,
// e.g. "list.html") replaces the built-in one; templates absent from fsys fall
// back to the embedded default. Use it to rebrand or restructure the admin UI
// without forking. A malformed override template fails New(), never at
// request time.
func WithTemplateFS(fsys fs.FS) Option {
	return func(c *config) { c.templateFS = fsys }
}
