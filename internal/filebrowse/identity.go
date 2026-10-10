package filebrowse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// fileIDPrefix opens every file identity: the SHA-256 of the file's bytes, the one
// identity the viewer, the save guard, the download and the image cache key share.
// Only a file at or below WholeFileMax has one.
const fileIDPrefix = "sha256:"

var errInvalidFileID = errors.New("invalid file_id")

// fileIDOf is the identity of data.
func fileIDOf(data []byte) string {
	sum := sha256.Sum256(data)
	return fileIDPrefix + hex.EncodeToString(sum[:])
}

// parseFileID validates a client-sent identity: the prefix and 64 lowercase hex digits.
func parseFileID(s string) (string, error) {
	digest, ok := strings.CutPrefix(s, fileIDPrefix)
	if !ok || len(digest) != sha256.Size*2 {
		return "", errInvalidFileID
	}
	for i := range len(digest) {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errInvalidFileID
		}
	}
	return s, nil
}

// etagOf is the strong validator for an identity. Quoted, because http.ServeContent
// ignores an unquoted ETag.
func etagOf(id string) string { return strconv.Quote(id) }
