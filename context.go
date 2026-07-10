package pgdesk

import (
	"context"
	"log/slog"
	"reflect"
)

// ctxKey is an unexported context key type so pgdesk's values never collide with
// the host's context keys.
type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyState
	ctxKeyRequestID
	ctxKeyLogger
	ctxKeyNonce
	ctxKeyFlash
)

// WithPrincipal returns a copy of ctx carrying the authenticated operator. The
// host's authentication middleware calls this; pgdesk reads it for every
// authorization decision (O6).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFromContext returns the Principal attached by WithPrincipal, or nil if
// none is present. A nil principal on a protected route is denied (O6).
//
// A typed-nil pointer principal -- e.g. WithPrincipal(ctx, (*AppUser)(nil)) -- is
// treated as absent and returned as nil. Without this normalization the interface
// value would be non-nil, defeating every "Principal == nil" fail-closed gate and
// inviting a nil-receiver panic or a forged audit actor. The single choke point
// here means each read path stays fail-closed without repeating the check.
func PrincipalFromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxKeyPrincipal).(Principal)
	if p == nil {
		return nil
	}
	if rv := reflect.ValueOf(p); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	return p
}

// RequestIDFromContext returns the request ID bound for the current request, or
// "" if none. It flows into every log line, audit event, and the opaque 500 page
// so an operator can correlate a browser error with the real cause (O5, F5).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// LoggerFromContext returns the request-scoped *slog.Logger, or slog.Default if
// none is bound.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// stateFromContext returns the request-scoped (catalog, resources) snapshot
// loaded once at the top of the request (D1). Using this snapshot for the whole
// request means a mid-request Reload cannot shift the schema or resource set
// underneath a handler.
func stateFromContext(ctx context.Context) *adminState {
	s, _ := ctx.Value(ctxKeyState).(*adminState)
	return s
}

func withState(ctx context.Context, s *adminState) context.Context {
	return context.WithValue(ctx, ctxKeyState, s)
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// nonceFromContext returns the per-request CSP nonce (F2), or "" if none.
func nonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(ctxKeyNonce).(string)
	return n
}

func withNonce(ctx context.Context, n string) context.Context {
	return context.WithValue(ctx, ctxKeyNonce, n)
}

// flashFromContext returns the one-shot flash messages read for this request.
func flashFromContext(ctx context.Context) []flashMsg {
	f, _ := ctx.Value(ctxKeyFlash).([]flashMsg)
	return f
}

func withFlash(ctx context.Context, f []flashMsg) context.Context {
	return context.WithValue(ctx, ctxKeyFlash, f)
}
