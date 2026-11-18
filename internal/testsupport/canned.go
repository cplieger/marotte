package testsupport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// CannedRerecord is what a failure tells its reader to run: the set is a recording, so a stale answer is recorded
// again, never edited.
const CannedRerecord = "a wire change re-records the set: tests/perf/record-canned.sh (CONTRIBUTING.md \"Performance\")"

// CannedAnswer is one recorded answer of the client's canned set (static-src/perf/canned/answers).
type CannedAnswer struct {
	File    string          `json:"-"`
	Route   string          `json:"route"`
	URL     string          `json:"url"`
	Request json.RawMessage `json:"request"`
	Body    json.RawMessage `json:"body"`
	Status  int             `json:"status"`
}

// RepoRoot is the module root: the nearest directory above the working directory that holds go.mod.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("RepoRoot: no go.mod above the working directory")
		}
		dir = parent
	}
}

// CannedAnswers is every recorded answer of the canned set, in file-name order.
func CannedAnswers(t testing.TB) []CannedAnswer {
	t.Helper()
	dir := filepath.Join(RepoRoot(t), "static-src", "perf", "canned", "answers")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("CannedAnswers: no recorded answers under %s (%v); %s", dir, err, CannedRerecord)
	}
	out := make([]CannedAnswer, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("CannedAnswers: %v", err)
		}
		var a CannedAnswer
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("CannedAnswers: %s: %v", filepath.Base(f), err)
		}
		a.File = filepath.Base(f)
		out = append(out, a)
	}
	return out
}

// HoldCanned runs check on every recorded answer of route, a key of tests/perf/canned-routes.json, and fails t when
// the set records none. A route the client reads through no generated decoder is held by one HoldCanned or
// HoldCannedShape call with the route as a string literal: TestCannedRoutes_EveryNullRouteIsHeld finds the calls in
// the source.
func HoldCanned(t *testing.T, route string, check func(a *CannedAnswer) error) {
	t.Helper()
	n := 0
	for _, a := range CannedAnswers(t) {
		if a.Route != route {
			continue
		}
		n++
		if err := check(&a); err != nil {
			t.Errorf("%s (%s): %v; %s", a.File, route, err, CannedRerecord)
		}
	}
	if n == 0 {
		t.Errorf("the canned set records no answer of %s; %s", route, CannedRerecord)
	}
}

// HoldCannedShape holds every recorded answer of route to ReadsBackAs a T, the type its handler writes.
func HoldCannedShape[T any](t *testing.T, route string) {
	t.Helper()
	HoldCanned(t, route, func(a *CannedAnswer) error {
		var v T
		return ReadsBackAs(a.Body, &v)
	})
}

// ReadsBackAs fails unless body reads into ptr with no field its type lacks and writes back as the same JSON, so an
// answer missing a field the type writes fails too.
func ReadsBackAs(body json.RawMessage, ptr any) error {
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return fmt.Errorf("no body, and the route answers with a %T", ptr)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ptr); err != nil {
		return fmt.Errorf("does not read as %T: %w", ptr, err)
	}
	again, err := json.Marshal(ptr)
	if err != nil {
		return fmt.Errorf("re-encoding %T: %w", ptr, err)
	}
	same, err := sameJSON(body, again)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("is not what %T writes:\nrecorded %s\nwritten  %s", ptr, clip(body), clip(again))
	}
	return nil
}

func sameJSON(a, b []byte) (bool, error) {
	var x, y any
	for _, p := range []struct {
		v   *any
		raw []byte
	}{{&x, a}, {&y, b}} {
		dec := json.NewDecoder(bytes.NewReader(p.raw))
		dec.UseNumber()
		if err := dec.Decode(p.v); err != nil {
			return false, fmt.Errorf("comparing JSON: %w", err)
		}
	}
	return reflect.DeepEqual(x, y), nil
}

func clip(b []byte) string {
	const most = 600
	if len(b) > most {
		return string(b[:most]) + "…"
	}
	return string(b)
}
