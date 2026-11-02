package nodeopts

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWithHeapLimit(t *testing.T) {
	cases := []struct {
		name      string
		inherited string
		mib       int
		want      string
	}{
		{"empty inherited value", "", 1024, "--max-old-space-size=1024"},
		{
			"unrelated options kept byte for byte", "--report-on-signal --report-directory=/tmp/r", 512,
			"--report-on-signal --report-directory=/tmp/r --max-old-space-size=512",
		},
		{
			"a quoted option keeps its quoting", `--title="a b" --trace-warnings`, 512,
			`--title="a b" --trace-warnings --max-old-space-size=512`,
		},
		{"an escaped quote survives", `--title="a\"b"`, 512, `--title="a\"b" --max-old-space-size=512`},
		{"the dashed flag is replaced", "--max-old-space-size=4096", 1024, "--max-old-space-size=1024"},
		{"the underscored flag is replaced", "--max_old_space_size=4096", 1024, "--max-old-space-size=1024"},
		{"the mixed spelling is replaced", "--max-old_space-size=4096 --x", 1024, "--x --max-old-space-size=1024"},
		{"the percentage flag is removed", "--max-old-space-size-percentage=10", 1024, "--max-old-space-size=1024"},
		{
			"the underscored percentage flag is removed", "--max_old_space_size_percentage=10 --x", 1024,
			"--x --max-old-space-size=1024",
		},
		{
			"duplicates are all removed", "--max-old-space-size=1 --a --max-old-space-size=2", 64,
			"--a --max-old-space-size=64",
		},
		{"runs of spaces collapse to one", "--a   --b ", 64, "--a --b --max-old-space-size=64"},
		{"a tab inside an unrelated token is kept", "--title=a\tb", 64, "--title=a\tb --max-old-space-size=64"},
		{
			"a flag that only shares the prefix is kept", "--max-old-space-sizes=3", 64,
			"--max-old-space-sizes=3 --max-old-space-size=64",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WithHeapLimit(tc.inherited, tc.mib)
			if err != nil {
				t.Fatalf("WithHeapLimit(%q, %d) error: %v", tc.inherited, tc.mib, err)
			}
			if got != tc.want {
				t.Errorf("WithHeapLimit(%q, %d) = %q, want %q", tc.inherited, tc.mib, got, tc.want)
			}
		})
	}
}

func TestWithHeapLimit_RefusesWhatNodeRefuses(t *testing.T) {
	cases := []struct {
		name      string
		inherited string
		want      error
	}{
		{"the separated form", "--max-old-space-size 512", errHeapFlagValue},
		{"a non-numeric value", "--max-old-space-size=big", errHeapFlagValue},
		{"an empty value", "--max-old-space-size=", errHeapFlagValue},
		{"a tab inside a heap value", "--max-old-space-size=512\t--title=x", errHeapFlagValue},
		{"an unterminated quote", `--title="a b`, errUnterminatedString},
		{"a trailing escape inside quotes", `--title="a\`, errInvalidEscape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WithHeapLimit(tc.inherited, 1024)
			if !errors.Is(err, tc.want) {
				t.Errorf("WithHeapLimit(%q) = %q, %v, want error %v", tc.inherited, got, err, tc.want)
			}
		})
	}
}

func TestWithHeapLimit_ZeroReturnsTheInputUnparsed(t *testing.T) {
	for _, in := range []string{"", "--max-old-space-size=8", `--title="unterminated`, "--max-old-space-size 1"} {
		got, err := WithHeapLimit(in, 0)
		if err != nil || got != in {
			t.Errorf("WithHeapLimit(%q, 0) = %q, %v, want the input unchanged", in, got, err)
		}
	}
}

// kasNode is the node binary kiro-cli unpacks for KAS, or "" when it is not installed here.
func kasNode(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".local", "share", "kiro-cli", "node")
	if st, err := os.Stat(p); err != nil || st.Mode()&0o111 == 0 {
		return ""
	}
	return p
}

// TestWithHeapLimit_NodeAgrees runs the KAS node with each composed value: the heap limit lands at mib plus V8's
// 48 MiB young-generation allowance, and an unrelated quoted option survives.
func TestWithHeapLimit_NodeAgrees(t *testing.T) {
	node := kasNode(t)
	if node == "" {
		t.Skip("the KAS node binary is not installed here")
	}
	const script = `console.log(JSON.stringify({limit: require("v8").getHeapStatistics().heap_size_limit, title: process.title}))`
	for _, in := range []string{"", "--max-old-space-size=8192", "--max_old_space_size_percentage=10", `--title="a b"`} {
		t.Run(in, func(t *testing.T) {
			composed, err := WithHeapLimit(in, 1024)
			if err != nil {
				t.Fatalf("WithHeapLimit: %v", err)
			}
			cmd := exec.CommandContext(t.Context(), node, "-e", script)
			cmd.Env = []string{"NODE_OPTIONS=" + composed}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("node with NODE_OPTIONS=%q: %v", composed, err)
			}
			var got struct {
				Limit int64  `json:"limit"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			if want := int64(1024+48) << 20; got.Limit != want {
				t.Errorf("heap_size_limit = %d MiB, want %d", got.Limit>>20, want>>20)
			}
			if in == `--title="a b"` && got.Title != "a b" {
				t.Errorf("process.title = %q, want %q", got.Title, "a b")
			}
		})
	}
}
