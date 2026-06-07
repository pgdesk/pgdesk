package csrf

import (
	"errors"
	"strings"
	"testing"
)

func mustSigner(t *testing.T, primary []byte, retired ...[]byte) *Signer {
	t.Helper()
	s, err := NewSigner(primary, retired...)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSignerRejectsMissingKey(t *testing.T) {
	if _, err := NewSigner(nil); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("nil key: got %v, want ErrNoKeys", err)
	}
	if _, err := NewSigner([]byte{}); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("empty key: got %v, want ErrEmptyKey", err)
	}
	if _, err := NewSigner([]byte("primary"), []byte{}); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("empty retired key: got %v, want ErrEmptyKey", err)
	}
}

func TestIssueThenVerifyRoundTrips(t *testing.T) {
	s := mustSigner(t, []byte("super-secret-key"))
	tok, err := s.Issue()
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Verify(tok); err != nil {
		t.Fatalf("Verify fresh token: %v", err)
	}
	if !strings.Contains(tok, ".") {
		t.Fatalf("token missing separator: %q", tok)
	}
}

func TestIssueProducesUniqueTokens(t *testing.T) {
	s := mustSigner(t, []byte("k"))
	seen := make(map[string]bool)
	for i := range 100 {
		tok, err := s.Issue()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token at iteration %d", i)
		}
		seen[tok] = true
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	s := mustSigner(t, []byte("key-one"))
	tok, _ := s.Issue()

	cases := map[string]string{
		"empty":            "",
		"no separator":     "abcdef",
		"trailing dot":     "abc.",
		"leading dot":      ".abc",
		"flipped sig byte": flipLastChar(tok),
		"wrong key":        mintWith(t, []byte("different-key")),
		"garbage nonce":    "!!!." + strings.SplitN(tok, ".", 2)[1],
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Verify(bad); err == nil {
				t.Fatalf("Verify(%q) = nil, want error", bad)
			}
		})
	}
}

func TestKeyRotationVerifiesRetiredTokens(t *testing.T) {
	old := mustSigner(t, []byte("old-primary"))
	oldTok, _ := old.Issue()

	// New signer rotates in a fresh primary but keeps the old key for verify.
	rotated := mustSigner(t, []byte("new-primary"), []byte("old-primary"))
	if err := rotated.Verify(oldTok); err != nil {
		t.Fatalf("rotated signer should still verify old token: %v", err)
	}

	// A signer without the old key must reject it.
	fresh := mustSigner(t, []byte("new-primary"))
	if err := fresh.Verify(oldTok); err == nil {
		t.Fatal("signer without retired key must reject old token")
	}

	// New tokens are signed with the new primary.
	newTok, _ := rotated.Issue()
	if err := fresh.Verify(newTok); err != nil {
		t.Fatalf("fresh signer should verify new-primary token: %v", err)
	}
}

func TestVerifyDoubleSubmit(t *testing.T) {
	s := mustSigner(t, []byte("dsk"))
	a, _ := s.Issue()
	b, _ := s.Issue() // valid but different token

	if err := s.VerifyDoubleSubmit(a, a); err != nil {
		t.Fatalf("matching valid tokens: %v", err)
	}
	if err := s.VerifyDoubleSubmit(a, b); !errors.Is(err, ErrMismatch) {
		t.Fatalf("two different valid tokens: got %v, want ErrMismatch", err)
	}
	if err := s.VerifyDoubleSubmit("garbage", a); err == nil {
		t.Fatal("invalid cookie token must fail")
	}
	if err := s.VerifyDoubleSubmit(a, "garbage"); err == nil {
		t.Fatal("invalid form token must fail")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := mustSigner(t, []byte("seal-key"))
	payload := []byte(`{"l":"info","m":"User created"}`)
	tok := s.Seal(payload)
	got, err := s.Open(tok)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload round-trip mismatch: got %q", got)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	s := mustSigner(t, []byte("seal-key"))
	tok := s.Seal([]byte("hello"))
	for _, bad := range []string{"", "no-dot", "abc.", ".abc", flipLastChar(tok), mintSealWith(t, []byte("other"))} {
		if _, err := s.Open(bad); err == nil {
			t.Errorf("Open(%q) = nil, want error", bad)
		}
	}
}

func TestSealVerifiesUnderRotation(t *testing.T) {
	old := mustSigner(t, []byte("old"))
	tok := old.Seal([]byte("x"))
	rotated := mustSigner(t, []byte("new"), []byte("old"))
	if _, err := rotated.Open(tok); err != nil {
		t.Fatalf("rotated signer should open old-sealed token: %v", err)
	}
}

func mintSealWith(t *testing.T, key []byte) string {
	t.Helper()
	return mustSigner(t, key).Seal([]byte("hello"))
}

// flipLastChar corrupts the FIRST base64 character of the token. (The last
// character can encode only ignored trailing bits, so flipping it may decode to
// the same bytes; the first character is always meaningful.)
func flipLastChar(tok string) string {
	if tok == "" {
		return "x"
	}
	b := []byte(tok)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func mintWith(t *testing.T, key []byte) string {
	t.Helper()
	s := mustSigner(t, key)
	tok, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
