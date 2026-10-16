package nodeopts

import (
	"strconv"
	"strings"
	"testing"
)

// FuzzWithHeapLimit: never panics, and a success lexes cleanly and carries exactly one heap token, the last one.
func FuzzWithHeapLimit(f *testing.F) {
	for _, seed := range []string{
		"", "--a", `--title="a b"`, `"\"`, `--x="a\"b"`, "--max_old_space_size=1 --max-old-space-size=2",
		"--max-old-space-size-percentage=5", "a\tb", `"`, `\`, "  ",
	} {
		f.Add(seed, 512)
	}
	f.Fuzz(func(t *testing.T, inherited string, mib int) {
		if mib <= 0 {
			mib = -mib%65536 + 1
		}
		out, err := WithHeapLimit(inherited, mib)
		if err != nil {
			return
		}
		tokens, err := lex(out)
		if err != nil {
			t.Fatalf("composed %q does not lex: %v", out, err)
		}
		heaps := 0
		for i, tk := range tokens {
			heap, herr := isHeapToken(tk.text)
			if herr != nil {
				t.Fatalf("composed %q carries a malformed heap token %q", out, tk.text)
			}
			if heap {
				heaps++
				if i != len(tokens)-1 {
					t.Fatalf("composed %q has a heap token before the end", out)
				}
			}
		}
		if heaps != 1 || !strings.HasSuffix(out, "--max-old-space-size="+strconv.Itoa(mib)) {
			t.Fatalf("composed %q: %d heap tokens, want exactly the appended one", out, heaps)
		}
	})
}
