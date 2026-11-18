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
// the test binary, beside the package's other parallel tests, fails t with the child's output unless the child ran
// and passed it, and the caller returns. Call it from a top-level test, after any skip.
func Alone(t *testing.T) bool {
	t.Helper()
	if os.Getenv(aloneEnv) == t.Name() {
		return true
	}
	t.Parallel()
	args := []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$", "-test.count=1", "-test.v=true"}
	if f := flag.Lookup("test.timeout"); f != nil {
		args = append(args, "-test.timeout="+f.Value.String())
	}
	//nolint:gosec,nolintlint // G204, G702: argv[0] is the test binary itself and the pattern is t.Name() quoted
	child := exec.CommandContext(t.Context(), os.Args[0], args...)
	child.Env = testchild.Env(aloneEnv + "=" + t.Name())
	out, err := child.CombinedOutput()
	// A pattern that matched nothing exits 0 too.
	if err == nil && !bytes.Contains(out, []byte("--- PASS: "+t.Name()+" (")) {
		err = errors.New("the child did not pass the test")
	}
	if err != nil {
		t.Fatalf("%s in its own process: %v\n%s", t.Name(), err, out)
	}
	if testing.Verbose() {
		t.Logf("%s in its own process:\n%s", t.Name(), out)
	}
	return false
}
