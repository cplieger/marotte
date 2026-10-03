// Package preview serves an agent-written HTML page and the folder it sits in
// to a sandboxed iframe on marotte's own origin. A grant is an HMAC capability
// naming one folder; the serve route answers only paths beneath it, resolved by
// the kernel with symlinks refused, under a response policy that gives the
// document an opaque origin.
package preview

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
)

// tokenMaxLen bounds a presented token before any decoding.
const tokenMaxLen = 1024

const (
	tokenVersion byte = 1
	tokenDomain       = "marotte-preview-v1\x00"
)

var (
	errBadToken     = errors.New("invalid preview token")
	errExpiredToken = errors.New("preview token expired")
)

// Capability is what a verified token grants: read access to Folder and
// everything beneath it until Expiry.
type Capability struct {
	Expiry time.Time
	Folder string
}

// Signer mints and verifies preview tokens. Its key and epoch are per process,
// so a restart invalidates every token it issued.
type Signer struct {
	now   func() time.Time
	epoch string
	key   [32]byte
}

// NewSigner returns a Signer with a fresh random key and epoch.
func NewSigner() (*Signer, error) {
	s := &Signer{now: time.Now}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, err
	}
	var e [8]byte
	if _, err := rand.Read(e[:]); err != nil {
		return nil, err
	}
	s.epoch = hex.EncodeToString(e[:])
	return s, nil
}

// Epoch identifies this process's key; it changes on every restart.
func (s *Signer) Epoch() string { return s.epoch }

// Mint returns a token granting folder until exp.
func (s *Signer) Mint(folder string, exp time.Time) string {
	payload := make([]byte, 0, 9+len(folder))
	payload = append(payload, tokenVersion)
	secs := max(exp.Unix(), 0)
	payload = binary.BigEndian.AppendUint64(payload, uint64(secs))
	payload = append(payload, folder...)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(s.sign(payload))
}

// Verify checks tok's signature, expiry and version and returns the
// capability it carries. Every failure is errBadToken or errExpiredToken.
func (s *Signer) Verify(tok string) (Capability, error) {
	if len(tok) > tokenMaxLen {
		return Capability{}, errBadToken
	}
	p64, s64, ok := strings.Cut(tok, ".")
	if !ok {
		return Capability{}, errBadToken
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(p64)
	if err != nil {
		return Capability{}, errBadToken
	}
	sig, err := enc.DecodeString(s64)
	if err != nil || !hmac.Equal(sig, s.sign(payload)) {
		return Capability{}, errBadToken
	}
	if len(payload) < 9 {
		return Capability{}, errBadToken
	}
	raw := binary.BigEndian.Uint64(payload[1:9])
	if raw > math.MaxInt64 {
		return Capability{}, errBadToken
	}
	exp := time.Unix(int64(raw), 0)
	if !s.now().Before(exp) {
		return Capability{}, errExpiredToken
	}
	if payload[0] != tokenVersion {
		return Capability{}, errBadToken
	}
	return Capability{Folder: string(payload[9:]), Expiry: exp}, nil
}

func (s *Signer) sign(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte(tokenDomain))
	m.Write(payload)
	return m.Sum(nil)
}
