// Package csrf implements pgdesk's signed double-submit CSRF protection (D5).
//
// pgdesk owns a dedicated, independent CSRF cookie so it never depends on the
// host application's session. A token is a signed value:
//
//	token = base64url(nonce) + "." + base64url(HMAC-SHA256(secret, nonce))
//
// On a mutating request all three must hold:
//   - the cookie token is validly signed,
//   - the form token is validly signed,
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
	"errors"
	"strings"
)

// CookieName is the fixed, host-locked CSRF cookie name. The __Host- prefix is a
// browser-enforced contract: the cookie must be Secure, have no Domain, and use
// Path=/. Browsers reject a __Host- cookie that violates those rules, which is
// exactly the subdomain-injection defense we want.
const CookieName = "__Host-pgdesk_csrf"

// FormField is the hidden form field name carrying the token on POST.
const FormField = "_pgdesk_csrf"

const nonceLen = 32

var (
	// ErrNoKeys is returned by NewSigner when no signing key is supplied.
	ErrNoKeys = errors.New("pgdesk/csrf: at least one signing key is required")
	// ErrEmptyKey is returned when a supplied key has zero length.
	ErrEmptyKey = errors.New("pgdesk/csrf: signing key must not be empty")
	// ErrMalformed indicates a token that is not in the nonce.signature form.
	ErrMalformed = errors.New("pgdesk/csrf: malformed token")
	// ErrBadSignature indicates a token whose signature matches no configured key.
	ErrBadSignature = errors.New("pgdesk/csrf: signature does not verify")
	// ErrMismatch indicates the cookie and form tokens differ.
	ErrMismatch = errors.New("pgdesk/csrf: cookie and form tokens differ")
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

// Issue mints a fresh token signed with the primary key. Each call uses a new
// random nonce, so tokens are unpredictable and single-request-scoped in spirit.
func (s *Signer) Issue() (string, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("pgdesk/csrf: reading randomness: " + err.Error())
	}
	sig := sign(s.keys[0], nonce)
	return enc.EncodeToString(nonce) + "." + enc.EncodeToString(sig), nil
}

// Verify reports whether token is well-formed and signed by any configured key.
// It never reveals which key matched. Comparison is constant time.
func (s *Signer) Verify(token string) error {
	nonce, sig, err := split(token)
	if err != nil {
		return err
	}
	for _, k := range s.keys {
		if hmac.Equal(sig, sign(k, nonce)) {
			return nil
		}
	}
	return ErrBadSignature
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

func split(token string) (nonce, sig []byte, err error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return nil, nil, ErrMalformed
	}
	nonce, err = enc.DecodeString(token[:dot])
	if err != nil || len(nonce) != nonceLen {
		return nil, nil, ErrMalformed
	}
	sig, err = enc.DecodeString(token[dot+1:])
	if err != nil || len(sig) != sha256.Size {
		return nil, nil, ErrMalformed
	}
	return nonce, sig, nil
}
