package server

import (
	"net/http"
	"slices"
	"strings"
)

// entityTags returns the members of h's name field, an RFC 9110 section 5.6.1 list over every
// field line, split at commas outside the quotes an opaque-tag may hold commas in.
func entityTags(h http.Header, name string) []string {
	field := strings.Join(h.Values(name), ",")
	var tags []string
	add := func(member string) {
		if tag := strings.Trim(member, " \t"); tag != "" {
			tags = append(tags, tag)
		}
	}
	start, inQuote := 0, false
	for i := range len(field) {
		switch {
		case field[i] == '"':
			inQuote = !inQuote
		case field[i] == ',' && !inQuote:
			add(field[start:i])
			start = i + 1
		}
	}
	add(field[start:])
	return tags
}

// ifMatchHolds reports whether r's If-Match lists etag, compared strongly as RFC 9110 section
// 13.1.1 requires. etag must be strong, so a weak member never matches it. A "*" member is not
// honoured, since it would let a save overwrite a document it never read.
func ifMatchHolds(r *http.Request, etag string) bool {
	return slices.Contains(entityTags(r.Header, headerIfMatch), etag)
}

// ifNoneMatchHolds reports whether r's If-None-Match is "*" or lists etag, compared weakly as
// RFC 9110 section 13.1.2 requires, so a tag a content coding weakened still revalidates.
func ifNoneMatchHolds(r *http.Request, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, tag := range entityTags(r.Header, headerIfNoneMatch) {
		if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
			return true
		}
	}
	return false
}
