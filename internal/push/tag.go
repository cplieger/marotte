package push

import (
	"crypto/sha256"
	"encoding/base64"
)

// tagLen is the length of a client tag: 22 base64url characters carry 132 bits,
// inside the [A-Za-z0-9_-]{1,64} grammar of the SSE-Client header.
const tagLen = 22

// tagOf derives the presence tag of a push subscription: base64url(sha256(endpoint))
// truncated to tagLen. The page computes the same value and sends it as SSE-Client, so a
// presence row and the subscription it silences share a key. Safe to log; the endpoint is not.
func tagOf(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:tagLen]
}
