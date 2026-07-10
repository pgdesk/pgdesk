// Package csrf implements pgdesk's signed double-submit CSRF protection (D5).
//
// pgdesk owns a dedicated, independent CSRF cookie so it never depends on the
// host application's session. A token is a signed, timestamped value:
//
//	token = base64url(ts || nonce) + "." + base64url(HMAC-SHA256(secret, ts || nonce))
//
// where ts is the 8-byte big-endian Unix issuance time (seconds) and nonce is
// 32 random bytes. Embedding ts inside the signed payload lets Verify enforce a
// TTL: a token older than TTL (or dated in the future beyond a small clock skew)
// is rejected even though its signature is valid.
//
// On a mutating request all three must hold:
//   - the cookie token is validly signed and unexpired,
//   - the form token is validly signed and unexpired,
//   - the cookie and form tokens are byte-equal (constant time).
//
// The cookie uses the __Host- prefix (Secure, Path=/, no Domain), HttpOnly, and
// SameSite=Lax. SameSite is the outer layer; the signed token handles what
// SameSite misses; the __Host- prefix defeats subdomain cookie injection.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// CookieName is the fixed, host-locked CSRF cookie name. The __Host- prefix is a
// browser-enforced contract: the cookie must be Secure, have no Domain, and use
// Path=/. Browsers reject a __Host- cookie that violates those rules, which is
// exactly the subdomain-injection defense we want.
const CookieName = "__Host-pgdesk_csrf"

// FormField is the hidden form field name carrying the token on POST.
const FormField = "_pgdesk_csrf"

const (
	nonceLen = 32 // random bytes per token
	stampLen = 8  // big-endian Unix seconds prefixed to the signed payload
)

// DefaultTTL is how long an issued token stays valid. 12 hours comfortably
// covers a working admin session while bounding the window in which a leaked
// token is useful.
const DefaultTTL = 12 * time.Hour

// clockSkew tolerates modest clock disagreement between issuing and verifying
// hosts before a future-dated token is rejected.
const clockSkew = 2 * time.Minute

// TTL is the token lifetime enforced by Verify. It defaults to DefaultTTL and
// may be overridden at process start (before any Signer is used) to tune the
// session window without changing exported signatures.
var TTL = DefaultTTL

// timeNow reads the wall clock. It is a package var so tests can freeze or
// advance time deterministically; production code never overrides it.
var timeNow = time.Now

var (
	// ErrNoKeys is returned by NewSigner when no signing key is supplied.
	ErrNoKeys = errors.New("pgdesk/csrf: at least one signing key is required")
	// ErrEmptyKey is returned when a supplied key has zero length.
	ErrEmptyKey = errors.New("pgdesk/csrf: signing key must not be empty")
	// ErrMalformed indicates a token that is not in the payload.signature form.
	ErrMalformed = errors.New("pgdesk/csrf: malformed token")
	// ErrBadSignature indicates a token whose signature matches no configured key.
	ErrBadSignature = errors.New("pgdesk/csrf: signature does not verify")
	// ErrMismatch indicates the cookie and form tokens differ.
	ErrMismatch = errors.New("pgdesk/csrf: cookie and form tokens differ")
	// ErrExpired indicates a validly signed token whose age exceeds TTL.
	ErrExpired = errors.New("pgdesk/csrf: token has expired")
	// ErrFutureDated indicates a validly signed token issued in the future
	// beyond the tolerated clock skew.
	ErrFutureDated = errors.New("pgdesk/csrf: token is dated in the future")
)

var enc = base64.RawURLEncoding

// Signer mints and verifies CSRF tokens. It supports key rotation: tokens are
// always signed with the primary (first) key, but verification accepts any
// configured key so retired keys keep validating outstanding tokens.
//
// A Signer is immutable after construction and safe for concurrent use.
type Signer struct {
	keys [][]byte // keys[0] is primary; the rest are retired/rotation keys.
}

// NewSigner builds a Signer. The first key is primary (used for signing); any
// additional keys are accepted only for verification. Keys are copied so the
// caller may reuse or zero the input slices.
func NewSigner(primary []byte, retired ...[]byte) (*Signer, error) {
	if len(primary) == 0 {
		if primary == nil {
			return nil, ErrNoKeys
		}
		return nil, ErrEmptyKey
	}
	keys := make([][]byte, 0, 1+len(retired))
	keys = append(keys, cloneKey(primary))
	for _, k := range retired {
		if len(k) == 0 {
			return nil, ErrEmptyKey
		}
		keys = append(keys, cloneKey(k))
	}
	return &Signer{keys: keys}, nil
}

func cloneKey(k []byte) []byte {
	c := make([]byte, len(k))
	copy(c, k)
	return c
}

// Issue mints a fresh token signed with the primary key. Each call stamps the
// current time and a new random nonce into the signed payload, so tokens are
// unpredictable and TTL-bounded (see Verify).
//
// A token is scoped to the pgdesk CSRF cookie's lifetime, not to a single
// request: the same value is reused across the session until it expires or the
// cookie is replaced. It is deliberately NOT bound to the authenticated
// principal; binding the subject into the signed payload is a possible future
// hardening (it would require the request handler to supply the principal).
func (s *Signer) Issue() (string, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("pgdesk/csrf: reading randomness: " + err.Error())
	}
	payload := make([]byte, stampLen+nonceLen)
	binary.BigEndian.PutUint64(payload[:stampLen], uint64(timeNow().Unix()))
	copy(payload[stampLen:], nonce)
	sig := sign(s.keys[0], payload)
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sig), nil
}

// Verify reports whether token is well-formed, signed by any configured key, and
// within its TTL. It never reveals which key matched. The signature is checked
// before the embedded timestamp is trusted, and comparison is constant time.
func (s *Signer) Verify(token string) error {
	payload, sig, err := split(token)
	if err != nil {
		return err
	}
	verified := false
	for _, k := range s.keys {
		if hmac.Equal(sig, sign(k, payload)) {
			verified = true
			break
		}
	}
	if !verified {
		return ErrBadSignature
	}
	issued := int64(binary.BigEndian.Uint64(payload[:stampLen]))
	now := timeNow().Unix()
	if now-issued > int64(TTL/time.Second) {
		return ErrExpired
	}
	if issued-now > int64(clockSkew/time.Second) {
		return ErrFutureDated
	}
	return nil
}

// VerifyDoubleSubmit enforces the full double-submit contract on a mutating
// request: both tokens must independently verify AND be byte-equal. All
// comparisons are constant time.
func (s *Signer) VerifyDoubleSubmit(cookieToken, formToken string) error {
	if err := s.Verify(cookieToken); err != nil {
		return err
	}
	if err := s.Verify(formToken); err != nil {
		return err
	}
	if !hmac.Equal([]byte(cookieToken), []byte(formToken)) {
		return ErrMismatch
	}
	return nil
}

// Seal signs an arbitrary payload for a tamper-evident cookie (e.g. flash
// messages). The result is base64url(payload) + "." + base64url(HMAC). It is
// authenticated, not encrypted: the payload is readable but cannot be forged.
func (s *Signer) Seal(payload []byte) string {
	mac := sign(s.keys[0], payload)
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac)
}

// Open verifies a Seal token with any configured key and returns the payload, or
// an error if it is malformed or the signature does not verify (constant time).
func (s *Signer) Open(token string) ([]byte, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return nil, ErrMalformed
	}
	payload, err := enc.DecodeString(token[:dot])
	if err != nil {
		return nil, ErrMalformed
	}
	mac, err := enc.DecodeString(token[dot+1:])
	if err != nil || len(mac) != sha256.Size {
		return nil, ErrMalformed
	}
	for _, k := range s.keys {
		if hmac.Equal(mac, sign(k, payload)) {
			return payload, nil
		}
	}
	return nil, ErrBadSignature
}

func sign(key, nonce []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(nonce)
	return mac.Sum(nil)
}

// split decodes a token into its signed payload (timestamp || nonce) and
// signature. Tokens in the pre-TTL format (a bare 32-byte nonce payload) fail
// the length check and surface as ErrMalformed rather than panicking.
func split(token string) (payload, sig []byte, err error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return nil, nil, ErrMalformed
	}
	payload, err = enc.DecodeString(token[:dot])
	if err != nil || len(payload) != stampLen+nonceLen {
		return nil, nil, ErrMalformed
	}
	sig, err = enc.DecodeString(token[dot+1:])
	if err != nil || len(sig) != sha256.Size {
		return nil, nil, ErrMalformed
	}
	return payload, sig, nil
}
