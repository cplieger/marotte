package bridge

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestClassifyDroppedHead(t *testing.T) {
	cases := []struct {
		name    string
		head    string
		kind    droppedKind
		id      int64
		method  string
		session marotte.SessionID
	}{
		{"a response", `{"jsonrpc":"2.0","id":7,"result":{"text":"abc`, droppedResponse, 7, "", ""},
		{"an error response", `{"jsonrpc":"2.0","id":9,"error":{"code":-1,"message":"x`, droppedResponse, 9, "", ""},
		{
			"a request", `{"jsonrpc":"2.0","id":3,"method":"fs/write_text_file","params":{"sessionId":"sess-a","content":"`,
			droppedRequest, 3, "fs/write_text_file", "sess-a",
		},
		{
			"a notification", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"x":[1,2]},"sessionId":"sess-b","more":"`,
			droppedNotification, 0, "session/update", "sess-b",
		},
		{
			"a notification cut before its session", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"text":"`,
			droppedNotification, 0, "session/update", "",
		},
		{"an id cut before its kind", `{"jsonrpc":"2.0","id":5,`, droppedUnknown, 5, "", ""},
		{"not an object", `["x"`, droppedUnknown, 0, "", ""},
		{"empty", ``, droppedUnknown, 0, "", ""},
		{
			"a nested sessionId is not params'", `{"method":"m","params":{"update":{"sessionId":"sess-inner"},"x":"`,
			droppedNotification, 0, "m", "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDroppedHead([]byte(tc.head))
			if got.kind != tc.kind {
				t.Errorf("kind = %q, want %q", got.kind, tc.kind)
			}
			if tc.id != 0 && (got.id == nil || *got.id != tc.id) {
				t.Errorf("id = %v, want %d", got.id, tc.id)
			}
			if got.method != tc.method {
				t.Errorf("method = %q, want %q", got.method, tc.method)
			}
			if got.sessionID != tc.session {
				t.Errorf("sessionID = %q, want %q", got.sessionID, tc.session)
			}
		})
	}
}

// FuzzClassifyDroppedHead_noPanicBoundedWork: any head classifies without a panic, a kind is always set, and a
// request or response always carries the id it was classified by.
func FuzzClassifyDroppedHead_noPanicBoundedWork(f *testing.F) {
	for _, seed := range []string{
		`{"id":1,"method":"m","params":{"sessionId":"s"}}`, `{"id":2,"result":{}}`, `{"method":"m"`, `{`, `[[[[`,
		`{"params":{"a":{"b":[{"c":1}]}},"method":"x"}`, strings.Repeat(`{"a":`, 200),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, head []byte) {
		if len(head) > droppedHeadCap {
			head = head[:droppedHeadCap]
		}
		got := classifyDroppedHead(head)
		switch got.kind {
		case droppedRequest, droppedResponse:
			if got.id == nil {
				t.Fatalf("kind %q without an id for %q", got.kind, head)
			}
		case droppedNotification:
			if got.method == "" {
				t.Fatalf("a notification without a method for %q", head)
			}
		case droppedUnknown:
		default:
			t.Fatalf("unset kind %q for %q", got.kind, head)
		}
	})
}
