package pgdesk

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/pgdesk/pgdesk/internal/csrf"
	"github.com/pgdesk/pgdesk/internal/render"
)

// staticPrefix is the base-path-relative prefix under which embedded assets are
// served (F8). It is dispatched by literal prefix, not a mux wildcard.
const staticPrefix = "/_static/"

// buildHandler assembles the admin's http.Handler exactly once and caches it.
// The chain, outermost first:
//
//	recover -> base (security headers, CSP nonce, request ID, logger, catalog
//	snapshot) -> host middleware -> internal routing mux
//
// Authorization is NOT here; it is enforced per-route in each handler against
// the request Principal, fail-closed (O6).
func (a *Admin) buildHandler() http.Handler {
	a.buildOnce.Do(func() {
		// The resource set was already built and validated in New; here we only
		// assemble the routes.
		mux := http.NewServeMux()
		mux.HandleFunc("GET /{$}", a.handleIndex)
		// Every {resource} route is wrapped so the resource's own middleware
		// (Resource.Use) runs for exactly that resource's requests and nothing else.
		mux.HandleFunc("GET /{resource}", a.resourceScoped(a.handleList))
		// Literal segments are more specific than {key}, so no ServeMux conflict.
		mux.HandleFunc("GET /{resource}/new", a.resourceScoped(a.handleCreateForm))
		mux.HandleFunc("POST /{resource}/new", a.resourceScoped(a.handleCreate))
		mux.HandleFunc("GET /{resource}/export.csv", a.resourceScoped(a.handleExport))
		mux.HandleFunc("GET /{resource}/options.json", a.resourceScoped(a.handleOptions))
		mux.HandleFunc("POST /{resource}/action", a.resourceScoped(a.handleAction))
		mux.HandleFunc("GET /{resource}/{key}", a.resourceScoped(a.handleDetail))
		mux.HandleFunc("GET /{resource}/{key}/edit", a.resourceScoped(a.handleEditForm))
		mux.HandleFunc("POST /{resource}/{key}/edit", a.resourceScoped(a.handleUpdate))
		mux.HandleFunc("POST /{resource}/{key}/delete", a.resourceScoped(a.handleDelete))

		// Static assets are dispatched by literal prefix BEFORE the wildcard mux,
		// so they never collide with the {resource} patterns (which a subtree
		// route would, in Go's ServeMux conflict detection).
		routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, staticPrefix) {
				a.handleStatic(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		})

		// Strip the base path so the internal patterns are root-relative.
		stripped := http.StripPrefix(a.cfg.basePath, routed)

		h := chain(stripped, a.cfg.middleware...) // host middleware (may set Principal)
		h = a.baseMiddleware(h)                   // request-scoped context + headers
		h = a.recoverMiddleware(h)                // outermost: escaped panic -> 500
		a.handler = h
	})
	return a.handler
}

// resourceScoped wraps a resource handler so that, when the request's resource
// declares middleware via Resource.Use, that middleware wraps only this
// resource's routes (and no other resource's). The resource is resolved from the
// per-request state snapshot that baseMiddleware installed before routing, so a
// mid-request Reload cannot shift which chain applies. Resources without
// middleware pay nothing beyond a map lookup.
func (a *Admin) resourceScoped(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := stateFromContext(r.Context())
		if st != nil {
			if res, ok := st.resource(r.PathValue("resource")); ok && len(res.middleware) > 0 {
				chain(h, res.middleware...).ServeHTTP(w, r)
				return
			}
		}
		h(w, r)
	}
}

// recoverMiddleware converts any panic that escapes a handler into a logged,
// request-ID'd generic 500 -- a live server never crashes on a request-path panic
// (cross-cutting convention, F5).
func (a *Admin) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := RequestIDFromContext(r.Context())
				a.cfg.logger.Error("pgdesk: recovered panic in request",
					"panic", rec, "request_id", reqID, "path", r.URL.Path)
				a.renderError(w, r, http.StatusInternalServerError, "An unexpected error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// baseMiddleware sets security headers + a per-request CSP nonce (F2, F3),
// resolves/generates the request ID (O5), binds a request-scoped logger, loads
// the catalog snapshot ONCE (D1), and applies the per-request query deadline
// window to the context.
func (a *Admin) baseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.closed.Load() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		nonce := newRequestID() // 128-bit random, reused as the CSP nonce
		reqID := sanitizeRequestID(r.Header.Get("X-Request-Id"))

		setSecurityHeaders(w.Header(), nonce)
		w.Header().Set("X-Request-Id", reqID)

		logger := a.cfg.logger.With("request_id", reqID)

		ctx := r.Context()
		ctx = withRequestID(ctx, reqID)
		ctx = withLogger(ctx, logger)
		ctx = withNonce(ctx, nonce)
		ctx = withState(ctx, a.state.Load()) // one (catalog, resources) snapshot for the request

		// Read + clear one-shot flash messages on display requests only.
		if r.Method == http.MethodGet {
			ctx = withFlash(ctx, a.takeFlash(w, r))
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// setSecurityHeaders writes the default hardening headers. They are applied by
// the handler and can be overridden by host middleware wrapping it (F3).
func setSecurityHeaders(h http.Header, nonce string) {
	h.Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'nonce-"+nonce+"'; "+
			"style-src 'self' 'nonce-"+nonce+"'; "+
			"img-src 'self' data:; "+
			"frame-ancestors 'none'; "+
			"base-uri 'none'; "+
			"form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// queryContext derives a bounded context for a DB call from the request (O2).
func (a *Admin) queryContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), a.cfg.queryTimeout)
}

// --- authorization entry point (O6) ---

// can reports whether the current principal may perform a capability. It is the
// one predicate the whole package asks: guard calls it to enforce, and the view
// models call it to decide whether to render an affordance. Because both read the
// same answer, the UI cannot offer an operation the guard will refuse.
//
// An authorizer error is logged and denies.
func (a *Admin) can(r *http.Request, capability Capability, resource, action string) bool {
	attrs := Attributes{
		Principal:  PrincipalFromContext(r.Context()),
		Capability: capability,
		Resource:   resource,
		Action:     action,
	}
	ok, err := permitted(r.Context(), a.cfg.authorizer, attrs)
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: authorizer error",
			"capability", capability, "resource", resource, "error", err)
		return false
	}
	return ok
}

// guard resolves the principal and checks a capability before any query runs. On
// deny it writes the appropriate response (redirect to login for protected GETs,
// 401/403 otherwise) and returns false. A true result means the caller may
// proceed.
func (a *Admin) guard(w http.ResponseWriter, r *http.Request, capability Capability, resource, action string) bool {
	if PrincipalFromContext(r.Context()) == nil {
		a.denyUnauthenticated(w, r)
		return false
	}
	if !a.can(r, capability, resource, action) {
		a.renderError(w, r, http.StatusForbidden, "You are not permitted to perform this action.")
		return false
	}
	return true
}

// denyUnauthenticated redirects browser GETs to the login URL (if configured) or
// returns 401. Mutating/JSON routes always get 401 (they must not follow a
// redirect blindly).
func (a *Admin) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && a.cfg.loginURL != "" {
		http.Redirect(w, r, a.cfg.loginURL, http.StatusSeeOther)
		return
	}
	a.renderError(w, r, http.StatusUnauthorized, "Authentication is required.")
}

// --- static assets (F8) ---

func (a *Admin) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		a.renderError(w, r, http.StatusInternalServerError, "Static assets unavailable.")
		return
	}
	// Trim the literal prefix to get the asset path (e.g. "pgdesk.css").
	// embed.FS + fs.ValidPath structurally prevent traversal (F8).
	name := strings.TrimPrefix(r.URL.Path, staticPrefix)
	if name == "" || !fs.ValidPath(name) {
		a.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	f, err := sub.Open(name)
	if err != nil {
		a.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		a.renderError(w, r, http.StatusNotFound, "Not found.")
		return
	}
	seeker, ok := f.(io.ReadSeeker)
	if !ok {
		a.renderError(w, r, http.StatusInternalServerError, "Static assets unavailable.")
		return
	}
	// Long immutable cache: assets are embedded and versioned with the binary.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// --- index (O6: CapAccessAdmin) ---

func (a *Admin) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r, CapAccessAdmin, "", "") {
		return
	}
	st := stateFromContext(r.Context())
	var items []resourceNav
	if st != nil {
		for _, name := range st.order {
			items = append(items, resourceNav{Name: name, LabelPlural: st.resources[name].LabelPlural})
		}
	}
	data := indexView{
		Base:      a.baseView(r, a.cfg.title),
		Resources: items,
	}
	a.renderPage(w, r, http.StatusOK, "index", data)
}

// --- rendering helpers ---

// baseView builds the layout data shared by every page.
func (a *Admin) baseView(r *http.Request, title string) baseView {
	principal := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		principal = p.DisplayName()
	}
	current := r.PathValue("resource")
	var nav []navItem
	if st := stateFromContext(r.Context()); st != nil {
		for _, name := range st.order {
			nav = append(nav, navItem{
				Label:  st.resources[name].LabelPlural,
				URL:    a.cfg.basePath + "/" + name,
				Active: name == current,
			})
		}
	}
	return baseView{
		Title:     title,
		SiteTitle: a.cfg.title,
		BasePath:  a.cfg.basePath,
		Principal: principal,
		Nav:       nav,
		Flash:     flashFromContext(r.Context()),
	}
}

// renderPage renders a page with request-scoped CSRF token and CSP nonce. A
// render failure is logged and converted to a generic 500 (F5).
func (a *Admin) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	token := a.ensureCSRFToken(w, r)
	rf := render.RequestFuncs{CSRFToken: token, Nonce: nonceFromContext(r.Context())}
	if err := a.renderer.RenderPage(w, status, name, rf, data); err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: render failed",
			"template", name, "error", err, "request_id", RequestIDFromContext(r.Context()))
		// Best-effort generic error; headers may already be partially set.
		a.renderError(w, r, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}

// renderError renders the opaque error page. It NEVER includes stack traces,
// SQL, schema names, or driver errors -- only a generic message plus the request
// ID for correlation with server logs (F5).
func (a *Admin) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	data := errorView{
		Base:       a.baseView(r, http.StatusText(status)),
		Status:     status,
		StatusText: http.StatusText(status),
		Message:    message,
		RequestID:  RequestIDFromContext(r.Context()),
	}
	rf := render.RequestFuncs{Nonce: nonceFromContext(r.Context())}
	if err := a.renderer.RenderPage(w, status, "error", rf, data); err != nil {
		// Absolute fallback: plain text, still leaking nothing sensitive.
		http.Error(w, http.StatusText(status), status)
	}
}

// ensureCSRFToken returns a signed token for the current request and ensures the
// __Host- cookie is set. It reuses a valid existing cookie so the token is stable
// across page views (double-submit needs cookie == form token). Returns "" when
// no signer is configured (read-only admin) (D5).
func (a *Admin) ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if a.signer == nil {
		return ""
	}
	if c, err := r.Cookie(csrf.CookieName); err == nil {
		if verr := a.signer.Verify(c.Value); verr == nil {
			return c.Value // reuse existing valid token
		}
	}
	token, err := a.signer.Issue()
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: issuing CSRF token", "error", err)
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrf.CookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

// verifyCSRF enforces the double-submit contract on a mutating request (D5).
func (a *Admin) verifyCSRF(r *http.Request) error {
	if a.signer == nil {
		return fmt.Errorf("pgdesk: no CSRF signer configured")
	}
	c, err := r.Cookie(csrf.CookieName)
	if err != nil {
		return fmt.Errorf("pgdesk: missing CSRF cookie: %w", err)
	}
	form := r.PostFormValue(csrf.FormField)
	return a.signer.VerifyDoubleSubmit(c.Value, form)
}

// safeRedirect validates a redirect target: it must be a relative path under the
// admin base path (starts with "/", not "//", no scheme, no host). Anything else
// falls back to the admin root (F4).
func (a *Admin) safeRedirect(dest string) string {
	root := a.cfg.basePath + "/"
	if dest == "" || dest[0] != '/' || strings.HasPrefix(dest, "//") {
		return root
	}
	if strings.ContainsAny(dest, "\\") || strings.Contains(dest, "://") {
		return root
	}
	if !strings.HasPrefix(dest, a.cfg.basePath+"/") && dest != a.cfg.basePath {
		return root
	}
	return dest
}
