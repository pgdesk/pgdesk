package pgdesk

import (
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"
)

type Option func(*config)

type config struct {
	title         string
	basePath      string
	schemas       []string
	loginURL      string
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
		logger:          slog.New(slog.DiscardHandler),
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

func WithTitle(title string) Option {
	return func(c *config) { c.title = title }
}

func WithBasePath(path string) Option {
	return func(c *config) { c.basePath = normalizeBasePath(path) }
}

func WithSchemas(schemas ...string) Option {
	return func(c *config) {
		if len(schemas) > 0 {
			c.schemas = append([]string(nil), schemas...)
			return
		}
		c.noteIgnoredOption("pgdesk: WithSchemas() called with no arguments; ignored, keeping " + strings.Join(c.schemas, ","))
	}
}

func WithResource(name string, configure func(*Resource)) Option {
	return func(c *config) {
		c.resources = append(c.resources, resourceReg{name: name, fn: configure})
	}
}

func WithSecretKey(primary []byte, retired ...[]byte) Option {
	return func(c *config) {
		c.secretPrimary = append([]byte(nil), primary...)
		c.secretRetired = nil
		for _, k := range retired {
			c.secretRetired = append(c.secretRetired, append([]byte(nil), k...))
		}
	}
}

func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

func WithQueryTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.queryTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithQueryTimeout(%s) ignored: value must be > 0, keeping %s", d, c.queryTimeout))
	}
}

func WithExportTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.exportTimeout = d
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithExportTimeout(%s) ignored: value must be > 0, keeping %s", d, c.exportTimeout))
	}
}

func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

func WithLoginURL(url string) Option {
	return func(c *config) { c.loginURL = url }
}

func WithAuthorizer(az Authorizer) Option {
	return func(c *config) { c.authorizer = az }
}

func WithAuditLogger(l AuditLogger) Option {
	return func(c *config) { c.audit = l }
}

func WithTxAuditLogger(l TxAuditLogger) Option {
	return func(c *config) { c.txAudit = l }
}

func WithMetrics(m Metrics) Option {
	return func(c *config) {
		if m != nil {
			c.metrics = m
		}
	}
}

func WithMaxBodyBytes(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBodyBytes = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBodyBytes(%d) ignored: value must be > 0, keeping %d", n, c.maxBodyBytes))
	}
}

func WithMaxPageSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxPageSize = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxPageSize(%d) ignored: value must be > 0, keeping %d", n, c.maxPageSize))
	}
}

func WithMaxExportRows(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxExportRows = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxExportRows(%d) ignored: value must be > 0, keeping %d", n, c.maxExportRows))
	}
}

func WithMaxOptions(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxOptions = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxOptions(%d) ignored: value must be > 0, keeping %d", n, c.maxOptions))
	}
}

func WithMaxBulk(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxBulk = n
			return
		}
		c.noteIgnoredOption(fmt.Sprintf("pgdesk: WithMaxBulk(%d) ignored: value must be > 0, keeping %d", n, c.maxBulk))
	}
}

func WithTemplateFS(fsys fs.FS) Option {
	return func(c *config) { c.templateFS = fsys }
}
