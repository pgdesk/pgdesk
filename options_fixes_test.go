package pgdesk

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsSafeBasePath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"root", "/", true},
		{"simple", "/admin", true},
		{"nested", "/admin/tools", true},
		{"dashes_underscores", "/admin-tools_v2", true},
		{"empty", "", false},
		{"open_brace", "/admin/{id}", false},
		{"close_brace_only", "/admin}", false},
		{"space", "/admin tools", false},
		{"tab", "/admin\ttools", false},
		{"newline", "/admin\ntools", false},
		{"control_char", "/admin\x00tools", false},
		{"question_mark", "/admin?x=1", false},
		{"percent", "/admin%20", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSafeBasePath(tc.path)
			if got != tc.want {
				t.Errorf("isSafeBasePath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

type stubDB struct{}

func (stubDB) BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("stubDB: not implemented")
}

func (stubDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("stubDB: not implemented")
}

func (stubDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

func (stubDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("stubDB: not implemented")
}

func (stubDB) Ping(ctx context.Context) error {
	return errors.New("stubDB: not implemented")
}

func TestNewWithDBRejectsUnsafeBasePath(t *testing.T) {
	_, err := NewWithDB(stubDB{}, WithBasePath("/admin/{id}"))
	if !errors.Is(err, ErrUnsafeBasePath) {
		t.Fatalf("NewWithDB with unsafe base path: got err %v, want ErrUnsafeBasePath", err)
	}
}

func TestNewWithDBAcceptsSafeBasePath_ProceedsPastValidation(t *testing.T) {

	_, err := NewWithDB(stubDB{}, WithBasePath("/admin"))
	if err == nil {
		t.Fatal("expected an error from the non-functional stub DB, got nil")
	}
	if errors.Is(err, ErrUnsafeBasePath) {
		t.Fatalf("safe base path incorrectly rejected as unsafe: %v", err)
	}
}

var _ DB = stubDB{}

func TestLogExposureWarnsWhenNoAuthorizerAndResourcesExposed(t *testing.T) {
	var buf bytes.Buffer
	a := &Admin{cfg: defaultConfig()}
	a.cfg.logger = slog.New(slog.NewTextHandler(&buf, nil))

	st := &adminState{
		catalog:   syntheticCatalog(),
		resources: map[string]*Resource{"users": {pageSize: 10}},
		order:     []string{"users"},
	}
	a.logExposure(st, "test")

	out := buf.String()
	if !strings.Contains(out, "no Authorizer configured") {
		t.Errorf("expected a no-Authorizer WARN, got log:\n%s", out)
	}
	if !strings.Contains(out, "authorizer") || !strings.Contains(out, "deny-all") {
		t.Errorf("expected exposure log to record authorizer=none (deny-all), got:\n%s", out)
	}
}

func TestLogExposureSilentWhenNoResourcesExposed(t *testing.T) {
	var buf bytes.Buffer
	a := &Admin{cfg: defaultConfig()}
	a.cfg.logger = slog.New(slog.NewTextHandler(&buf, nil))

	st := &adminState{catalog: syntheticCatalog(), resources: map[string]*Resource{}, order: nil}
	a.logExposure(st, "test")

	out := buf.String()
	if strings.Contains(out, "no Authorizer configured") {
		t.Errorf("did not expect a no-Authorizer WARN with zero exposed resources, got log:\n%s", out)
	}
}

func TestLogExposureNoWarnWhenAuthorizerConfigured(t *testing.T) {
	var buf bytes.Buffer
	a := &Admin{cfg: defaultConfig()}
	a.cfg.logger = slog.New(slog.NewTextHandler(&buf, nil))
	a.cfg.authorizer = AuthorizerFunc(func(ctx context.Context, attrs Attributes) (Decision, error) {
		return Allow, nil
	})

	st := &adminState{
		catalog:   syntheticCatalog(),
		resources: map[string]*Resource{"users": {pageSize: 10}},
		order:     []string{"users"},
	}
	a.logExposure(st, "test")

	out := buf.String()
	if strings.Contains(out, "no Authorizer configured") {
		t.Errorf("did not expect a no-Authorizer WARN with an authorizer configured, got log:\n%s", out)
	}
	if !strings.Contains(out, "authorizer") || !strings.Contains(out, "configured") {
		t.Errorf("expected exposure log to record authorizer=configured, got:\n%s", out)
	}
}

func TestLogExposureWarnsOnOversizedPageSize(t *testing.T) {
	var buf bytes.Buffer
	a := &Admin{cfg: defaultConfig()}
	a.cfg.logger = slog.New(slog.NewTextHandler(&buf, nil))
	a.cfg.authorizer = AuthorizerFunc(func(ctx context.Context, attrs Attributes) (Decision, error) {
		return Allow, nil
	})

	st := &adminState{
		catalog:   syntheticCatalog(),
		resources: map[string]*Resource{"users": {pageSize: 5000}},
		order:     []string{"users"},
	}
	a.logExposure(st, "test")

	out := buf.String()
	if !strings.Contains(out, "PageSize exceeds") {
		t.Errorf("expected an oversized-PageSize WARN, got log:\n%s", out)
	}
}

func TestWithQueryTimeoutIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.queryTimeout
	WithQueryTimeout(0)(c)
	if c.queryTimeout != want {
		t.Errorf("queryTimeout changed on ignored value: got %v, want %v", c.queryTimeout, want)
	}
	if len(c.optionWarnings) != 1 {
		t.Fatalf("expected exactly one ignored-option note, got %d: %v", len(c.optionWarnings), c.optionWarnings)
	}
	if !strings.Contains(c.optionWarnings[0], "WithQueryTimeout") {
		t.Errorf("note does not mention WithQueryTimeout: %q", c.optionWarnings[0])
	}
}

func TestWithQueryTimeoutAcceptsPositive(t *testing.T) {
	c := defaultConfig()
	WithQueryTimeout(30 * time.Second)(c)
	if c.queryTimeout != 30*time.Second {
		t.Errorf("queryTimeout = %v, want 30s", c.queryTimeout)
	}
	if len(c.optionWarnings) != 0 {
		t.Errorf("expected no ignored-option notes, got %v", c.optionWarnings)
	}
}

func TestWithExportTimeoutIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.exportTimeout
	WithExportTimeout(-time.Second)(c)
	if c.exportTimeout != want {
		t.Errorf("exportTimeout changed on ignored value")
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithExportTimeout") {
		t.Errorf("expected a WithExportTimeout note, got %v", c.optionWarnings)
	}
}

func TestWithMaxBodyBytesIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.maxBodyBytes
	WithMaxBodyBytes(0)(c)
	if c.maxBodyBytes != want {
		t.Errorf("maxBodyBytes changed on ignored value")
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithMaxBodyBytes") {
		t.Errorf("expected a WithMaxBodyBytes note, got %v", c.optionWarnings)
	}
}

func TestWithMaxPageSizeIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.maxPageSize
	WithMaxPageSize(-1)(c)
	if c.maxPageSize != want {
		t.Errorf("maxPageSize changed on ignored value")
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithMaxPageSize") {
		t.Errorf("expected a WithMaxPageSize note, got %v", c.optionWarnings)
	}
}

func TestWithMaxExportRowsIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.maxExportRows
	WithMaxExportRows(0)(c)
	if c.maxExportRows != want {
		t.Errorf("maxExportRows changed on ignored value")
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithMaxExportRows") {
		t.Errorf("expected a WithMaxExportRows note, got %v", c.optionWarnings)
	}
}

func TestWithSchemasIgnoresEmptyCall(t *testing.T) {
	c := defaultConfig()
	want := append([]string(nil), c.schemas...)
	WithSchemas()(c)
	if strings.Join(c.schemas, ",") != strings.Join(want, ",") {
		t.Errorf("schemas changed on empty call: got %v, want %v", c.schemas, want)
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithSchemas") {
		t.Errorf("expected a WithSchemas note, got %v", c.optionWarnings)
	}
}

func TestWithMaxBulkAcceptsPositive(t *testing.T) {
	c := defaultConfig()
	WithMaxBulk(42)(c)
	if c.maxBulk != 42 {
		t.Errorf("maxBulk = %d, want 42", c.maxBulk)
	}
	if len(c.optionWarnings) != 0 {
		t.Errorf("expected no ignored-option notes, got %v", c.optionWarnings)
	}
}

func TestWithMaxBulkIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	want := c.maxBulk
	WithMaxBulk(0)(c)
	if c.maxBulk != want {
		t.Errorf("maxBulk changed on ignored value")
	}
	if len(c.optionWarnings) != 1 || !strings.Contains(c.optionWarnings[0], "WithMaxBulk") {
		t.Errorf("expected a WithMaxBulk note, got %v", c.optionWarnings)
	}
}

func TestNewWithDBEmitsIgnoredOptionWarningsAfterLoggerResolves(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	_, err := NewWithDB(stubDB{},
		WithMaxBulk(0),
		WithLogger(logger),
	)
	if err == nil {
		t.Fatal("expected an error from the non-functional stub DB, got nil")
	}

	out := buf.String()
	if !strings.Contains(out, "WithMaxBulk") {
		t.Errorf("expected the WithMaxBulk ignored-option WARN to reach the logger set later in the option chain, got:\n%s", out)
	}
}
