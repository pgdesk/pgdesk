//go:build integration

package pgdesk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pgdesk/pgdesk"
)

func TestIntegrationClose(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithResource("it_users", nil))
	ctx := context.Background()

	if err := admin.Healthy(ctx); err != nil {
		t.Fatalf("Healthy() = %v, want nil", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if err := admin.Healthy(ctx); !errors.Is(err, pgdesk.ErrClosed) {
		t.Errorf("Healthy() after Close = %v, want ErrClosed", err)
	}
	if err := admin.Reload(ctx); !errors.Is(err, pgdesk.ErrClosed) {
		t.Errorf("Reload() after Close = %v, want ErrClosed", err)
	}
	if rec := do(admin, httptest.NewRequest("GET", "/admin/it_users", nil)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("request after Close: status = %d, want 503", rec.Code)
	}
}

func TestIntegrationMissingRowIsNotFound(t *testing.T) {
	admin, _ := setup(t)

	for _, path := range []string{"/admin/it_users/999", "/admin/it_users/999/edit"} {
		if rec := do(admin, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestIntegrationDatabaseErrors(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		closePool  bool
		wantStatus int
		wantRetry  string
		internal   string
	}{
		{
			name:       "database unavailable",
			closePool:  true,
			wantStatus: http.StatusInternalServerError,
			internal:   "closed pool",
		},
		{
			name:       "query deadline",
			timeout:    time.Nanosecond,
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  "1",
			internal:   "deadline",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []pgdesk.Option{pgdesk.WithResource("it_users", func(r *pgdesk.Resource) { r.SearchFields("email") })}
			if tt.timeout > 0 {
				opts = append(opts, pgdesk.WithQueryTimeout(tt.timeout))
			}
			admin, pool := setupWith(t, opts...)
			if tt.closePool {
				pool.Close()
			}

			for _, path := range []string{"/admin/it_users", "/admin/it_users/options.json?q=a"} {
				rec := do(admin, httptest.NewRequest("GET", path, nil))
				if rec.Code != tt.wantStatus {
					t.Errorf("GET %s: status = %d, want %d", path, rec.Code, tt.wantStatus)
				}
				if got := rec.Header().Get("Retry-After"); got != tt.wantRetry {
					t.Errorf("GET %s: Retry-After = %q, want %q", path, got, tt.wantRetry)
				}
				if strings.Contains(rec.Body.String(), tt.internal) {
					t.Errorf("GET %s: response leaks %q", path, tt.internal)
				}
			}
		})
	}
}
