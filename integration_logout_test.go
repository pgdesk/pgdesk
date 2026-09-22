//go:build integration

package pgdesk_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

func TestIntegrationLogoutURLRendersSignOutForm(t *testing.T) {
	_, pool := setup(t)
	admin, err := pgdesk.New(pool,
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithLogoutURL("/auth/logout"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	body := do(admin, httptest.NewRequest("GET", "/admin/", nil)).Body.String()
	if !strings.Contains(body, `<form method="post" action="/auth/logout" class="pg-logout">`) {
		t.Fatalf("no sign-out form in header:\n%s", body)
	}
}

func TestIntegrationNoLogoutURLRendersNoForm(t *testing.T) {
	admin, _ := setup(t)
	if body := do(admin, httptest.NewRequest("GET", "/admin/", nil)).Body.String(); strings.Contains(body, "Sign out") {
		t.Fatal("sign-out shown without WithLogoutURL")
	}
}
