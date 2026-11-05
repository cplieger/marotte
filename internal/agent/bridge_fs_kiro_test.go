package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// KAS's revert of a create whose file is already gone succeeds only on its not-found dialect.
func TestKiroFSDeleteMissingPathSaysPathDoesNotExist(t *testing.T) {
	for _, path := range []string{"ghost.txt", "nodir/ghost.txt"} {
		err := deleteThrough(t, t.TempDir(), path)
		if err == nil || !strings.HasPrefix(err.Error(), kasNotFoundPrefix) {
			t.Errorf("delete(%s) err = %v, want a message starting %q", path, err, kasNotFoundPrefix)
		}
	}
}

func deleteThrough(t *testing.T, work, path string) error {
	t.Helper()
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, path))
	<-br.done
	return br.response.err
}

// TestKiroFSDeleteRefusesDirectory pins the undo invariant: no snapshot can hold a directory.
func TestKiroFSDeleteRefusesDirectory(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "tree", "nested", "f")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deleteThrough(t, work, "tree"); !errors.Is(err, errNoUndoDirectory) {
		t.Errorf("err = %v, want errNoUndoDirectory", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("the refused tree lost its file: %v", err)
	}
}

func TestKiroFSDeleteRefusesFileOverReadCap(t *testing.T) {
	work := t.TempDir()
	big := filepath.Join(work, "big.bin")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, fsReadCap+1); err != nil {
		t.Fatal(err)
	}
	if err := deleteThrough(t, work, "big.bin"); !errors.Is(err, errNoUndoTooLarge) {
		t.Errorf("err = %v, want errNoUndoTooLarge", err)
	}
	if _, err := os.Stat(big); err != nil {
		t.Errorf("the refused file is gone: %v", err)
	}
}

// TestKiroFSDeleteAtReadCapDeletes pins the boundary: fs/read_text_file serves exactly fsReadCap bytes.
func TestKiroFSDeleteAtReadCapDeletes(t *testing.T) {
	work := t.TempDir()
	edge := filepath.Join(work, "edge.bin")
	if err := os.WriteFile(edge, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(edge, fsReadCap); err != nil {
		t.Fatal(err)
	}
	if err := deleteThrough(t, work, "edge.bin"); err != nil {
		t.Fatalf("err = %v, want nil at exactly fsReadCap", err)
	}
	if _, err := os.Lstat(edge); !os.IsNotExist(err) {
		t.Errorf("lstat err = %v, want ErrNotExist", err)
	}
}

// TestKiroFSDeleteUnlinksLiveSymlinks pins that a delete removes the link KAS names, matching
// KAS's own fs.rm, and never the file or directory it points at, inside the workspace or not.
func TestKiroFSDeleteUnlinksLiveSymlinks(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	for _, f := range []string{filepath.Join(work, "dir", "f"), filepath.Join(work, "file"), filepath.Join(outside, "f")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, link, target, kept string }{
		{"to a directory", "to-dir", filepath.Join(work, "dir"), filepath.Join(work, "dir", "f")},
		{"to a file", "to-file", filepath.Join(work, "file"), filepath.Join(work, "file")},
		{"out of the workspace", "to-outside", filepath.Join(outside, "f"), filepath.Join(outside, "f")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(work, tc.link)
			if err := os.Symlink(tc.target, link); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			if err := deleteThrough(t, work, tc.link); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Errorf("lstat(link) err = %v, want ErrNotExist", err)
			}
			if _, err := os.Stat(tc.kept); err != nil {
				t.Errorf("the link's target lost its file: %v", err)
			}
		})
	}
}

// TestKiroFSDeleteUnlinksDanglingSymlink pins that the guard judges the link, never a target.
func TestKiroFSDeleteUnlinksDanglingSymlink(t *testing.T) {
	work := t.TempDir()
	link := filepath.Join(work, "link")
	if err := os.Symlink(filepath.Join(work, "missing"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := deleteThrough(t, work, "link"); err != nil {
		t.Fatalf("err = %v, want nil for a dangling symlink", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("lstat(link) err = %v, want ErrNotExist", err)
	}
}

func TestKiroFSDeleteUnlinksFIFO(t *testing.T) {
	work := t.TempDir()
	fifo := filepath.Join(work, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if err := deleteThrough(t, work, "pipe"); err != nil {
		t.Fatalf("err = %v, want nil for a FIFO", err)
	}
	if _, err := os.Lstat(fifo); !os.IsNotExist(err) {
		t.Errorf("lstat err = %v, want ErrNotExist", err)
	}
}

// TestKiroFSDeleteRefusesWorkDirRoot pins the refusal of a root itself.
func TestKiroFSDeleteRefusesWorkDirRoot(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "."))
	<-br.done
	// The sentinel, not any error: without the guard undoCopyRefusal refuses "." as a directory anyway.
	if !errors.Is(br.response.err, errRefusedRootDelete) {
		t.Fatalf("err = %v, want errRefusedRootDelete", br.response.err)
	}
	if _, err := os.Stat(filepath.Join(work, "keep.txt")); err != nil {
		t.Errorf("the workspace was touched anyway: %v", err)
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
