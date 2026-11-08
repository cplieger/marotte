package steering

import (
	"testing"
	"unicode/utf8"
)

// FuzzTruncateUTF8 verifies truncateUTF8 never splits a rune and keeps valid UTF-8 valid.
func FuzzTruncateUTF8(f *testing.F) {
	f.Add("hello", 3)
	f.Add("日本語テスト", 6)
	f.Add("", 0)
	f.Add("abc", 100)
	f.Add("🎉🎊🎈", 4)
	f.Add("\xff\xfe\xfd", 2)

	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 {
			return
		}
		result := truncateUTF8(s, n)

		if len(result) > n {
			t.Fatalf("truncateUTF8(%q, %d) = %q; len %d > %d", s, n, result, len(result), n)
		}

		if utf8.ValidString(s) && !utf8.ValidString(result) {
			t.Fatalf("truncateUTF8(%q, %d) = %q; invalid UTF-8 from valid input", s, n, result)
		}

		if len(result) > 0 && s[:len(result)] != result {
			t.Fatalf("truncateUTF8(%q, %d) = %q; not a byte prefix", s, n, result)
		}

		if len(s) <= n && result != s {
			t.Fatalf("truncateUTF8(%q, %d) = %q; should be unchanged", s, n, result)
		}
	})
}
