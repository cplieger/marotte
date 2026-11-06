package kascap

import (
	"regexp"
	"strings"
	"testing"
)

// overflowListRe is KAS's list of model-service messages it types ContextWindowExceededError, anchored on one member.
var overflowListRe = regexp.MustCompile(`=\[([^\]]*"input content length exceeds threshold"[^\]]*)\]`)

// TestRequestSizeCapStillUntypedByKAS pins the premise of rpcerr's requestSizeCapped: KAS does not count the
// model service's "Request exceeds the maximum allowed size" refusal as a context overflow, and so types it
// GenericValidationError, a CLIENT_ERROR class that writes its own reader sentence.
func TestRequestSizeCapStillUntypedByKAS(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		lists := overflowListRe.FindAllStringSubmatch(b.src, -1)
		if len(lists) != 1 {
			t.Fatalf("found %d overflow message lists holding \"input content length exceeds threshold\", want exactly 1; re-anchor on KAS's hasKnownContextOverflowMessage", len(lists))
		}
		if strings.Contains(strings.ToLower(lists[0][1]), "maximum allowed size") {
			t.Errorf("KAS's overflow list %s now holds the request size cap: KAS types it ContextWindowExceededError, so delete rpcerr's requestSizeCapped match", lists[0][1])
		}
		var generic []jsClass
		for _, c := range jsClasses(b.src) {
			if c.name == "GenericValidationError" {
				generic = append(generic, c)
			}
		}
		if len(generic) != 1 {
			t.Fatalf("found %d GenericValidationError classes, want exactly 1", len(generic))
		}
		if h := generic[0].head; !strings.Contains(h, "userFacingSessionErrorMessage") || !strings.Contains(h, `"CLIENT_ERROR"`) {
			t.Errorf("GenericValidationError no longer writes a reader sentence as a CLIENT_ERROR; re-check rpcerr's requestSizeCapped premise")
		}
	})
}
