package pgdesk

import (
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"
)

// Option configures New.
type Option func(*config)

type config struct {
	title         string
	basePath      string
	schemas       []string
	loginURL      string
	logoutURL     string
	queryTimeout  time.Duration
	exportTimeout time.Duration

	resources []resourceReg

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
	maxOptions      int

	templateFS fs.FS

	autoRegister *autoRegisterConfig

	optionWarnings []string
}

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
		logger:          slog.New(discardHandler{}),
		metrics:         nopMetrics{},
		authorizer:      nil,
		defaultPageSize: 50,
		maxPageSize:     200,
		maxBodyBytes:    1 << 20,
		maxBulk:         500,
		maxExportRows:   50000,
		maxOptions:      20,
	}
}

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

// WithTitle sets the title shown in the header. Default "pgdesk".
func WithTitle(title string) Option {
	return func(c *config) { c.title = title }
}

// WithBasePath sets the URL path the admin is served under. Default "/admin".
func WithBasePath(path string) Option {
	return func(c *config) { c.basePath = normalizeBasePath(path) }
}

// WithSchemas sets the schemas tables are read from. Default "public".
func WithSchemas(schemas ...string) Option {
	return func(c *config) {
		if len(schemas) > 0 {
			c.schemas = append([]string(nil), schemas...)
			return
		}
		c.noteIgnoredOption("pgdesk: WithSchemas() called with no arguments; ignored, keeping " + strings.Join(c.schemas, ","))
	}
}

// WithResource exposes the named table or view, configured by configure (which may be nil).
// A later call for the same name replaces an earlier one.
func WithResource(name string, configure func(*Resource)) Option {
	return func(c *config) {
		c.resources = append(c.resources, resourceReg{name: name, fn: configure})
	}
}

// WithSecretKey sets the key that signs CSRF tokens and flash messages.
// Values signed with a retired key are still accepted, so keys can be rotated.
func WithSecretKey(primary []byte, retired ...[]byte) Option {
	return func(c *config) {
		c.secretPrimary = append([]byte(nil), primary...)
		c.secretRetired = nil
		for _, k := range retired {
			c.secretRetired = append(c.secretRetired, append([]byte(nil), k...))
		}
	}
}

// WithLogger sets the logger. By default nothing is logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithQueryTimeout limits each request's database work. Default 15s.
func WithQueryTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.queryTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithQueryTimeout(%s) ignored: value must be > 0, keeping %s", d, c.queryTimeout))
	}
}

// WithExportTimeout limits a CSV export. Default 5m; never less than the query timeout.
func WithExportTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.exportTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithExportTimeout(%s) ignored: value must be > 0, keeping %s", d, c.exportTimeout))
	}
}

// WithMiddleware runs mw, in order, on every admin request before routing.
// Use it to set the Principal.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

// WithLoginURL redirects unauthenticated GET requests to url instead of answering 401.
func WithLoginURL(url string) Option {
	return func(c *config) { c.loginURL = url }
}

// WithLogoutURL adds a sign-out button to the header that POSTs to url.
func WithLogoutURL(url string) Option {
	return func(c *config) { c.logoutURL = url }
}

// WithAuthorizer sets the Authorizer. If az also implements Scoper, its constraints filter rows.
func WithAuthorizer(az Authorizer) Option {
	return func(c *config) { c.authorizer = az }
}

// WithAuditLogger sets an AuditLogger.
func WithAuditLogger(l AuditLogger) Option {
	return func(c *config) { c.audit = l }
}

// WithTxAuditLogger sets a TxAuditLogger.
func WithTxAuditLogger(l TxAuditLogger) Option {
	return func(c *config) { c.txAudit = l }
}

// WithMetrics sets the Metrics receiver.
func WithMetrics(m Metrics) Option {
	return func(c *config) {
		if m != nil {
			c.metrics = m
		}
	}
}

// WithMaxBodyBytes limits the size of a form submission. Default 1 MiB.
func WithMaxBodyBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBodyBytes = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBodyBytes(%d) ignored: value must be > 0, keeping %d", n, c.maxBodyBytes))
	}
}

// WithMaxPageSize caps the page_size a list request can ask for. Default 200.
func WithMaxPageSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxPageSize = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxPageSize(%d) ignored: value must be > 0, keeping %d", n, c.maxPageSize))
	}
}

// WithMaxExportRows caps the rows in a CSV export. Default 50000.
func WithMaxExportRows(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxExportRows = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxExportRows(%d) ignored: value must be > 0, keeping %d", n, c.maxExportRows))
	}
}

// WithMaxOptions caps the choices the foreign-key picker returns per search. Default 20.
func WithMaxOptions(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxOptions = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxOptions(%d) ignored: value must be > 0, keeping %d", n, c.maxOptions))
	}
}

// WithMaxBulk caps the rows one bulk action can select. Default 500.
func WithMaxBulk(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBulk = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBulk(%d) ignored: value must be > 0, keeping %d", n, c.maxBulk))
	}
}

// WithTemplateFS replaces built-in templates with same-named *.html files from fsys.
func WithTemplateFS(fsys fs.FS) Option {
	return func(c *config) { c.templateFS = fsys }
}
