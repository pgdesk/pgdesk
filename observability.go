package pgdesk

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Metrics is an optional hook so hosts can bridge pgdesk internals to
// Prometheus/OTel without the library taking a metrics dependency. The
// default is a no-op. Implementations must be safe for concurrent use and must
// not block.
type Metrics interface {
	// ObserveRequest reports the outcome of a handled admin request.
	ObserveRequest(route string, status int, dur time.Duration)
	// ObserveQuery reports the duration and error state of a DB operation.
	ObserveQuery(op string, dur time.Duration, err error)
}

// nopMetrics is the default Metrics: it discards everything.
type nopMetrics struct{}

func (nopMetrics) ObserveRequest(string, int, time.Duration) {}
func (nopMetrics) ObserveQuery(string, time.Duration, error) {}

// newRequestID generates a random 128-bit request ID as lowercase hex. It is
// used only when the inbound request carries no X-Request-Id.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Randomness failure is not fatal for a correlation ID; fall back to a
		// fixed marker so logs still parse. This path is effectively unreachable.
		return "reqid-unavailable"
	}
	return hex.EncodeToString(b[:])
}

// sanitizeRequestID accepts an inbound X-Request-Id only if it is short and
// consists of safe characters, to keep untrusted input out of log/header
// contexts. Otherwise a fresh ID is generated.
func sanitizeRequestID(v string) string {
	if v == "" || len(v) > 128 {
		return newRequestID()
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
		if !ok {
			return newRequestID()
		}
	}
	return v
}
