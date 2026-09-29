//go:build integration

package pgdesk_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestIntegrationListFilters(t *testing.T) {
	// setup filters on status and created_at; the schema seeds ada (active)
	// and alan (pending).
	admin, _ := setup(t)
	const ada, alan = "ada@example.com", "alan@example.com"

	tests := []struct {
		query string
		want  []string
	}{
		{"f_status=active", []string{ada}},
		{"f_status__ne=active", []string{alan}},
		{"f_status__in=active,pending", []string{ada, alan}},
		{"f_status__in=suspended", nil},
		{"f_status__isnull=false", []string{ada, alan}},
		{"f_status__isnull=true", nil},
		{"f_created_at__gt=2000-01-01", []string{ada, alan}},
		{"f_created_at__lt=2000-01-01", nil},
		{"f_created_at__between=2000-01-01,2100-01-01", []string{ada, alan}},
		{"f_created_at__between=2100-01-01,2200-01-01", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?"+tt.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			for _, email := range []string{ada, alan} {
				got := strings.Contains(rec.Body.String(), email)
				if want := slices.Contains(tt.want, email); got != want {
					t.Errorf("lists %s = %t, want %t", email, got, want)
				}
			}
		})
	}
}

func TestIntegrationListFiltersRejectBadInput(t *testing.T) {
	admin, _ := setup(t)

	for _, query := range []string{
		"f_status=bogus",
		"f_status__in=active,bogus",
		"f_status__in=,",
		"f_status__lt=active",
		"f_status__isnull=maybe",
		"f_created_at__gt=yesterday",
		"f_created_at__between=2000-01-01",
		"f_created_at__between=2000-01-01,soon",
		"f_email=ada@example.com",
	} {
		t.Run(query, func(t *testing.T) {
			rec := do(admin, httptest.NewRequest("GET", "/admin/it_users?"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}
