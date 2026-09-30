package pgdesk

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/csrf"
)

func TestCookieAttributes(t *testing.T) {
	tests := []struct {
		name     string
		opts     []func(*config)
		csrfName string
		secure   bool
	}{
		{"default", nil, "__Host-pgdesk_csrf", true},
		{"WithInsecureCookies", []func(*config){WithInsecureCookies()}, "pgdesk_csrf", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testAdmin(t, tt.opts...)
			req := httptest.NewRequest("GET", "/admin/users", nil)

			rec := httptest.NewRecorder()
			a.ensureCSRFToken(rec, req)
			a.setFlash(rec, "info", "Saved.")
			cookies := rec.Result().Cookies()
			if len(cookies) != 2 {
				t.Fatalf("got cookies %v, want a CSRF and a flash cookie", cookies)
			}

			req.AddCookie(cookies[1])
			rec = httptest.NewRecorder()
			a.takeFlash(rec, req)
			cookies = append(cookies, rec.Result().Cookies()...)

			want := []string{tt.csrfName, flashCookieName, flashCookieName}
			if len(cookies) != len(want) {
				t.Fatalf("got cookies %v, want %v", cookies, want)
			}
			for i, c := range cookies {
				if c.Name != want[i] || c.Secure != tt.secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
					t.Errorf("cookie %q, want name %s, Secure=%t, HttpOnly and SameSite=Lax", c, want[i], tt.secure)
				}
				if strings.HasPrefix(c.Name, "__Host-") && (!c.Secure || c.Path != "/" || c.Domain != "") {
					t.Errorf("cookie %q breaks the __Host- prefix rules, so browsers will reject it", c)
				}
			}
		})
	}
}

func TestInsecureCookiesCSRFRoundTrip(t *testing.T) {
	a := testAdmin(t, WithInsecureCookies())
	rec := httptest.NewRecorder()
	token := a.ensureCSRFToken(rec, httptest.NewRequest("GET", "/admin/users/1/edit", nil))

	form := url.Values{csrf.FormField: {token}}
	req := httptest.NewRequest("POST", "/admin/users/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if err := a.verifyCSRF(req); err != nil {
		t.Fatalf("verifyCSRF: %v", err)
	}
}

func TestVerifyCSRFExplainsPlainHTTP(t *testing.T) {
	tests := []struct {
		name           string
		opts           []func(*config)
		target         string
		forwardedProto string
		wantHint       bool
	}{
		{"plain HTTP", nil, "http://admin.internal/admin/users/new", "", true},
		{"HTTPS", nil, "https://admin.example.com/admin/users/new", "", false},
		{"HTTPS terminated by a proxy", nil, "http://admin.internal/admin/users/new", "https", false},
		{"WithInsecureCookies", []func(*config){WithInsecureCookies()}, "http://admin.internal/admin/users/new", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testAdmin(t, tt.opts...)
			req := httptest.NewRequest("POST", tt.target, nil)
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}

			err := a.verifyCSRF(req)
			if !errors.Is(err, http.ErrNoCookie) {
				t.Fatalf("verifyCSRF = %v, want an error wrapping http.ErrNoCookie", err)
			}
			if got := errors.Is(err, errPlainHTTP); got != tt.wantHint {
				t.Errorf("verifyCSRF = %q; explains plain HTTP: %t, want %t", err, got, tt.wantHint)
			}
		})
	}
}
