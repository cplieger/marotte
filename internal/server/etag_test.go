package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func conditional(name string, values ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	for _, v := range values {
		r.Header.Add(name, v)
	}
	return r
}

// The expected values follow RFC 9110 section 8.8.3.2's comparison table and the list syntax
// of section 5.6.1.
func TestIfMatchHolds(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{"the same strong tag", []string{`"1-2"`}, true},
		{"a member of a list", []string{`"x", "1-2"`}, true},
		{"a member on a second field line", []string{`"x"`, `"1-2"`}, true},
		{"a weak spelling of the tag", []string{`W/"1-2"`}, false},
		{"another tag", []string{`"1-3"`}, false},
		{"a comma inside a tag is not a separator", []string{`"1-2,x"`}, false},
		{"no field", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ifMatchHolds(conditional(headerIfMatch, c.values...), `"1-2"`); got != c.want {
				t.Errorf("ifMatchHolds(%q) = %t, want %t", c.values, got, c.want)
			}
		})
	}
}

func TestIfNoneMatchHolds(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{"the same tag", []string{`"ab"`}, true},
		{"the tag a content coding weakened", []string{`W/"ab"`}, true},
		{"a member of a list", []string{`"x", W/"ab"`}, true},
		{"a member on a second field line", []string{`"x"`, `"ab"`}, true},
		{"the wildcard", []string{`*`}, true},
		{"another tag", []string{`"ac"`}, false},
		{"no field", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ifNoneMatchHolds(conditional(headerIfNoneMatch, c.values...), `"ab"`); got != c.want {
				t.Errorf("ifNoneMatchHolds(%q) = %t, want %t", c.values, got, c.want)
			}
		})
	}
}
