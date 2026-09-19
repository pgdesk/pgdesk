package pgdesk

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Metrics interface {
	ObserveRequest(route string, status int, dur time.Duration)

	ObserveQuery(op string, dur time.Duration, err error)
}

type nopMetrics struct{}

func (nopMetrics) ObserveRequest(string, int, time.Duration) {}
func (nopMetrics) ObserveQuery(string, time.Duration, error) {}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {

		return "reqid-unavailable"
	}
	return hex.EncodeToString(b[:])
}

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
