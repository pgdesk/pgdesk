//go:build integration

package pgdesk_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

// csrfRoutes are the routes that change data. Each form is valid apart from its CSRF token.
var csrfRoutes = []struct {
	name string
	path string
	form func(version string) url.Values
}{
	{"create", "/admin/it_users/new", func(string) url.Values {
		return url.Values{"email": {"mallory@example.com"}, "status": {"active"}}
	}},
	{"update", "/admin/it_users/1/edit", func(version string) url.Values {
		return url.Values{"_version": {version}, "email": {"mallory@example.com"}, "status": {"active"}}
	}},
	{"delete", "/admin/it_users/" + leavingID + "/delete", func(string) url.Values {
		return url.Values{}
	}},
	{"bulk action", "/admin/it_users/action", func(string) url.Values {
		return url.Values{"_action": {"activate"}, "key": {"2"}}
	}},
}

// badTokens pick the cookie and form field a request sends, given two tokens
// the admin issued. An empty string sends nothing.
var badTokens = []struct {
	name string
	pick func(valid, other string) (cookie, field string)
}{
	{"no token", func(string, string) (string, string) { return "", "" }},
	{"form field only", func(valid, _ string) (string, string) { return "", valid }},
	{"cookie only", func(valid, _ string) (string, string) { return valid, "" }},
	{"cookie and form field differ", func(valid, other string) (string, string) { return valid, other }},
	{"unsigned token", func(string, string) (string, string) { return "forged", "forged" }},
}

// postWithCSRF posts form to path, sending cookie and field as the CSRF cookie
// and form field. An empty value is left out.
func postWithCSRF(admin *pgdesk.Admin, path string, form url.Values, cookie, field string) *httptest.ResponseRecorder {
	if field != "" {
		form.Set("_pgdesk_csrf", field)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: cookie})
	}
	return do(admin, req)
}

func TestIntegrationStateChangingRoutesRequireCSRF(t *testing.T) {
	for _, route := range csrfRoutes {
		t.Run(route.name, func(t *testing.T) {
			// Without this, a route that fails for another reason would pass every case below.
			t.Run("valid token", func(t *testing.T) {
				admin, pool := usersAdmin(t)
				token, version := editToken(t, admin, "1")
				before := usersSnapshot(t, pool)

				rec := postWithCSRF(admin, route.path, route.form(version), token, token)

				if rec.Code != http.StatusSeeOther {
					t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body)
				}
				if usersSnapshot(t, pool) == before {
					t.Error("a valid request left it_users unchanged")
				}
			})

			for _, bad := range badTokens {
				t.Run(bad.name, func(t *testing.T) {
					admin, pool := usersAdmin(t)
					valid, version := editToken(t, admin, "1")
					other, _ := editToken(t, admin, "1")
					if valid == other {
						t.Fatal("two forms were issued the same token")
					}
					cookie, field := bad.pick(valid, other)
					before := usersSnapshot(t, pool)

					rec := postWithCSRF(admin, route.path, route.form(version), cookie, field)

					if rec.Code != http.StatusForbidden {
						t.Errorf("status = %d, want 403", rec.Code)
					}
					if usersSnapshot(t, pool) != before {
						t.Error("the request changed it_users")
					}
				})
			}
		})
	}
}
