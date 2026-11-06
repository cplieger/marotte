// Package userjson prepares the bytes of a JSON file a person may have saved with an editor.
package userjson

import "bytes"

// bom is the UTF-8 byte order mark a Windows editor prepends to a saved file; encoding/json rejects it.
var bom = []byte{0xEF, 0xBB, 0xBF}

// Body returns data without one leading UTF-8 byte order mark, as Python's utf-8-sig codec
// decodes it: a second mark stays, and fails the parse. Data without a mark is returned as is.
func Body(data []byte) []byte {
	return bytes.TrimPrefix(data, bom)
}

// Empty reports body holding only JSON whitespace (RFC 8259 §2: space, tab, LF, CR). \v and \f
// are not JSON whitespace, so bytes.TrimSpace would wave through a file JSON rejects.
func Empty(body []byte) bool {
	return len(bytes.Trim(body, " \t\n\r")) == 0
}
