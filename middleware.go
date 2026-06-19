package pgdesk

import "net/http"

// Middleware is the standard net/http middleware shape, so pgdesk composes with
// any stdlib-compatible middleware the host already uses (no framework
// dependency). Middleware added via WithMiddleware wraps the entire admin
// handler; per-resource middleware (Resource.Use) wraps only that resource's
// routes.
type Middleware func(http.Handler) http.Handler

// chain applies middleware so that the first element is the outermost wrapper
// and runs first on the way in. chain(h, a, b) yields a(b(h)).
func chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		if mw[i] != nil {
			h = mw[i](h)
		}
	}
	return h
}
