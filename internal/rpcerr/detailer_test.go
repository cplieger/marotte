package rpcerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// dataErr is a minimal detailer standing in for *marotte.RPCError, which this leaf
// package does not import.
type dataErr struct {
	msg  string
	data json.RawMessage
}

func (e *dataErr) Error() string              { return e.msg }
func (e *dataErr) ErrorData() json.RawMessage { return e.data }

// TestDetails_FoundAtAnyWrappingDepth pins that the cause is found through several layers
// of fmt.Errorf; a top-level type assertion would find none.
func TestDetails_FoundAtAnyWrappingDepth(t *testing.T) {
	leaf := &dataErr{msg: "Internal error", data: json.RawMessage(`{"details":"session not idle"}`)}

	for _, depth := range []int{0, 1, 5} {
		err := error(leaf)
		for range depth {
			err = fmt.Errorf("layer: %w", err)
		}
		if got := Details(err); got != "session not idle" {
			t.Errorf("Details at wrapping depth %d = %q, want %q", depth, got, "session not idle")
		}
		if got := Text(err); got != "session not idle" {
			t.Errorf("Text at wrapping depth %d = %q, want %q", depth, got, "session not idle")
		}
	}
}

// TestDetails_FoundThroughAJoin pins the other tree shape errors.AsType walks and
// a type assertion does not: errors.Join's multi-Unwrap.
func TestDetails_FoundThroughAJoin(t *testing.T) {
	leaf := &dataErr{msg: "Internal error", data: json.RawMessage(`{"details":"policy parse failed"}`)}
	err := errors.Join(errors.New("first"), fmt.Errorf("second: %w", leaf))
	if got := Details(err); got != "policy parse failed" {
		t.Errorf("Details through errors.Join = %q, want %q", got, "policy parse failed")
	}
}

// TestDetails_IgnoresAnErrorWithNoData pins that an error with no `error.data` yields an
// empty Details and Text falls back to the error string.
func TestDetails_IgnoresAnErrorWithNoData(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", errors.New("params invalid"))
	if got := Details(err); got != "" {
		t.Errorf("Details on a plain error = %q, want %q", got, "")
	}
	if got := Text(err); got != "wrapped: params invalid" {
		t.Errorf("Text on a plain error = %q, want the error string", got)
	}
}
