package preview

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestSigner(t *testing.T, now time.Time) *Signer {
	t.Helper()
	s, err := NewSigner()
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	s.now = func() time.Time { return now }
	return s
}

func TestSigner_RoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newTestSigner(t, now)
	exp := now.Add(time.Hour)
	c, err := s.Verify(s.Mint("/w/demo", exp))
	if err != nil {
		t.Fatalf("Verify(Mint) = %v, want nil", err)
	}
	if c.Folder != "/w/demo" || !c.Expiry.Equal(exp) {
		t.Errorf("Verify(Mint) = %+v, want folder /w/demo expiring %v", c, exp)
	}
	if len(s.Epoch()) != 16 {
		t.Errorf("Epoch() = %q, want 16 hex characters", s.Epoch())
	}
}

func TestSigner_RefusesForgeries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newTestSigner(t, now)
	tok := s.Mint("/w/demo", now.Add(time.Hour))
	p, sig, _ := strings.Cut(tok, ".")
	flip := func(x string, i int) string {
		b := []byte(x)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	other := newTestSigner(t, now)
	cases := map[string]string{
		"payload byte":   flip(p, 3) + "." + sig,
		"signature byte": p + "." + flip(sig, 3),
		"swapped halves": sig + "." + p,
		"no separator":   p + sig,
		"bad base64":     "!!!." + sig,
		"other key":      other.Mint("/w/demo", now.Add(time.Hour)),
		"too long":       strings.Repeat("a", tokenMaxLen+1),
		"empty":          "",
	}
	for name, bad := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			if _, err := s.Verify(bad); !errors.Is(err, errBadToken) {
				t.Errorf("Verify(%s) = %v, want errBadToken", name, err)
			}
		})
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestSigner_RefusesWrongVersion(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newTestSigner(t, now)
	payload := []byte{2, 0, 0, 0, 0, 0x7f, 0, 0, 0, '/', 'w'}
	tok := b64(payload) + "." + b64(s.sign(payload))
	if _, err := s.Verify(tok); !errors.Is(err, errBadToken) {
		t.Errorf("Verify(version 2) = %v, want errBadToken", err)
	}
}

func TestSigner_RefusesExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newTestSigner(t, now)
	for name, exp := range map[string]time.Time{"past": now.Add(-time.Second), "now": now} {
		if _, err := s.Verify(s.Mint("/w/demo", exp)); !errors.Is(err, errExpiredToken) {
			t.Errorf("Verify(%s expiry) = %v, want errExpiredToken", name, err)
		}
	}
}

func FuzzVerify(f *testing.F) {
	now := time.Unix(1_700_000_000, 0)
	s, err := NewSigner()
	if err != nil {
		f.Fatal(err)
	}
	s.now = func() time.Time { return now }
	valid := s.Mint("/w/demo", now.Add(time.Hour))
	f.Add(valid)
	f.Add("")
	f.Add("a.b")
	f.Fuzz(func(t *testing.T, tok string) {
		c, err := s.Verify(tok)
		if err == nil && tok != valid && c.Folder != "/w/demo" {
			t.Fatalf("Verify(%q) granted folder %q", tok, c.Folder)
		}
	})
}
