package testsupport

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"regexp"
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
	run := "^" + regexp.QuoteMeta(t.Name()) + "$"
	if sub, ok := subtestPattern(flagValue("test.run")); ok {
		run += "/" + sub
	}
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

// subtestPattern is what follows the top-level element of a -run pattern, split as testing's splitRegexp splits it:
// at the first slash outside brackets and parentheses. A top-level alternation forwards nothing, so the child runs
// every subtest rather than another test or a wrong subset.
func subtestPattern(run string) (string, bool) {
	brackets, parens, slash := 0, 0, -1
	for i := 0; i < len(run); i++ {
		top := brackets == 0 && parens == 0
		switch c := run[i]; {
		case c == '\\':
			i++
		case c == '[':
			brackets++
		case c == ']':
			brackets = max(brackets-1, 0)
		case brackets > 0:
		case c == '(':
			parens++
		case c == ')':
			parens--
		case c == '|' && top:
			return "", false
		case c == '/' && top && slash < 0:
			slash = i
		}
	}
	if slash < 0 {
		return "", false
	}
	return run[slash+1:], true
}
