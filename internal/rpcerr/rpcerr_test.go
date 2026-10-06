package rpcerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// rpcErr builds the error shape a KAS failure arrives as.
func rpcErr(message, data string) error {
	e := &marotte.RPCError{Code: -32603, Message: message}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

// TestDetails pins that `error.data` is read at all: a -32603 puts its cause there and sets
// message to the literal "Internal error".
func TestDetails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"an error carrying no data", rpcErr("boom", ""), ""},
		{"a non-RPC error", errors.New("boom"), ""},
		{"the details shape", rpcErr("Internal error", `{"details":"the real cause"}`), "the real cause"},
		{
			// A Zod issue array: its messages are what a caller wants.
			"a Zod issue array",
			rpcErr("Internal error", `[{"message":"workspacePaths is required","path":["workspacePaths"]},{"message":"inputs must be an object","path":["inputs"]}]`),
			"workspacePaths is required; inputs must be an object",
		},
		{
			"an unrecognised shape falls back to the raw JSON",
			rpcErr("Internal error", `{"weird":true}`),
			`{"weird":true}`,
		},
		{"an empty details string falls through rather than winning", rpcErr("Internal error", `{"details":""}`), `{"details":""}`},
		{
			// A Zod array with no message text: the raw JSON still beats "".
			"a Zod issue array with no messages falls back to the raw JSON",
			rpcErr("Internal error", `[{"path":["workspacePaths"]},{"message":""}]`),
			`[{"path":["workspacePaths"]},{"message":""}]`,
		},
		{"a typed-error envelope carries no text", rpcErr("Your network proxy refused the connection.", `{"errorType":"ProxyAuthenticationRequiredError","retryErrorType":"TRANSIENT","requestId":"r"}`), ""},
		{"an envelope with extra members carries no text", rpcErr("m", `{"errorType":"SessionOwnsLiveWorkflowsError","sessionId":"s","workflowIds":["w"]}`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Details(c.err); got != c.want {
				t.Errorf("Details() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestText pins the composition: data wins when present, and the error string is the
// fallback rather than an empty result.
func TestText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is empty rather than a literal", nil, ""},
		{
			"data wins over a boilerplate message",
			rpcErr("Internal error", `{"details":"The monthly usage limit has been reached"}`),
			"The monthly usage limit has been reached",
		},
		{
			// The case where Details alone answers "".
			"a message-only error falls back to the message",
			rpcErr("workspacePaths is not iterable", ""),
			"workspacePaths is not iterable",
		},
		{"an ordinary Go error passes through", errors.New("boom"), "boom"},
		{
			"a typed error renders its message, not the envelope",
			fmt.Errorf("ACP error -32000: %w", rpcErr("Your network proxy refused the connection.", `{"errorType":"ProxyAuthenticationRequiredError","retryErrorType":"TRANSIENT","requestId":"r"}`)),
			"Your network proxy refused the connection.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Text(c.err); got != c.want {
				t.Errorf("Text() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestTextBoundsAndSanitizes pins the cap (covering Details' unbounded raw-JSON fallback)
// and the control-character stripping of provider text.
func TestTextBoundsAndSanitizes(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", maxTextBytes*3)
	got := Text(rpcErr("Internal error", `{"details":"`+huge+`"}`))
	if len(got) > maxTextBytes {
		t.Errorf("Text() returned %d bytes, want <= %d", len(got), maxTextBytes)
	}

	// A newline would forge a second log record and U+202E reorders a line; neither survives.
	dirty := rpcErr("Internal error", `{"details":"first\nSECOND\u202ereordered"}`)
	got = Text(dirty)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Text() = %q, want no newline or carriage return", got)
	}
	if strings.Contains(got, "\u202e") {
		t.Errorf("Text() = %q, want the Bidi override removed", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "SECOND") {
		t.Errorf("Text() = %q, want the real words preserved", got)
	}
}

// FuzzDetails pins that unwrapping an arbitrary `error.data` cannot panic and
// never invents content. Data is untrusted input from another process, and this
// is the only function that interprets it.
func FuzzDetails(f *testing.F) {
	f.Add(`{"details":"x"}`)
	f.Add(`[{"message":"a"}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`"a string"`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`{"details":123}`)
	f.Fuzz(func(t *testing.T, data string) {
		got := Details(rpcErr("Internal error", data))
		if data == "" && got != "" {
			t.Fatalf("Details() = %q for absent data, want \"\"", got)
		}
		if len(got) > len(data)+2*len(data) {
			t.Fatalf("Details() grew %d bytes of data into %d", len(data), len(got))
		}
	})
}

// FuzzText pins the bound on the value that reaches a user surface; Text owns the cap.
func FuzzText(f *testing.F) {
	f.Add(`{"details":"x"}`, "Internal error")
	f.Add(`[{"message":"a"}]`, "Internal error")
	f.Add(``, "workspacePaths is not iterable")
	f.Add(`{"details":"\u202e\n\u0000"}`, "Internal error")
	f.Fuzz(func(t *testing.T, data, message string) {
		got := Text(rpcErr(message, data))
		if len(got) > maxTextBytes {
			t.Fatalf("Text() = %d bytes, want <= %d", len(got), maxTextBytes)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("Text() = %q, want no line break", got)
		}
	})
}

func TestMappedOf(t *testing.T) {
	t.Parallel()
	env := `{"errorType":"ProxyAuthenticationRequiredError","retryErrorType":"TRANSIENT","requestId":"req-407"}`
	want := Mapped{ErrorType: "ProxyAuthenticationRequiredError", RetryErrorType: "TRANSIENT", RequestID: "req-407"}
	if got, ok := MappedOf(fmt.Errorf("a: %w", fmt.Errorf("b: %w", rpcErr("m", env)))); !ok || got != want {
		t.Errorf("MappedOf(envelope wrapped twice) = (%+v, %v), want (%+v, true)", got, ok, want)
	}
	for name, err := range map[string]error{
		"details shape":     rpcErr("m", `{"details":"x"}`),
		"no data":           rpcErr("m", ""),
		"unrelated object":  rpcErr("m", `{"weird":true}`),
		"a non-RPC error":   errors.New("boom"),
		"a Zod issue array": rpcErr("m", `[{"message":"x"}]`),
	} {
		if got, ok := MappedOf(err); ok {
			t.Errorf("MappedOf(%s) = (%+v, true), want not found", name, got)
		}
	}
}

func TestAccount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		m       Mapped
		message string
		want    string
	}{
		{
			"a context overflow names the compact remedy",
			Mapped{ErrorType: ContextWindowExceededError, RequestID: "r1"},
			"Context limit exceeded unexpectedly. Please try again.",
			"This chat reached the model's context limit. Compact the context, then send the prompt again. (request r1)",
		},
		{
			"a throttle says kiro-cli already retried",
			Mapped{ErrorType: "ClientThrottleError", RetryErrorType: RetryThrottling},
			"Too many requests.",
			"Too many requests. kiro-cli already retried. Wait a moment and send again, or switch to another model.",
		},
		{
			"an unreachable model registry names the login remedy",
			Mapped{ErrorType: "ModelRegistryUnavailableError", RetryErrorType: "SERVER_ERROR"},
			"Kiro could not load the available models.",
			"Kiro could not load the available models. Run `kiro-cli login`, then send the prompt again.",
		},
		{
			"a transient class keeps KAS's prose alone",
			Mapped{ErrorType: "ProxyAuthenticationRequiredError", RetryErrorType: "TRANSIENT", RequestID: "req-407"},
			"Your network proxy refused the connection.",
			"Your network proxy refused the connection. (request req-407)",
		},
		{
			"a minified class name gets the message alone",
			Mapped{ErrorType: "ge"},
			"Your connection was interrupted.",
			"Your connection was interrupted.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Account(c.m, c.message); got != c.want {
				t.Errorf("Account(%+v, %q) = %q, want %q", c.m, c.message, got, c.want)
			}
		})
	}
}
