package agent

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// writeKASExport writes the zip KAS's handleExportSession would, under a
// kiro-exports-* directory of TMPDIR, and returns its path.
func writeKASExport(t *testing.T, sid string) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), kasExportDirPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create(sid + "/session.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(`{"id":"` + sid + `"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "kiro-session-"+sid+".zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// An unknown id gets KAS's in-band not-found.
func exportAnswer(b *fakeBridge, paths map[string]string, asked *[]string, mu *sync.Mutex) {
	b.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionExport {
			return nil, nil, false
		}
		sid, _ := params["sessionId"].(string)
		mu.Lock()
		*asked = append(*asked, sid)
		mu.Unlock()
		p, ok := paths[sid]
		if !ok {
			return json.RawMessage(`{"success":false,"error":"Session ` + sid + ` not found"}`), nil, true
		}
		res, _ := json.Marshal(map[string]any{"success": true, "filePath": p})
		return res, nil, true
	}
}

func exportRequest(h *Runtime, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+id+"/kiro-session", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.handleKiroSessionExport(rec, req)
	return rec
}

func zipNames(t *testing.T, body []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response is not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

func TestKiroSessionExport_MergesTheChainAndRoutesTheLiveSegment(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h, cs, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	cs.seed(t, "c1", func(c *marotte.Chat) {
		c.Name = "Fix the parser"
		c.ACPSessionID = "sess_b"
		c.PriorACPSessionIDs = []string{"sess_a"}
	})
	pathA, pathB := writeKASExport(t, "sess_a"), writeKASExport(t, "sess_b")

	var mu sync.Mutex
	var utilAsked, liveAsked []string
	exportAnswer(br, map[string]string{"sess_a": pathA}, &utilAsked, &mu)
	live := newFakeBridge()
	live.sessionID = "sess_b"
	exportAnswer(live, map[string]string{"sess_b": pathB}, &liveAsked, &mu)
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: live, state: bridgeIdle})

	rec := exportRequest(h, "c1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET kiro-session = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, ".kiro-session.zip") {
		t.Errorf("Content-Disposition = %q, want a .kiro-session.zip attachment", got)
	}
	want := []string{"sess_a/session.json", "sess_b/session.json"}
	if got := zipNames(t, rec.Body.Bytes()); !slices.Equal(got, want) {
		t.Errorf("merged entries = %v, want %v", got, want)
	}
	if !slices.Equal(utilAsked, []string{"sess_a"}) {
		t.Errorf("utility session asked for %v, want [sess_a]", utilAsked)
	}
	if !slices.Equal(liveAsked, []string{"sess_b"}) {
		t.Errorf("live bridge asked for %v, want [sess_b]", liveAsked)
	}
	for _, p := range []string{pathA, pathB} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("KAS export %s still on disk after serving (stat err %v)", p, err)
		}
	}
}

func TestKiroSessionExport_SkipsASegmentKASCannotFind(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h, cs, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	cs.seed(t, "c1", func(c *marotte.Chat) {
		c.ACPSessionID = "sess_b"
		c.PriorACPSessionIDs = []string{"sess_gone"}
	})
	var mu sync.Mutex
	var asked []string
	exportAnswer(br, map[string]string{"sess_b": writeKASExport(t, "sess_b")}, &asked, &mu)

	rec := exportRequest(h, "c1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET kiro-session = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := zipNames(t, rec.Body.Bytes()); !slices.Equal(got, []string{"sess_b/session.json"}) {
		t.Errorf("entries = %v, want only the segment KAS still has", got)
	}
}

func TestKiroSessionExport_NothingToExportIs404(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h, cs, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	cs.seed(t, "c1", func(c *marotte.Chat) { c.ACPSessionID = "sess_gone" })
	var mu sync.Mutex
	var asked []string
	exportAnswer(br, nil, &asked, &mu)

	if rec := exportRequest(h, "c1"); rec.Code != http.StatusNotFound {
		t.Errorf("GET kiro-session with no exportable segment = %d, want 404", rec.Code)
	}
	if rec := exportRequest(h, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("GET kiro-session for an unknown chat = %d, want 404", rec.Code)
	}
}

func TestKiroSessionExport_RefusesAPathOutsideTheExportDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h, cs, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	cs.seed(t, "c1", func(c *marotte.Chat) { c.ACPSessionID = "sess_a" })
	outside := filepath.Join(t.TempDir(), "kiro-session-sess_a.zip")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	exportAnswer(br, map[string]string{"sess_a": outside}, &asked, &mu)

	if rec := exportRequest(h, "c1"); rec.Code != http.StatusInternalServerError {
		t.Errorf("GET kiro-session for an unconfined path = %d, want 500", rec.Code)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a path outside the export dir was touched: %v", err)
	}
}

func TestOpenConfinedExport_RefusesASymlink(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir, err := os.MkdirTemp(os.TempDir(), kasExportDirPrefix)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "kiro-session-s.zip")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if e, err := openConfinedExport(link, "s"); err == nil {
		e.close()
		t.Error("openConfinedExport opened a symlink, want a refusal")
	}
}

func TestOpenConfinedExport_RefusesAnotherSessionsFile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	p := writeKASExport(t, "sess_b")
	if e, err := openConfinedExport(p, "sess_a"); err == nil {
		e.close()
		t.Errorf("openConfinedExport(%s, sess_a) opened sess_b's export, want a refusal", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("a refused export was touched: %v", err)
	}
}

func TestOpenConfinedExport_RefusesASymlinkedExportDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "kiro-session-s.zip"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(os.TempDir(), kasExportDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if e, err := openConfinedExport(filepath.Join(link, "kiro-session-s.zip"), "s"); err == nil {
		e.close()
		t.Error("openConfinedExport followed a symlinked export directory, want a refusal")
	}
}

// A directory swapped for a symlink after opening must not redirect cleanup.
func TestKASExportRemove_IgnoresASwappedExportDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	p := writeKASExport(t, "s")
	e, err := openConfinedExport(p, "s")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, filepath.Base(p))
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}

	e.remove()

	if _, err := os.Stat(victim); err != nil {
		t.Errorf("cleanup followed the swapped directory and removed %s: %v", victim, err)
	}
	if _, err := os.Stat(filepath.Join(moved, filepath.Base(p))); !os.IsNotExist(err) {
		t.Errorf("the opened export survived cleanup (stat err %v)", err)
	}
}
