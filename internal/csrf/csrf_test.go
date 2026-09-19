package csrf

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func withFrozenTime(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	prev := timeNow
	cur := at
	timeNow = func() time.Time { return cur }
	t.Cleanup(func() { timeNow = prev })
	return func(next time.Time) { cur = next }
}

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

	rotated := mustSigner(t, []byte("new-primary"), []byte("old-primary"))
	if err := rotated.Verify(oldTok); err != nil {
		t.Fatalf("rotated signer should still verify old token: %v", err)
	}

	fresh := mustSigner(t, []byte("new-primary"))
	if err := fresh.Verify(oldTok); err == nil {
		t.Fatal("signer without retired key must reject old token")
	}

	newTok, _ := rotated.Issue()
	if err := fresh.Verify(newTok); err != nil {
		t.Fatalf("fresh signer should verify new-primary token: %v", err)
	}
}

func TestVerifyEnforcesTTL(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name    string
		issueAt time.Time
		checkAt time.Time
		want    error
	}{
		{
			name:    "fresh token verifies",
			issueAt: base,
			checkAt: base,
			want:    nil,
		},
		{
			name:    "within TTL verifies",
			issueAt: base,
			checkAt: base.Add(TTL - time.Minute),
			want:    nil,
		},
		{
			name:    "at TTL boundary still verifies",
			issueAt: base,
			checkAt: base.Add(TTL),
			want:    nil,
		},
		{
			name:    "past TTL expires",
			issueAt: base,
			checkAt: base.Add(TTL + time.Second),
			want:    ErrExpired,
		},
		{
			name:    "well past TTL expires",
			issueAt: base,
			checkAt: base.Add(48 * time.Hour),
			want:    ErrExpired,
		},
		{
			name:    "future within skew verifies",
			issueAt: base,
			checkAt: base.Add(-clockSkew),
			want:    nil,
		},
		{
			name:    "future beyond skew rejected",
			issueAt: base,
			checkAt: base.Add(-clockSkew - time.Minute),
			want:    ErrFutureDated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustSigner(t, []byte("ttl-secret-key"))
			setNow := withFrozenTime(t, tc.issueAt)
			tok, err := s.Issue()
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			setNow(tc.checkAt)
			err = s.Verify(tok)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Verify: got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Verify: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyRejectsPreTTLFormat(t *testing.T) {
	key := []byte("legacy-key")
	s := mustSigner(t, key)

	nonce := make([]byte, nonceLen)
	oldTok := enc.EncodeToString(nonce) + "." + enc.EncodeToString(sign(key, nonce))

	if err := s.Verify(oldTok); !errors.Is(err, ErrMalformed) {
		t.Fatalf("pre-TTL token: got %v, want ErrMalformed", err)
	}
}

func TestVerifyDoubleSubmit(t *testing.T) {
	s := mustSigner(t, []byte("dsk"))
	a, _ := s.Issue()
	b, _ := s.Issue()

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
