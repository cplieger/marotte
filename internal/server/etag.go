package server

import (
	"net/http"
	"strings"
)

// entityTags returns the members of h's name field, an RFC 9110 section 5.6.1 list over every
// field line, split at commas outside the quotes an opaque-tag may hold commas in.
func entityTags(h http.Header, name string) []string {
	field := strings.Join(h.Values(name), ",")
	var tags []string
	start, inQuote := 0, false
	for i := 0; i <= len(field); i++ {
		if i < len(field) {
			if field[i] == '"' {
				inQuote = !inQuote
			}
			if field[i] != ',' || inQuote {
				continue
			}
		}
		if tag := strings.Trim(field[start:i], " \t"); tag != "" {
			tags = append(tags, tag)
		}
		start = i + 1
	}
	return tags
}

// ifMatchHolds reports whether r's If-Match lists etag, compared strongly as RFC 9110 section
// 13.1.1 requires: a weak member never matches.
func ifMatchHolds(r *http.Request, etag string) bool {
	for _, tag := range entityTags(r.Header, headerIfMatch) {
		if tag == etag && !strings.HasPrefix(tag, "W/") {
			return true
		}
	}
	return false
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
