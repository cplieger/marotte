package userjson

import (
	"bytes"
	"testing"
)

func TestBody(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a mark before an object", in: "\xEF\xBB\xBF{\"a\":1}", want: `{"a":1}`},
		{name: "a mark alone", in: "\xEF\xBB\xBF", want: ""},
		// Python's utf-8-sig decodes one mark; the second is content.
		{name: "two marks keep the second", in: "\xEF\xBB\xBF\xEF\xBB\xBF{}", want: "\xEF\xBB\xBF{}"},
		{name: "no mark", in: `{"a":1}`, want: `{"a":1}`},
		{name: "a mark after whitespace is content", in: " \xEF\xBB\xBF{}", want: " \xEF\xBB\xBF{}"},
		{name: "a partial mark is content", in: "\xEF\xBB{}", want: "\xEF\xBB{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Body([]byte(tt.in)); string(got) != tt.want {
				t.Errorf("Body(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBody_NoMarkIsTheSameSlice(t *testing.T) {
	in := []byte(`{"a":1}`)
	if got := Body(in); len(got) != len(in) || &got[0] != &in[0] {
		t.Errorf("Body copied or changed a slice with no mark: %q", got)
	}
}

func TestEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "nothing", in: nil, want: true},
		{name: "RFC 8259 whitespace", in: []byte(" \t\n\r"), want: true},
		{name: "vertical tab is not JSON whitespace", in: []byte("\v"), want: false},
		{name: "form feed is not JSON whitespace", in: []byte("\f"), want: false},
		{name: "a document", in: []byte(" {} "), want: false},
		{name: "a mark is not whitespace", in: []byte("\xEF\xBB\xBF"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Empty(bytes.Clone(tt.in)); got != tt.want {
				t.Errorf("Empty(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
