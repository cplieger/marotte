package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDeliverer is the delivery port, answering each call from answer.
type fakeDeliverer struct {
	answer func(session string) (string, error)
	calls  []delivery
	mu     sync.Mutex
}

type delivery struct{ session, text, source string }

func (f *fakeDeliverer) Deliver(_ context.Context, session, text, source string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, delivery{session: session, text: text, source: source})
	f.mu.Unlock()
	if f.answer == nil {
		return DeliveredPrompt, nil
	}
	return f.answer(session)
}

type handlerRig struct {
	h     *Handler
	deliv *fakeDeliverer
	now   time.Time
}

func newHandlerRig(t *testing.T) *handlerRig {
	t.Helper()
	rig := &handlerRig{deliv: &fakeDeliverer{}, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	rig.h = NewHandler(mustGate(t, testToken), rig.deliv)
	rig.h.now = func() time.Time { return rig.now }
	return rig
}

// serve posts body to session's notify route through a mux holding Pattern, the way the server mounts it.
func (rig *handlerRig) serve(t *testing.T, session, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(Pattern, rig.h)
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/notify", strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func (rig *handlerRig) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return rig.serve(t, "sess-1", "Bearer "+testToken, body)
}

func notifyBody(t *testing.T, text, source, key string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"text": text, "source": source, "dedupe_key": key})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeAnswer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer %q is not JSON: %v", w.Body.String(), err)
	}
	return got
}

func TestHandler_DeliversThroughThePort(t *testing.T) {
	for _, verb := range []string{DeliveredSteer, DeliveredPrompt} {
		t.Run(verb, func(t *testing.T) {
			rig := newHandlerRig(t)
			rig.deliv.answer = func(string) (string, error) { return verb, nil }
			w := rig.post(t, notifyBody(t, "1 new message from RubyRobin", "grelot", "m-1"))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
			}
			if got := decodeAnswer(t, w); len(got) != 1 || got["delivered"] != verb {
				t.Errorf("answer = %v, want {delivered: %s}", got, verb)
			}
			want := delivery{session: "sess-1", text: "1 new message from RubyRobin", source: "grelot"}
			if len(rig.deliv.calls) != 1 || rig.deliv.calls[0] != want {
				t.Errorf("deliveries = %+v, want [%+v]", rig.deliv.calls, want)
			}
		})
	}
}

func TestHandler_Dedupe(t *testing.T) {
	t.Run("the same key within ten minutes is a duplicate", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		rig.now = rig.now.Add(dedupeWindow - time.Second)
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if got := decodeAnswer(t, w); len(got) != 1 || got["duplicate"] != true {
			t.Errorf("answer = %v, want {duplicate: true}", got)
		}
		if len(rig.deliv.calls) != 1 {
			t.Errorf("delivered %d times, want once", len(rig.deliv.calls))
		}
	})
	t.Run("the same key after ten minutes is delivered again", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		rig.now = rig.now.Add(dedupeWindow)
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if got := decodeAnswer(t, w); got["delivered"] != DeliveredPrompt {
			t.Errorf("answer = %v, want a delivery", got)
		}
		if len(rig.deliv.calls) != 2 {
			t.Errorf("delivered %d times, want twice", len(rig.deliv.calls))
		}
	})
	t.Run("a new key is not a duplicate", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-2"))
		if got := decodeAnswer(t, w); got["delivered"] != DeliveredPrompt {
			t.Errorf("answer = %v, want a delivery", got)
		}
	})
	t.Run("the same key on another session is not a duplicate", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		w := rig.serve(t, "sess-2", "Bearer "+testToken, notifyBody(t, "wake", "grelot", "m-1"))
		if got := decodeAnswer(t, w); got["delivered"] != DeliveredPrompt {
			t.Errorf("answer = %v, want a delivery", got)
		}
	})
	t.Run("a refused delivery is not recorded, so its retry is delivered", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.deliv.answer = func(string) (string, error) { return "", ErrUnavailable }
		rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		rig.deliv.answer = nil
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if got := decodeAnswer(t, w); got["delivered"] != DeliveredPrompt {
			t.Errorf("retry answer = %v, want a delivery", got)
		}
	})
}

func TestHandler_DeliveryFailures(t *testing.T) {
	t.Run("an unknown session is 404", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.deliv.answer = func(string) (string, error) { return "", ErrUnknownSession }
		if w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1")); w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
	t.Run("a session id no chat can hold is 404 without a delivery", func(t *testing.T) {
		rig := newHandlerRig(t)
		w := rig.serve(t, "a..b", "Bearer "+testToken, notifyBody(t, "wake", "grelot", "m-1"))
		if w.Code != http.StatusNotFound || len(rig.deliv.calls) != 0 {
			t.Errorf("status = %d after %d deliveries, want 404 and none", w.Code, len(rig.deliv.calls))
		}
	})
	t.Run("an unavailable engine is 503 with Retry-After", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.deliv.answer = func(string) (string, error) { return "", ErrUnavailable }
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
		if w.Header().Get("Retry-After") == "" {
			t.Errorf("503 carries no Retry-After")
		}
	})
	t.Run("refused text is 400 naming text", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.deliv.answer = func(string) (string, error) {
			return "", &TextRefusedError{Reason: "it reads as a system notice"}
		}
		w := rig.post(t, notifyBody(t, "[notification/info] hi", "grelot", "m-1"))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"text: it reads as a system notice"`) {
			t.Errorf("answer = %d %s, want 400 naming text", w.Code, w.Body.String())
		}
	})
	t.Run("any other failure is 500 and not recorded", func(t *testing.T) {
		rig := newHandlerRig(t)
		rig.deliv.answer = func(string) (string, error) { return "", errors.New("disk full") }
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
		rig.deliv.answer = nil
		if got := decodeAnswer(t, rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))); got["delivered"] != DeliveredPrompt {
			t.Errorf("retry answer = %v, want a delivery", got)
		}
	})
}

func TestHandler_Credential(t *testing.T) {
	t.Run("door off is 401 naming the variable", func(t *testing.T) {
		deliv := &fakeDeliverer{}
		rig := &handlerRig{h: NewHandler(Gate{}, deliv), deliv: deliv}
		w := rig.post(t, notifyBody(t, "wake", "grelot", "m-1"))
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), TokenEnv) {
			t.Errorf("answer = %d %s, want 401 naming %s", w.Code, w.Body.String(), TokenEnv)
		}
		if len(deliv.calls) != 0 {
			t.Errorf("delivered with the door off")
		}
	})
	for name, auth := range map[string]string{
		"a wrong token":   "Bearer " + strings.Repeat("x", len(testToken)),
		"no token":        "",
		"another scheme":  "Basic " + testToken,
		"the bare secret": testToken,
	} {
		t.Run(name+" is 401", func(t *testing.T) {
			rig := newHandlerRig(t)
			w := rig.serve(t, "sess-1", auth, notifyBody(t, "wake", "grelot", "m-1"))
			if w.Code != http.StatusUnauthorized || len(rig.deliv.calls) != 0 {
				t.Errorf("answer = %d after %d deliveries, want 401 and none", w.Code, len(rig.deliv.calls))
			}
		})
	}
}

func TestHandler_BodyValidation(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"text missing", `{"source":"grelot","dedupe_key":"m-1"}`, "text"},
		{"text blank", notifyBody(t, " \n\t", "grelot", "m-1"), "text"},
		{"text over the cap", notifyBody(t, strings.Repeat("a", maxTextBytes+1), "grelot", "m-1"), "text"},
		{"source missing", `{"text":"wake","dedupe_key":"m-1"}`, "source"},
		{"source over the cap", notifyBody(t, "wake", strings.Repeat("g", maxSourceBytes+1), "m-1"), "source"},
		{"source with a space", notifyBody(t, "wake", "grelot bot", "m-1"), "source"},
		{"source with a line break", notifyBody(t, "wake", "grelot\nx", "m-1"), "source"},
		{"source with a non-ASCII letter", notifyBody(t, "wake", "grélot", "m-1"), "source"},
		{"dedupe_key missing", `{"text":"wake","source":"grelot"}`, "dedupe_key"},
		{"dedupe_key over the cap", notifyBody(t, "wake", "grelot", strings.Repeat("k", maxDedupeKeyBytes+1)), "dedupe_key"},
		{"dedupe_key with a control byte", notifyBody(t, "wake", "grelot", "m\x01"), "dedupe_key"},
		{"not JSON", `text=wake`, "body"},
		{"a JSON array", `[]`, "body"},
		{"a field of the wrong type", `{"text":1,"source":"grelot","dedupe_key":"m-1"}`, "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newHandlerRig(t)
			w := rig.post(t, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if got, _ := decodeAnswer(t, w)["error"].(string); !strings.HasPrefix(got, tc.field+": ") {
				t.Errorf("error = %q, want it to name %s", got, tc.field)
			}
			if len(rig.deliv.calls) != 0 {
				t.Errorf("a refused body was delivered")
			}
		})
	}
	t.Run("the caps themselves are accepted", func(t *testing.T) {
		rig := newHandlerRig(t)
		w := rig.post(t, notifyBody(t, strings.Repeat("a", maxTextBytes),
			strings.Repeat("g", maxSourceBytes), strings.Repeat("k", maxDedupeKeyBytes)))
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
	})
	t.Run("another method is 405", func(t *testing.T) {
		rig := newHandlerRig(t)
		mux := http.NewServeMux()
		mux.Handle(Pattern, rig.h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/notify", http.NoBody))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})
	t.Run("a body past the limit is 413", func(t *testing.T) {
		rig := newHandlerRig(t)
		w := rig.post(t, `{"text":"`+strings.Repeat("a", maxBodyBytes)+`"}`)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", w.Code)
		}
	})
}

func TestDedupe_SizeCapAndExpiry(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	key := func(i int) dedupeKey { return dedupeKey{session: "s", key: "k" + strconv.Itoa(i)} }
	fill := func(d *dedupeTable, at time.Time, n int) {
		for i := range n {
			if d.reserve(key(i), at) != reserveFresh {
				t.Fatalf("key %d was not fresh", i)
			}
			d.commit(key(i), at.Add(time.Duration(i)*time.Millisecond))
		}
	}
	t.Run("the table never grows past its cap and evicts the oldest", func(t *testing.T) {
		d := newDedupeTable()
		fill(d, start, maxDedupeEntries)
		if got := d.reserve(dedupeKey{session: "s", key: "new"}, start.Add(time.Minute)); got != reserveFresh {
			t.Fatalf("reserve past the cap = %v, want fresh", got)
		}
		if n := d.len(); n != maxDedupeEntries {
			t.Errorf("entries = %d, want the cap %d", n, maxDedupeEntries)
		}
		if got := d.reserve(key(0), start.Add(time.Minute)); got != reserveFresh {
			t.Errorf("the oldest key = %v after an eviction, want fresh", got)
		}
		if got := d.reserve(key(maxDedupeEntries-1), start.Add(time.Minute)); got != reserveDuplicate {
			t.Errorf("the newest key = %v, want still a duplicate", got)
		}
	})
	t.Run("expired entries are swept rather than evicting live ones", func(t *testing.T) {
		d := newDedupeTable()
		fill(d, start, maxDedupeEntries-1)
		live := dedupeKey{session: "s", key: "live"}
		d.reserve(live, start.Add(dedupeWindow))
		d.commit(live, start.Add(dedupeWindow))
		if n := d.len(); n != maxDedupeEntries {
			t.Fatalf("entries = %d, want the cap", n)
		}
		later := start.Add(2*dedupeWindow - time.Second)
		d.reserve(dedupeKey{session: "s", key: "next"}, later)
		if n := d.len(); n != 2 {
			t.Errorf("entries after the sweep = %d, want the live key and the new one", n)
		}
		if got := d.reserve(live, later); got != reserveDuplicate {
			t.Errorf("the live key = %v, want a duplicate", got)
		}
	})
	t.Run("a key in flight is neither a duplicate nor delivered twice", func(t *testing.T) {
		d := newDedupeTable()
		d.reserve(key(0), start)
		if got := d.reserve(key(0), start); got != reserveInFlight {
			t.Errorf("a second reserve = %v, want in flight", got)
		}
		d.release(key(0))
		if got := d.reserve(key(0), start); got != reserveFresh {
			t.Errorf("reserve after a release = %v, want fresh", got)
		}
	})
	t.Run("a table full of keys in flight refuses a new one", func(t *testing.T) {
		d := newDedupeTable()
		for i := range maxDedupeEntries {
			d.reserve(key(i), start)
		}
		if got := d.reserve(dedupeKey{session: "s", key: "new"}, start); got != reserveInFlight {
			t.Errorf("reserve into a table of in-flight keys = %v, want refused as in flight", got)
		}
	})
}

func (d *dedupeTable) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}
