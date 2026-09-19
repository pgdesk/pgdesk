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

const CookieName = "__Host-pgdesk_csrf"

const FormField = "_pgdesk_csrf"

const (
	nonceLen = 32
	stampLen = 8
)

const DefaultTTL = 12 * time.Hour

const clockSkew = 2 * time.Minute

var TTL = DefaultTTL

var timeNow = time.Now

var (
	ErrNoKeys = errors.New("pgdesk/csrf: at least one signing key is required")

	ErrEmptyKey = errors.New("pgdesk/csrf: signing key must not be empty")

	ErrMalformed = errors.New("pgdesk/csrf: malformed token")

	ErrBadSignature = errors.New("pgdesk/csrf: signature does not verify")

	ErrMismatch = errors.New("pgdesk/csrf: cookie and form tokens differ")

	ErrExpired = errors.New("pgdesk/csrf: token has expired")

	ErrFutureDated = errors.New("pgdesk/csrf: token is dated in the future")
)

var enc = base64.RawURLEncoding

type Signer struct {
	keys [][]byte
}

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

func (s *Signer) Seal(payload []byte) string {
	mac := sign(s.keys[0], payload)
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac)
}

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
