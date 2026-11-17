// Package testchild builds the environment of a child process that runs the test binary.
package testchild

import (
	"os"
	"strings"
)

// Env returns the process environment followed by extra, for a child that runs this test binary
// or a copy of it. Under -race it ends with a GORACE that keeps the inherited race options and
// adds atexit_sleep_ms=0: a race-built binary otherwise sleeps that long, 1000 ms by default, on
// every clean exit (https://go.dev/doc/articles/race_detector#Options). os/exec keeps the last
// value of a repeated variable, so neither extra nor anything appended later may set GORACE.
func Env(extra ...string) []string {
	env := append(os.Environ(), extra...)
	if raceEnabled {
		env = append(env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	}
	return env
}
