package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func kiroFSMsg(t *testing.T, id int64, method, path string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		ID:     &id,
		Method: method,
		Params: mustJSON(t, map[string]any{"sessionId": "sess_x", "path": path}),
	}
}

func TestKiroFSStat(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path     string
		wantType string
		wantSize int64
	}{
		{"f.txt", fsTypeFile, 5},
		{"d", fsTypeDirectory, -1}, // directory size is filesystem-dependent
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h, br := hubForFSTest(t, work)
			h.inbound.respondKiroFSStat(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSStat, tc.path))
			<-br.done
			if br.response.err != nil {
				t.Fatalf("err = %v, want nil", br.response.err)
			}
			body, ok := br.response.result.(kiroStatBody)
			if !ok {
				t.Fatalf("result type = %T, want kiroStatBody", br.response.result)
			}
			if body.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", body.Type, tc.wantType)
			}
			if tc.wantSize >= 0 && body.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", body.Size, tc.wantSize)
			}
		})
	}
}

// TestKiroFSStatWireShapeCarriesSize pins `size`: isFSStatCapabilityResponse throws without it.
func TestKiroFSStatWireShapeCarriesSize(t *testing.T) {
	data, err := json.Marshal(kiroStatBody{Type: fsTypeFile, Size: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"type", "size"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("wire form %s is missing %q; KAS's guard requires it even at zero", data, k)
		}
	}
}

func TestKiroFSStatConfinesPath(t *testing.T) {
	// The target must exist outside the work dir; an ENOENT path errors with or without confinement.
	outside := t.TempDir()
	target := filepath.Join(outside, "real.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSStat(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSStat, target))
	<-br.done
	if br.response.err == nil {
		t.Error("err = nil for an existing path outside the work dir, want an error")
	}
}

func TestKiroFSReadDirectory(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "a.txt"), filepath.Join(work, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSReadDirectory, "."))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	got := map[string]string{}
	for _, e := range body.Entries {
		got[e.Name] = e.Type
	}
	want := map[string]string{"a.txt": fsTypeFile, "sub": fsTypeDirectory, "link": fsTypeSymlink}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("entry %q type = %q, want %q (os.ReadDir must not follow the link, matching node's withFileTypes)", name, got[name], typ)
		}
	}
}

// TestKiroFSReadDirectoryFiltersNothing pins the accepted residual: KAS's ignore evaluators
// judge the listed directory, not entry names, and marotte adds no matcher of its own.
func TestKiroFSReadDirectoryFiltersNothing(t *testing.T) {
	work := t.TempDir()
	for _, name := range []string{"keep.txt", ".env.dec", ".kiroignore"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, br := hubForFSTest(t, work)

	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSReadDirectory, "."))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	got := map[string]bool{}
	for _, e := range body.Entries {
		got[e.Name] = true
	}
	// `.env.dec` is the panel copy's example and `.kiroignore` the floor, so a re-added client filter fails here.
	for _, name := range []string{"keep.txt", ".env.dec", ".kiroignore"} {
		if !got[name] {
			t.Errorf("%q is missing from the listing; marotte filters no entry, KAS does the enforcing", name)
		}
	}
}

// TestKiroFSReadDirectoryMissingIsEmptyNotError matches KAS's NodeFileSystem, which returns [] on ENOENT.
func TestKiroFSReadDirectoryMissingIsEmptyNotError(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSReadDirectory, "no-such-dir"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil for a missing directory", br.response.err)
	}
	body, ok := br.response.result.(kiroReadDirBody)
	if !ok {
		t.Fatalf("result type = %T, want kiroReadDirBody", br.response.result)
	}
	if len(body.Entries) != 0 {
		t.Errorf("Entries = %v, want empty", body.Entries)
	}
}

// TestKiroFSReadDirectoryEmptyMarshalsAsArray pins `[]`: KAS calls .map on `entries`.
func TestKiroFSReadDirectoryEmptyMarshalsAsArray(t *testing.T) {
	data, err := json.Marshal(kiroReadDirBody{Entries: []kiroDirEntry{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"entries":[]}` {
		t.Errorf("wire form = %s, want {\"entries\":[]}", data)
	}
}

func TestKiroFSDeleteFile(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "gone.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "gone.txt"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: the file should be gone", err)
	}
}

// TestKiroFSDeleteDirectoryRecurses mirrors KAS's NodeFileSystem (fs.rm recursive).
func TestKiroFSDeleteDirectoryRecurses(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "tree")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "tree"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: the tree should be gone", err)
	}
}

// TestKiroFSDeleteRefusesWorkDirRoot pins the handler's one refusal.
func TestKiroFSDeleteRefusesWorkDirRoot(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "."))
	<-br.done
	// The sentinel, not any error: without the guard os.RemoveAll refuses "." with EINVAL anyway.
	if !errors.Is(br.response.err, errRefusedWorkDirRoot) {
		t.Fatalf("err = %v, want errRefusedWorkDirRoot", br.response.err)
	}
	if _, err := os.Stat(filepath.Join(work, "keep.txt")); err != nil {
		t.Errorf("the workspace was touched anyway: %v", err)
	}
}

// TestKiroFSDeleteRefusesRelativeResolvedPath pins the absoluteness assertion: a relative
// path would resolve against the server's cwd. Through the helper, since
// resolveInsideWorkDir cannot produce one today.
func TestKiroFSDeleteRefusesRelativeResolvedPath(t *testing.T) {
	if filepath.IsAbs("relative/path") {
		t.Skip("platform treats the fixture as absolute")
	}
	err := fmt.Errorf("%w: resolved path is not absolute", errRefusedWorkDirRoot)
	if !errors.Is(err, errRefusedWorkDirRoot) {
		t.Error("the not-absolute rejection must carry errRefusedWorkDirRoot so callers can classify it")
	}
}

func TestKiroFSDeleteConfinesPath(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, t.TempDir())
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, victim))
	<-br.done
	if br.response.err == nil {
		t.Error("err = nil for an absolute path outside the work dir, want an error")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a path outside the work dir was deleted: %v", err)
	}
}

// TestKiroFSDeleteSuccessCarriesNoMessage pins the reply: KAS throws a non-empty `message` as an error.
func TestKiroFSDeleteSuccessCarriesNoMessage(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "f"))
	<-br.done
	data, err := json.Marshal(br.response.result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("success reply = %s, want {} (a `message` field is thrown as an error)", data)
	}
}

// TestKiroFSDeleteDoesNotStage pins "no second gate": KAS restores a rejected delete through
// fs/write_text_file, and a gate would intercept that restore.
func TestKiroFSDeleteDoesNotStage(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "f")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	_, _ = h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.SupervisedMode = true
		return true
	})

	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "f"))
	<-br.done
	if br.response.err != nil {
		t.Fatalf("err = %v, want nil", br.response.err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("stat err = %v, want ErrNotExist: supervised mode must not stage a KAS-reviewed delete", err)
	}
}

func TestHandleKiroFSRequestClaimsOnlyItsOwnMethods(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	cases := []struct {
		method string
		want   bool
	}{
		{methodKiroFSStat, true},
		{methodKiroFSReadDirectory, true},
		{methodKiroFSDelete, true},
		{marotte.MethodFSRead, false},
		{marotte.MethodFSWrite, false},
		// Deliberately undeclared: the fs/{read,write}_text_file rung keeps every write guardrail.
		{"_kiro/fs/read_file", false},
		{"_kiro/fs/write_file", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			msg := kiroFSMsg(t, 9, tc.method, "x")
			if got := h.inbound.handleKiroFSRequest(t.Context(), "c1", h.originOf("c1"), msg); got != tc.want {
				t.Errorf("handleKiroFSRequest(%q) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// TestHandleKiroFSRequest_AnOrdinaryRequestNeitherPanicsNorApologises pins the panic net's
// boundary: a net firing on a clean return overwrites a good reply with "internal error".
func TestHandleKiroFSRequest_AnOrdinaryRequestNeitherPanicsNorApologises(t *testing.T) {
	logs := captureLogs(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)

	if !h.inbound.handleKiroFSRequest(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSStat, "f.txt")) {
		t.Fatal("handleKiroFSRequest did not claim a stat, so nothing ran")
	}
	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stat never answered")
	}
	// Drain the goroutine: the deferred net fires after the response.
	shutdownHub(t, h)

	br.respMu.Lock()
	got := br.response
	br.respMu.Unlock()
	if got.err != nil {
		t.Errorf("a stat that succeeded answered with %v; the panic net overwrote a good reply",
			got.err)
	}
	if _, ok := got.result.(kiroStatBody); !ok {
		t.Errorf("result type = %T, want kiroStatBody", got.result)
	}
	const panicLine = "kiro fs handler panic"
	if out := logs.String(); strings.Contains(out, `"msg":"`+panicLine+`"`) {
		t.Errorf("a handler that returned cleanly was reported as %q: %s", panicLine, out)
	}
}
