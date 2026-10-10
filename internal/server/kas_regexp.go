package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// errKASRuntimeMissing reports that the JavaScript runtime KAS runs on is not on disk yet.
var errKASRuntimeMissing = errors.New("KAS's JavaScript runtime is not installed")

// kasRegExpTimeout bounds one compile; node starts in about 30ms.
const kasRegExpTimeout = 10 * time.Second

// kasRegExpScript answers whether stdin compiles as `new RegExp(source)`. Anything other than
// a SyntaxError is a runtime fault, not a verdict, so it exits non-zero.
const kasRegExpScript = `let s;try{s=require("fs").readFileSync(0,"utf8")}catch{process.exit(2)}` +
	`try{new RegExp(s);process.stdout.write("valid")}` +
	`catch(e){if(!(e instanceof SyntaxError))throw e;process.stdout.write("invalid")}`

// kasRegExp compiles with the node binary KAS itself runs on, so a pattern is accepted exactly
// when KAS's `new RegExp` takes it, whatever that engine's version accepts.
type kasRegExp struct {
	node string
}

// compiles reports whether pattern is a valid JavaScript RegExp source. It returns
// errKASRuntimeMissing when the runtime is absent, and any other error when the runtime
// gave no verdict.
func (k kasRegExp) compiles(ctx context.Context, pattern string) (bool, error) {
	if k.node == "" {
		return false, errKASRuntimeMissing
	}
	if _, err := os.Stat(k.node); errors.Is(err, fs.ErrNotExist) {
		return false, errKASRuntimeMissing
	}
	ctx, cancel := context.WithTimeout(ctx, kasRegExpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, k.node, "-e", kasRegExpScript) //nolint:gosec // G204: the runtime path is resolved at startup, never request input; the pattern goes in on stdin
	// An empty environment keeps NODE_OPTIONS and friends from loading code into the check.
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(pattern)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("run %s: %w: %s", k.node, err, bytes.TrimSpace(stderr.Bytes()))
	}
	switch out.String() {
	case "valid":
		return true, nil
	case "invalid":
		return false, nil
	default:
		return false, fmt.Errorf("run %s: unexpected verdict %q", k.node, out.String())
	}
}

// kasAcceptsMatcher is KAS 2.28 `ARr`'s verdict on a hook matcher: it compiles as a RegExp, or,
// on a tool trigger, it is a `*`/`?` glob with none of `\^$()[]{}|+`.
func kasAcceptsMatcher(ctx context.Context, re kasRegExp, subject marotte.HookMatcherSubject, m string) (bool, error) {
	if subject == marotte.HookMatcherSubjectToolName && strings.ContainsAny(m, "*?") && !strings.ContainsAny(m, `\^$()[]{}|+`) {
		return true, nil
	}
	return re.compiles(ctx, m)
}
