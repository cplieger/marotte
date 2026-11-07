// Package obs owns marotte's Prometheus metrics and the registry that serves
// them, with the exposition prefix "marotte_". The registry is unexported, so
// a caller can scrape the page but cannot register, rename or delete a series.
//
// Each counter is pre-set at zero for every value of its labels, so increase()
// sees the first event after a start. The values are the owning package's
// names; a test there pins them to the lists below.
package obs

import (
	"net/http"

	"github.com/cplieger/go/measure"
)

var registry = measure.NewRegistry("marotte")

// engineExitCauses are the names internal/agent's exitCause gives an engine's end.
var engineExitCauses = []string{
	"restart_confirmed", "restart_auto", "shutdown", "stopped", "stdin_stalled", "parse_breaker",
	"lane_overflow", "external_stop", "crash",
}

// promptFailureClasses are the names internal/command gives a failed prompt's class.
var promptFailureClasses = []string{"fatal", "pipe_death", "busy", "transient", "throttled", "rejected", "auth"}

// The reasons a sign-in save fails.
const (
	// SignInStagedCopyLeft is a save refused because an earlier save's staged copy is still on disk.
	SignInStagedCopyLeft = "staged_copy_left"
	// SignInWriteFailed is a save whose write to the sign-in file failed.
	SignInWriteFailed = "write_failed"
)

var signInSaveReasons = []string{SignInStagedCopyLeft, SignInWriteFailed}

var (
	engineExits = measure.NewLabeledCounter("engine_exits_total",
		"Agent engine exits, by the cause the exit line names.", []string{"cause"})
	promptFailures = measure.NewLabeledCounter("prompt_failures_total",
		"Failed prompts, failed empty-turn retries included, by failure class; "+
			"pipe_death is a prompt the engine's own exit ended.", []string{"class"})
	signInSaveFailures = measure.NewLabeledCounter("signin_save_failures_total",
		"Sign-in changes the sign-in file could not take, by backend and reason.", []string{"backend", "reason"})
)

func init() {
	registry.MustRegister(engineExits, promptFailures, signInSaveFailures)
	for _, c := range engineExitCauses {
		engineExits.Add(0, c)
	}
	for _, c := range promptFailureClasses {
		promptFailures.Add(0, c)
	}
}

// Handler serves the Prometheus text exposition: every marotte series plus the
// Go and process families measure adds to every registry. It is the page's
// only writer, because a second writer of those families repeats their TYPE
// lines and Prometheus then rejects the whole scrape.
func Handler() http.Handler {
	return registry.Handler()
}

// EngineExited counts one engine exit under cause, one of engineExitCauses.
func EngineExited(cause string) { engineExits.Inc(cause) }

// PromptFailed counts one failed prompt under class, one of promptFailureClasses.
func PromptFailed(class string) { promptFailures.Inc(class) }

// SignInBackend pre-sets the sign-in save failure series of a backend at zero, for every reason.
func SignInBackend(backend string) {
	for _, r := range signInSaveReasons {
		signInSaveFailures.Add(0, backend, r)
	}
}

// SignInSaveFailed counts one failed sign-in save of backend under reason, SignInStagedCopyLeft or
// SignInWriteFailed.
func SignInSaveFailed(backend, reason string) { signInSaveFailures.Inc(backend, reason) }
