package httpreply

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A refusal naming fields answers 400 with the message and each field's error, in the order given.
func TestBadRequestFields_NamesEachField(t *testing.T) {
	rec := httptest.NewRecorder()

	BadRequestFields(rec, "This rule has problems", []FieldError{
		{Field: "sample", Error: "Send a sample or use_last, not both"},
		{Field: "triggers[0].every.minutes", Error: "At least 5 minutes"},
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	want := `{"error":"This rule has problems","fields":[{"field":"sample","error":"Send a sample or use_last, not both"},` +
		`{"field":"triggers[0].every.minutes","error":"At least 5 minutes"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != MIMETypeJSON {
		t.Errorf("Content-Type = %q, want %s", ct, MIMETypeJSON)
	}
}

// A rate refusal answers 429 with Retry-After in whole seconds rounded up (RFC 9110 §10.2.3) and the exact wait in
// the body.
func TestTooManyRequests_AnswersTheWait(t *testing.T) {
	cases := []struct {
		name   string
		wait   time.Duration
		header string
		body   string
	}{
		{"whole seconds", 2 * time.Second, "2", `{"error":"Too many tests","retry_after_ms":2000}`},
		{"a part second rounds up", 1500 * time.Millisecond, "2", `{"error":"Too many tests","retry_after_ms":1500}`},
		{"under a millisecond", 300 * time.Microsecond, "1", `{"error":"Too many tests","retry_after_ms":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			TooManyRequests(rec, "Too many tests", tc.wait)
			if rec.Code != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.header {
				t.Errorf("Retry-After = %q, want %q", got, tc.header)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.body {
				t.Errorf("body = %s, want %s", got, tc.body)
			}
		})
	}
}
