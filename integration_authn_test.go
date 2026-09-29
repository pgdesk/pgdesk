//go:build integration

package pgdesk_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

func TestIntegrationUnauthenticated(t *testing.T) {
	tests := []struct {
		name         string
		loginURL     string
		method, path string
		wantStatus   int
		wantLocation string
		wantJSON     bool
	}{
		{name: "page without login URL", method: "GET", path: "/admin/it_users", wantStatus: http.StatusUnauthorized},
		{name: "index without login URL", method: "GET", path: "/admin/", wantStatus: http.StatusUnauthorized},
		{name: "page redirects to login URL", loginURL: "/login", method: "GET", path: "/admin/it_users", wantStatus: http.StatusSeeOther, wantLocation: "/login"},
		{name: "POST is refused, not redirected", loginURL: "/login", method: "POST", path: "/admin/it_users/1/delete", wantStatus: http.StatusUnauthorized},
		{name: "JSON endpoint answers JSON", loginURL: "/login", method: "GET", path: "/admin/it_users/options.json", wantStatus: http.StatusUnauthorized, wantJSON: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []pgdesk.Option{pgdesk.WithResource("it_users", func(r *pgdesk.Resource) { r.SearchFields("email") })}
			if tt.loginURL != "" {
				opts = append(opts, pgdesk.WithLoginURL(tt.loginURL))
			}
			admin, _ := newAdmin(t, "", opts...)

			rec := do(admin, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("%s %s: status = %d, want %d", tt.method, tt.path, rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if got := strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"); got != tt.wantJSON {
				t.Errorf("JSON response = %t, want %t (Content-Type %q)", got, tt.wantJSON, rec.Header().Get("Content-Type"))
			}
			if strings.Contains(rec.Body.String(), "ada@example.com") {
				t.Error("response leaks row data")
			}
		})
	}
}
