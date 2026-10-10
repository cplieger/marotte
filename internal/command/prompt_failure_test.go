package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

func rpcErr(t *testing.T, code int, msg string, data any) error {
	t.Helper()
	e := &marotte.RPCError{Code: code, Message: msg}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal data: %v", err)
		}
		e.Data = raw
	}
	// The bridge wraps every RPC error, so the classifier must reach through %w.
	return fmt.Errorf("ACP error %d: %w", code, e)
}

// TestClassifyPromptFailure pins the four causes apart. Each row exists because
// the single-boolean predicate this replaced took the WRONG action on it.
func TestClassifyPromptFailure(t *testing.T) {
	cases := map[string]struct {
		err  error
		want promptFailureClass
	}{
		// A dead subprocess arrives in a TransportError whose Retryable is true; the identity check
		// must win.
		"dead bridge is not retryable": {
			err:  &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true},
			want: classPipeDeath,
		},
		"dead bridge survives wrapping": {
			err:  fmt.Errorf("prompt: %w", &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}),
			want: classPipeDeath,
		},
		"write failure is transient": {
			err:  &marotte.TransportError{Err: errors.New("write to ACP: broken pipe"), Retryable: true},
			want: classTransient,
		},
		"non-retryable transport is fatal": {
			err:  &marotte.TransportError{Err: errors.New("bad frame"), Retryable: false},
			want: classFatal,
		},
		"session busy by sentinel": {
			err:  fmt.Errorf("ACP error -32001: %w", marotte.ErrNotIdle),
			want: classBusy,
		},
		"session busy by code": {
			err:  rpcErr(t, marotte.RPCCodeNotIdle, "session is not idle", nil),
			want: classBusy,
		},
		"an answered internal error is final": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": "transient upstream fault"}),
			want: classFatal,
		},
		"auth failure is auth, not transient": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": "not logged in"}),
			want: classAuth,
		},
		"expired token is auth": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "ExpiredToken: refresh required", nil),
			want: classAuth,
		},
		"invalid token names its class": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": `{"errorType":"TokenInvalidError"}`,
			}),
			want: classAuth,
		},
		"expired token names its class": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": `{"errorType":"TokenExpiredError"}`,
			}),
			want: classAuth,
		},
		"a failed auth refresh names its class": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": `{"errorType":"AuthRefreshFailedError"}`,
			}),
			want: classAuth,
		},
		"the signed-out model registry names its class": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": `{"errorType":"ModelRegistryUnauthenticatedError"}`,
			}),
			want: classAuth,
		},
		"a bare name code is enough": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "INVALID_SSO_AUTH",
			}),
			want: classAuth,
		},
		"the sign-in-again sentence is auth": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			want: classAuth,
		},
		"a token sentence is auth": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "Authentication token has expired", nil),
			want: classAuth,
		},
		// An AWS SDK message saying a refresh WILL be attempted: grading it terminal-auth
		// suppressed the retry.
		"an SDK credential-refresh notice is not auth": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "a credential service availability issue. A refresh of these credentials will be attempted after Tue Jan 01 2030",
			}),
			want: classFatal,
		},
		"an echoed credentials header is not auth": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "access-control-allow-credentials: true",
			}),
			want: classFatal,
		},
		"throttle is not retryable": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "Too many requests, please wait.", rpcerr.Mapped{
				ErrorType:      "ClientThrottleError",
				RetryErrorType: "THROTTLING",
				RequestID:      "abc-123",
			}),
			want: classThrottled,
		},
		// A mapped error is classified by its fields, never by the mere presence of the data block,
		// which every mapped error carries.
		"validation error is not a throttle": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "The request was invalid.", rpcerr.Mapped{
				ErrorType:      "GenericValidationError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"access denied is auth, and is not a throttle": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "Access denied.", rpcerr.Mapped{
				ErrorType:      "AccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classAuth,
		},
		"a mapped auth class is auth": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "Authentication failed. Please sign in again.", rpcerr.Mapped{
				ErrorType:      "TokenExpiredError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classAuth,
		},
		// ModelRegistryAccessDeniedError is left out: no sign-in fixes missing model access.
		"an entitlement refusal is not auth": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "…this account does not have access to them.", rpcerr.Mapped{
				ErrorType:      "ModelRegistryAccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"a throttle outranks auth prose": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "Authentication token refresh throttled.", rpcerr.Mapped{
				ErrorType:      "ClientThrottleError",
				RetryErrorType: "THROTTLING",
			}),
			want: classThrottled,
		},
		"server error is not a throttle": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "The service failed.", rpcerr.Mapped{
				ErrorType:      "InternalServerError",
				RetryErrorType: "SERVER_ERROR",
			}),
			want: classFatal,
		},
		"mapped error with no retry classification is fatal": {
			err:  rpcErr(t, marotte.RPCCodeBridgeExited, "some other mapped failure", nil),
			want: classFatal,
		},
		"nil is fatal":         {err: nil, want: classFatal},
		"plain error is fatal": {err: errors.New("something else"), want: classFatal},
		// A validation refusal is the one -32603 that must not be retried: the request is what was
		// refused.
		"oversized image is rejected": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "ImageSizeExceeded: image exceeds 5 MB maximum: 6714372 bytes > 5242880",
			}),
			want: classRejected,
		},
		"oversized prompt is rejected": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			want: classRejected,
		},
		"unsupported document type is rejected": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "DisallowedFileType",
			}),
			want: classRejected,
		},
		"validation name in the message is rejected": {
			err:  rpcErr(t, marotte.RPCCodeInternal, "ImageDimensionExceeded", nil),
			want: classRejected,
		},
		"unclassified internal error is final": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "upstream connection reset",
			}),
			want: classFatal,
		},
		"a spent monthly allowance is not a validation refusal": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "MonthlyRequestCount limit reached",
			}),
			want: classFatal,
		},
		"a proxy 407 is final, not retried": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "Your network proxy refused the connection because it requires authentication (HTTP 407).", rpcerr.Mapped{
				ErrorType: "ProxyAuthenticationRequiredError", RetryErrorType: "TRANSIENT", RequestID: "req-407",
			}),
			want: classFatal,
		},
		"a session being closed is final": {
			err:  rpcErr(t, -32002, "A close is in progress for session sess_x", nil),
			want: classFatal,
		},
		"a session owning live runs is final": {
			err: rpcErr(t, marotte.RPCCodeBridgeExited, "This session owns live workflows.", map[string]any{
				"errorType": "SessionOwnsLiveWorkflowsError", "sessionId": "sess_x", "workflowIds": []string{"wf_1"},
			}),
			want: classFatal,
		},
		"a typed error on -32603 is graded by its envelope": {
			err: rpcErr(t, marotte.RPCCodeInternal, "Too many requests.", rpcerr.Mapped{
				ErrorType: "ClientThrottleError", RetryErrorType: "THROTTLING",
			}),
			want: classThrottled,
		},
		// Untyped errors fail closed even when their text looks retryable.
		"untyped not-idle text is fatal":   {err: errors.New("agent is not idle right now"), want: classFatal},
		"untyped internal text is fatal":   {err: errors.New("Internal error"), want: classFatal},
		"untyped validation text is fatal": {err: errors.New("ImageSizeExceeded"), want: classFatal},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyPromptFailure(tc.err)
			if got != tc.want {
				t.Errorf("classifyPromptFailure(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// retriesFor mirrors callPromptWithRetry's decision so the policy is asserted
// through the same expression the retry loop evaluates. A helper rather than a
// production predicate: the previous pass kept an IsRetryablePromptError wrapper
// for readability, and punused correctly called it out as used in tests only.
func retriesFor(err error) bool {
	class := classifyPromptFailure(err)
	return class == classBusy || class == classTransient
}

// TestRetryPolicy_NeverRetriesADeadBridgeOrAThrottle — a corpse cannot answer, and
// KAS's own client already spent five adaptive
// attempts on the throttle before handing it over, so a sixth only deepens it.
func TestRetryPolicy_NeverRetriesADeadBridgeOrAThrottle(t *testing.T) {
	dead := &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}
	if retriesFor(dead) {
		t.Error("a dead bridge is reported retryable; every attempt fails instantly against a closed done channel")
	}

	throttle := rpcErr(t, marotte.RPCCodeBridgeExited, "Too many requests.", rpcerr.Mapped{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
	})
	if retriesFor(throttle) {
		t.Error("a throttle is reported retryable; kiro-cli already exhausted its own adaptive attempts")
	}

	if !retriesFor(fmt.Errorf("ACP error -32001: %w", marotte.ErrNotIdle)) {
		t.Error("a busy session must still be retried")
	}
	if !retriesFor(&marotte.TransportError{Err: errors.New("write to ACP"), Retryable: true}) {
		t.Error("a transient write failure must still be retried")
	}

	if retriesFor(rpcErr(t, marotte.RPCCodeInternal, "Authentication failed. Please sign in again.", nil)) {
		t.Error("an auth failure is reported retryable; no number of attempts fixes a token the backend rejected")
	}
}

// TestPromptFailureReason_NamesAThrottle pins the user-facing half: a throttled
// turn's reason names the cause and the remedy, the one failure where both are known.
func TestPromptFailureReason_NamesAThrottle(t *testing.T) {
	const kasMsg = "Too many requests, please wait before trying again."
	err := rpcErr(t, marotte.RPCCodeBridgeExited, kasMsg, rpcerr.Mapped{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
		RequestID:      "req-9",
	})
	got := promptFailureReason(err, false)

	if !strings.Contains(got, kasMsg) {
		t.Errorf("reason %q dropped KAS's own user-facing message", got)
	}
	for _, want := range []string{"already retried", "switch to another model", "req-9"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not mention %q", got, want)
		}
	}

	other := rpcErr(t, marotte.RPCCodeBridgeExited, "The request was invalid.", rpcerr.Mapped{
		ErrorType:      "GenericValidationError",
		RetryErrorType: "CLIENT_ERROR",
	})
	if reason := promptFailureReason(other, false); strings.Contains(reason, "already retried") {
		t.Errorf("a validation error was given throttle advice: %q", reason)
	}

	blank := rpcErr(t, marotte.RPCCodeBridgeExited, "", rpcerr.Mapped{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
		RequestID:      "req-11",
	})
	got = promptFailureReason(blank, false)
	for _, leak := range []string{"{", "retryErrorType", "\"requestId\""} {
		if strings.Contains(got, leak) {
			t.Errorf("empty-message mapped error leaked the raw triplet (%q) at the user: %q", leak, got)
		}
	}
	if !strings.Contains(got, "ClientThrottleError") {
		t.Errorf("reason %q does not name the errorType, the one readable token in the triplet", got)
	}
	if !strings.Contains(got, "req-11") {
		t.Errorf("reason %q dropped the request id", got)
	}

	plain := errors.New("some other failure")
	if got := promptFailureReason(plain, false); got != plain.Error() {
		t.Errorf("promptFailureReason(%v) = %q, want it passed through unchanged", plain, got)
	}
}

func TestPromptFailureReason_ModelRegistryUnavailableNamesLoginRemedy(t *testing.T) {
	err := rpcErr(t, marotte.RPCCodeBridgeExited, "Kiro could not load the available models.", rpcerr.Mapped{
		ErrorType:      "ModelRegistryUnavailableError",
		RetryErrorType: "SERVER_ERROR",
	})
	got := promptFailureReason(err, false)
	for _, want := range []string{"Kiro could not load the available models.", "kiro-cli login"} {
		if !strings.Contains(got, want) {
			t.Errorf("promptFailureReason(ModelRegistryUnavailableError) = %q, want it to mention %q", got, want)
		}
	}
	if retriesFor(err) {
		t.Error("ModelRegistryUnavailableError is retryable, want a terminal login remedy")
	}

	other := rpcErr(t, marotte.RPCCodeBridgeExited, "The service failed.", rpcerr.Mapped{
		ErrorType:      "InternalServerError",
		RetryErrorType: "SERVER_ERROR",
	})
	if got := promptFailureReason(other, false); strings.Contains(got, "kiro-cli login") {
		t.Errorf("promptFailureReason(InternalServerError) = %q, want no login remedy", got)
	}
}

// KAS's own MCP wait fails a turn whose required server is still connecting past
// its timeout. A resend waits the same budget again, so it must not be retried, and
// KAS's sentence names the server, so it reaches the reader verbatim.
func TestClassifyRPCFailure_McpServersNotReadyIsFatalWithKASProse(t *testing.T) {
	const prose = "Required MCP server github did not become ready in time. Check the MCP Servers panel, then try again."
	err := rpcErr(t, marotte.RPCCodeBridgeExited, prose, rpcerr.Mapped{
		ErrorType:      "McpServersNotReadyError",
		RetryErrorType: "SERVER_ERROR",
	})
	if got := classifyPromptFailure(err); got != classFatal {
		t.Errorf("classifyPromptFailure(McpServersNotReadyError) = %v, want %v", got, classFatal)
	}
	if got := promptFailureReason(err, false); !strings.Contains(got, prose) {
		t.Errorf("promptFailureReason(McpServersNotReadyError) = %q, want KAS's sentence naming the server", got)
	}
}

// TestPromptFailureReason_NamesTheRefusalAsTerminal guards what the user is told about a validation
// refusal.
func TestPromptFailureReason_NamesTheRefusalAsTerminal(t *testing.T) {
	const cause = "ImageSizeExceeded: image exceeds 5 MB maximum: 6714372 bytes > 5242880"
	err := rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": cause})
	got := promptFailureReason(err, true)

	if !strings.Contains(got, cause) {
		t.Errorf("reason %q dropped the backend's own account of the refusal", got)
	}
	for _, want := range []string{"refused as sent", "smaller"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not tell the user the refusal is terminal (missing %q)", got, want)
		}
	}

	other := rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
		"details": "upstream connection reset",
	})
	if reason := promptFailureReason(other, false); strings.Contains(reason, "refused as sent") {
		t.Errorf("a transient internal error was told its request was refused: %q", reason)
	}
}

// KAS normally fills both machine fields, but the payload is decoded from the
// wire and a mapped error carrying only ONE of them is still a mapped error.
// Treating a half-filled triplet as "no payload at all" loses the throttle
// class on one side and the only readable token on the other, so each field is
// enough on its own.
func TestPromptFailureReason_HandlesAHalfFilledTriplet(t *testing.T) {
	t.Run("retryErrorType alone still classifies the throttle", func(t *testing.T) {
		err := rpcErr(t, marotte.RPCCodeBridgeExited, "Too many requests.", rpcerr.Mapped{
			RetryErrorType: "THROTTLING",
		})
		if got := classifyPromptFailure(err); got != classThrottled {
			t.Errorf("classifyPromptFailure(retryErrorType only) = %s, want %s", got, classThrottled)
		}
		if got := promptFailureReason(err, false); !strings.Contains(got, "already retried") {
			t.Errorf("reason %q does not carry the throttle advice", got)
		}
	})

	t.Run("errorType alone still names the failure", func(t *testing.T) {
		err := rpcErr(t, marotte.RPCCodeBridgeExited, "", rpcerr.Mapped{
			ErrorType: "ImprovementServiceUnavailable",
		})
		got := promptFailureReason(err, false)
		if !strings.Contains(got, "ImprovementServiceUnavailable") {
			t.Errorf("reason %q does not name the errorType, the only readable token it has", got)
		}
		if strings.Contains(got, "{") || strings.Contains(got, "errorType") {
			t.Errorf("reason %q leaked the raw triplet at the user", got)
		}
	})
}

func TestPromptFailureReason_HistoryShapedRefusalNamesRewind(t *testing.T) {
	const cause = "ImageCountExceeded: too many images"
	err := rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": cause})

	got := promptFailureReason(err, false)
	for _, want := range []string{"Rewind", "Reopening the chat does not clear it", "file or MCP tool"} {
		if !strings.Contains(got, want) {
			t.Errorf("promptFailureReason history refusal = %q, want it to mention %q", got, want)
		}
	}
}

func TestPromptFailureReason_PromptShapedRefusalKeepsSmallerAdvice(t *testing.T) {
	const cause = "ImageSizeExceeded: image exceeds the maximum"
	err := rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{"details": cause})

	got := promptFailureReason(err, true)
	if !strings.Contains(got, "Make the prompt or its attachments smaller") {
		t.Errorf("promptFailureReason current-prompt refusal = %q, want smaller-attachment advice", got)
	}
}

// TestContextWindowExceededIsPermanentAndNamesRecovery drives the shape KAS
// actually sends with its auto-compaction off: -32000, the mapped triplet, and a
// message naming a setting the reader has no control for.
func TestContextWindowExceededIsPermanentAndNamesRecovery(t *testing.T) {
	err := rpcErr(t, marotte.RPCCodeBridgeExited,
		"Context limit reached because the disableAutoCompaction setting is enabled. Compact the conversation manually, then try again. (Request ID: req-1)",
		rpcerr.Mapped{
			ErrorType:      "ContextWindowExceededError",
			RetryErrorType: "CLIENT_ERROR",
			RequestID:      "req-1",
		})

	if got := classifyPromptFailure(err); got != classFatal {
		t.Errorf("classifyPromptFailure(ContextWindowExceededError) = %s, want %s", got, classFatal)
	}
	if retriesFor(err) {
		t.Error("ContextWindowExceededError is retryable, want a permanent failure")
	}
	stop, got, kind := promptFailureAccount(t.Context(), err, false)
	if stop != marotte.StopReasonInterrupted || kind != marotte.FailureKindContextLimit {
		t.Errorf("promptFailureAccount(ContextWindowExceededError) stop, kind = %q, %q; want %q, %q",
			stop, kind, marotte.StopReasonInterrupted, marotte.FailureKindContextLimit)
	}
	if !strings.Contains(got, "Compact the context") {
		t.Errorf("reason = %q, want it to name %q", got, "Compact the context")
	}
	if strings.Contains(got, "disableAutoCompaction") {
		t.Errorf("reason = %q, want KAS's setting name kept out of it", got)
	}
	if n := strings.Count(got, "req-1"); n != 1 {
		t.Errorf("reason = %q names req-1 %d times, want once", got, n)
	}
}

// TestFailureKind_OnlyTheContextOverflowIsClassified pins that the predicate
// keys on the error TYPE: a sibling -32000 class carries no kind.
func TestFailureKind_OnlyTheContextOverflowIsClassified(t *testing.T) {
	err := rpcErr(t, marotte.RPCCodeBridgeExited, "Kiro could not load the available models.", rpcerr.Mapped{
		ErrorType:      "ModelRegistryUnavailableError",
		RetryErrorType: "SERVER_ERROR",
	})
	if _, _, kind := promptFailureAccount(t.Context(), err, false); kind != "" {
		t.Errorf("promptFailureAccount(ModelRegistryUnavailableError) kind = %q, want none", kind)
	}
}

// TestPromptFailureReason_SanitizesUpstreamText: a mapped error's reason is composed from upstream
// text, so it is sanitized.
func TestPromptFailureReason_SanitizesUpstreamText(t *testing.T) {
	const bidi = "\u202e"
	const c1 = "\u0085"

	err := rpcErr(t, marotte.RPCCodeBridgeExited, "Agent 'a"+bidi+"b' pins model 'm"+c1+"n'.", rpcerr.Mapped{
		ErrorType:      "AgentModelPinUnservableError",
		RetryErrorType: "CLIENT_ERROR",
		RequestID:      "req" + bidi + "-3",
	})
	got := promptFailureReason(err, false)

	for _, unsafe := range []string{bidi, c1} {
		if strings.Contains(got, unsafe) {
			t.Errorf("reason %q carries the unsanitized rune %q", got, unsafe)
		}
	}
	if !strings.Contains(got, "pins model") {
		t.Errorf("reason %q lost KAS's own user-facing text", got)
	}

	blank := rpcErr(t, marotte.RPCCodeBridgeExited, "", rpcerr.Mapped{
		ErrorType:      "Bad" + bidi + "Error",
		RetryErrorType: "CLIENT_ERROR",
	})
	if reason := promptFailureReason(blank, false); strings.Contains(reason, bidi) {
		t.Errorf("errorType reached the reader unsanitized: %q", reason)
	}

	login := rpcErr(t, marotte.RPCCodeBridgeExited, "Kiro could not"+bidi+" load models.", rpcerr.Mapped{
		ErrorType: "ModelRegistryUnavailableError",
	})
	reason := promptFailureReason(login, false)
	if strings.Contains(reason, bidi) {
		t.Errorf("the login-remedy branch passed its message through raw: %q", reason)
	}
	if !strings.Contains(reason, "kiro-cli login") {
		t.Errorf("the login-remedy branch lost its remedy: %q", reason)
	}
}

// TestPromptFailureReason_BoundsUpstreamProseAndKeepsTheRemedy: the reason is
// bounded per FIELD rather than over the composition, because this error's own
// message joins every served model id into one line. A single bound applied at
// the end would spend the whole budget on that list and cut off the two halves
// a reader can act on — the remedy sentence and the request id.
func TestPromptFailureReason_BoundsUpstreamProseAndKeepsTheRemedy(t *testing.T) {
	huge := strings.Repeat("model-id-that-is-not-served, ", 4000)
	err := rpcErr(t, marotte.RPCCodeBridgeExited, huge, rpcerr.Mapped{
		ErrorType:      "AgentModelPinUnservableError",
		RetryErrorType: "THROTTLING",
		RequestID:      "req-42",
	})
	got := promptFailureReason(err, false)

	const proseCap, idCap = 1024, 128
	if len(got) > proseCap+idCap+512 {
		t.Errorf("reason is %d bytes, want the per-field bounds to hold it near %d", len(got), proseCap)
	}
	for _, want := range []string{"already retried", "req-42"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q dropped %q; the bound cut off the actionable half", got, want)
		}
	}

	long := rpcErr(t, marotte.RPCCodeBridgeExited, "refused", rpcerr.Mapped{
		ErrorType: "AgentModelPinUnservableError",
		RequestID: strings.Repeat("z", 5000),
	})
	if reason := promptFailureReason(long, false); len(reason) > idCap+512 {
		t.Errorf("an oversized request id was not bounded: %d bytes", len(reason))
	}
}
