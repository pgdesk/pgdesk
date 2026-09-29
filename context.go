package pgdesk

import (
	"context"
	"log/slog"
	"reflect"
)

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyState
	ctxKeyRequestID
	ctxKeyLogger
	ctxKeyNonce
	ctxKeyFlash
)

// WithPrincipal returns a copy of ctx that carries p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFromContext returns the principal in ctx, or nil. A nil pointer counts as nil.
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

// RequestIDFromContext returns the request ID: the X-Request-Id header if valid, otherwise a random one.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// LoggerFromContext returns the request's logger, or slog.Default outside a request.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

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

func nonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(ctxKeyNonce).(string)
	return n
}

func withNonce(ctx context.Context, n string) context.Context {
	return context.WithValue(ctx, ctxKeyNonce, n)
}

func flashFromContext(ctx context.Context) []flashMsg {
	f, _ := ctx.Value(ctxKeyFlash).([]flashMsg)
	return f
}

func withFlash(ctx context.Context, f []flashMsg) context.Context {
	return context.WithValue(ctx, ctxKeyFlash, f)
}
