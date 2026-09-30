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

const staticPrefix = "/_static/"

func (a *Admin) buildHandler() http.Handler {
	a.buildOnce.Do(func() {

		mux := http.NewServeMux()
		mux.HandleFunc("GET /{$}", a.handleIndex)

		mux.HandleFunc("GET /{resource}", a.resourceScoped(a.handleList))

		mux.HandleFunc("GET /{resource}/new", a.resourceScoped(a.handleCreateForm))
		mux.HandleFunc("POST /{resource}/new", a.resourceScoped(a.handleCreate))
		mux.HandleFunc("GET /{resource}/export.csv", a.resourceScoped(a.handleExport))
		mux.HandleFunc("GET /{resource}/"+optionsSegment, a.resourceScoped(a.handleOptions))
		mux.HandleFunc("POST /{resource}/action", a.resourceScoped(a.handleAction))
		mux.HandleFunc("GET /{resource}/{key}", a.resourceScoped(a.handleDetail))
		mux.HandleFunc("GET /{resource}/{key}/edit", a.resourceScoped(a.handleEditForm))
		mux.HandleFunc("POST /{resource}/{key}/edit", a.resourceScoped(a.handleUpdate))
		mux.HandleFunc("POST /{resource}/{key}/delete", a.resourceScoped(a.handleDelete))

		routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, staticPrefix) {

				a.handleStatic(w, r)
				return
			}

			if !a.guardAdminAccess(w, r) {
				return
			}
			mux.ServeHTTP(w, r)
		})

		stripped := http.StripPrefix(a.cfg.basePath, routed)

		h := chain(stripped, a.cfg.middleware...)
		h = a.baseMiddleware(h)
		h = a.recoverMiddleware(h)
		a.handler = h
	})
	return a.handler
}

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

func (a *Admin) baseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.closed.Load() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		nonce := newRequestID()
		reqID := sanitizeRequestID(r.Header.Get("X-Request-Id"))

		setSecurityHeaders(w.Header(), nonce)
		w.Header().Set("X-Request-Id", reqID)

		logger := a.cfg.logger.With("request_id", reqID)

		ctx := r.Context()
		ctx = withRequestID(ctx, reqID)
		ctx = withLogger(ctx, logger)
		ctx = withNonce(ctx, nonce)
		ctx = withState(ctx, a.state.Load())

		if r.Method == http.MethodGet {
			ctx = withFlash(ctx, a.takeFlash(w, r))
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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

func (a *Admin) queryContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), a.cfg.queryTimeout)
}

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

func (a *Admin) guardAdminAccess(w http.ResponseWriter, r *http.Request) bool {
	if !answersJSON(r) {
		return a.guard(w, r, CapAccessAdmin, "", "")
	}
	if PrincipalFromContext(r.Context()) == nil {
		a.jsonError(w, r, http.StatusUnauthorized, "Authentication is required.")
		return false
	}
	if !a.can(r, CapAccessAdmin, "", "") {
		a.jsonError(w, r, http.StatusForbidden, "You are not permitted to use this admin.")
		return false
	}
	return true
}

func answersJSON(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/"+optionsSegment)
}

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

func (a *Admin) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && a.cfg.loginURL != "" {
		http.Redirect(w, r, a.cfg.loginURL, http.StatusSeeOther)
		return
	}
	a.renderError(w, r, http.StatusUnauthorized, "Authentication is required.")
}

func (a *Admin) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		a.renderError(w, r, http.StatusInternalServerError, "Static assets unavailable.")
		return
	}

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

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

func (a *Admin) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !a.guard(w, r, CapAccessAdmin, "", "") {
		return
	}
	st := stateFromContext(r.Context())
	var items []resourceNav
	for _, name := range a.listableResources(r, st) {
		items = append(items, resourceNav{Name: name, LabelPlural: st.resources[name].LabelPlural})
	}
	data := indexView{
		Base:      a.baseView(r, a.cfg.title),
		Resources: items,
		Groups:    groupIndex(items),
	}
	a.renderPage(w, r, http.StatusOK, "index", data)
}

func (a *Admin) listableResources(r *http.Request, st *adminState) []string {
	if st == nil {
		return nil
	}
	out := make([]string, 0, len(st.order))
	for _, name := range st.order {
		if a.can(r, CapList, name, "") {
			out = append(out, name)
		}
	}
	return out
}

func (a *Admin) baseView(r *http.Request, title string) baseView {
	principal := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		principal = p.DisplayName()
	}
	current := r.PathValue("resource")
	var nav []navItem
	st := stateFromContext(r.Context())
	for _, name := range a.listableResources(r, st) {
		nav = append(nav, navItem{
			Label:  st.resources[name].LabelPlural,
			URL:    a.cfg.basePath + "/" + name,
			Active: name == current,
		})
	}
	return baseView{
		Title:     title,
		SiteTitle: a.cfg.title,
		BasePath:  a.cfg.basePath,
		Principal: principal,
		LogoutURL: a.cfg.logoutURL,
		Nav:       nav,
		NavGroups: groupNav(nav),
		Flash:     flashFromContext(r.Context()),
	}
}

func (a *Admin) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	token := a.ensureCSRFToken(w, r)
	rf := render.RequestFuncs{CSRFToken: token, Nonce: nonceFromContext(r.Context())}
	if err := a.renderer.RenderPage(w, status, name, rf, data); err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: render failed",
			"template", name, "error", err, "request_id", RequestIDFromContext(r.Context()))

		a.renderError(w, r, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}

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

		http.Error(w, http.StatusText(status), status)
	}
}

func (a *Admin) ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if a.signer == nil {
		return ""
	}
	if c, err := r.Cookie(a.csrfCookieName()); err == nil {
		if verr := a.signer.Verify(c.Value); verr == nil {
			return c.Value
		}
	}
	token, err := a.signer.Issue()
	if err != nil {
		LoggerFromContext(r.Context()).Error("pgdesk: issuing CSRF token", "error", err)
		return ""
	}
	http.SetCookie(w, a.newCookie(a.csrfCookieName(), token, "/", 0))
	return token
}

func (a *Admin) verifyCSRF(r *http.Request) error {
	if a.signer == nil {
		return fmt.Errorf("pgdesk: no CSRF signer configured")
	}
	c, err := r.Cookie(a.csrfCookieName())
	if err != nil {
		if !a.cfg.insecureCookies && !isHTTPS(r) {
			return fmt.Errorf("pgdesk: missing CSRF cookie: %w (%w)", err, errPlainHTTP)
		}
		return fmt.Errorf("pgdesk: missing CSRF cookie: %w", err)
	}
	form := r.PostFormValue(csrf.FormField)
	return a.signer.VerifyDoubleSubmit(c.Value, form)
}

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
