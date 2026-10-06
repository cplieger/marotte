package steering

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWriteTools verifies writeTools never panics on arbitrary JSON and writes valid UTF-8.
func FuzzWriteTools(f *testing.F) {
	f.Add([]byte(`{"runtimes":{"node":{"version":"20.1"}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`invalid json`))
	f.Add([]byte(`{"npm":{"pkg":{"version":"1.0","binaries":["a","b"]}}}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		var b strings.Builder
		writeTools(&b, data)
		result := b.String()

		if !utf8.ValidString(result) {
			t.Fatalf("writeTools produced invalid UTF-8 for input %q", data)
		}

		// Non-empty output starts with the header.
		if result != "" && !strings.HasPrefix(result, "## Installed tools\n") {
			t.Fatalf("writeTools output doesn't start with expected header: %q", result[:min(50, len(result))])
		}
	})
}
