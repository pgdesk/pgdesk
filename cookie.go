package pgdesk

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pgdesk/pgdesk/internal/csrf"
)

var errPlainHTTP = errors.New("browsers drop Secure cookies over plain HTTP; serve pgdesk over HTTPS or use WithInsecureCookies")

// newCookie returns an HttpOnly, SameSite=Lax cookie that is Secure unless
// WithInsecureCookies was given.
func (a *Admin) newCookie(name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Secure:   !a.cfg.insecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

// csrfCookieName drops the __Host- prefix when cookies are not Secure,
// because browsers reject a __Host- cookie that lacks the Secure attribute.
func (a *Admin) csrfCookieName() string {
	if a.cfg.insecureCookies {
		return csrf.InsecureCookieName
	}
	return csrf.CookieName
}

// isHTTPS reports whether r reached the server over HTTPS, directly or through
// a TLS-terminating proxy. It only chooses what an error message says, so a
// forged X-Forwarded-Proto header gains an attacker nothing.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
