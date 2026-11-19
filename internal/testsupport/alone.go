package testsupport

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport/testchild"
)

const aloneEnv = "MAROTTE_TEST_ALONE"

// Alone reports whether t's test owns its process, so the caller may change process-wide state such as GOMAXPROCS
// or read process-wide counters such as runtime.MemStats. Otherwise it runs the test again in a child process of
// the test binary under the same -run subtest pattern, -skip and -short, fails t with the child's output unless the
// child ran and passed it, and the caller returns. The child starts at once and t is parallel: sharing no process
// state, the child runs beside the package's serial tests as well as its parallel ones. Call it from a top-level
// test, after any skip.
func Alone(t *testing.T) bool {
	t.Helper()
	if os.Getenv(aloneEnv) == t.Name() {
		return true
	}
	run := childRun(t.Name(), flagValue("test.run"))
	args := []string{"-test.run=" + run, "-test.count=1", "-test.v=true", "-test.short=" + flagValue("test.short")}
	for _, name := range []string{"test.skip", "test.timeout"} {
		if v := flagValue(name); v != "" {
			args = append(args, "-"+name+"="+v)
		}
	}
	//nolint:gosec,nolintlint // G204, G702: argv[0] is the test binary itself and every flag is the parent's own
	child := exec.CommandContext(t.Context(), os.Args[0], args...)
	child.Env = testchild.Env(aloneEnv + "=" + t.Name())
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	err := child.Start()
	t.Parallel()
	if err == nil {
		err = child.Wait()
	}
	// A pattern that matched nothing exits 0 too.
	if err == nil && !bytes.Contains(out.Bytes(), []byte("--- PASS: "+t.Name()+" (")) {
		err = errors.New("the child did not pass the test")
	}
	if err != nil {
		t.Fatalf("%s in its own process: %v\n%s", t.Name(), err, out.Bytes())
	}
	if testing.Verbose() {
		t.Logf("%s in its own process:\n%s", t.Name(), out.Bytes())
	}
	return false
}

func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// childRun is the -run pattern that runs test name and the subtests run would run under it. It keeps each
// alternative of run whose first element matches name, with that element replaced by name anchored; one with no
// further element runs every subtest.
func childRun(name, run string) string {
	top := "^" + regexp.QuoteMeta(name) + "$"
	var kept []string
	for _, elems := range splitRun(run) {
		if ok, err := regexp.MatchString(elems[0], name); err != nil || !ok {
			continue
		}
		if len(elems) == 1 {
			return top
		}
		kept = append(kept, top+"/"+strings.Join(elems[1:], "/"))
	}
	if len(kept) == 0 {
		return top
	}
	return strings.Join(kept, "|")
}

// splitRun splits a -run pattern into alternatives of slash-separated elements, as testing's splitRegexp does
// (src/testing/match.go): at a '|' or '/' outside brackets and parentheses.
func splitRun(s string) [][]string {
	var alts [][]string
	var elems []string
	start := 0
	for _, i := range topSeparators(s) {
		elems = append(elems, s[start:i])
		start = i + 1
		if s[i] == '|' {
			alts, elems = append(alts, elems), nil
		}
	}
	return append(alts, append(elems, s[start:]))
}

// topSeparators is the index of every '/' and '|' in s outside brackets and parentheses.
func topSeparators(s string) []int {
	var at []int
	brackets, parens := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			brackets++
		case ']':
			brackets = max(brackets-1, 0)
		case '(':
			if brackets == 0 {
				parens++
			}
		case ')':
			if brackets == 0 {
				parens--
			}
		case '\\':
			i++
		case '/', '|':
			if brackets == 0 && parens == 0 {
				at = append(at, i)
			}
		}
	}
	return at
}
