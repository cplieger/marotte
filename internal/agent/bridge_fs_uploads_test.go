package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// uploadsHub is a hub whose uploads folder is a temp dir holding one pasted text, the file
// a prompt names as `Attached file: <uploads>/paste-….txt`.
func uploadsHub(t *testing.T) (h *Runtime, br *respondingBridge, uploads, paste string) {
	t.Helper()
	uploads = canonDir(t, t.TempDir())
	paste = filepath.Join(uploads, "paste-2026-10-10T10-48-53.txt")
	if err := os.WriteFile(paste, []byte("pasted\ntext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br = hubForFSTest(t, canonDir(t, t.TempDir()))
	h.lifecycle.uploadsDir = uploads
	return h, br, uploads, paste
}

// The agent's read tool on an attachment marotte handed it: it failed with "path escapes
// workspace" while the agent's shell `cat` of the same path succeeded.
func TestRespondFSRead_ReadsAnUploadedAttachment(t *testing.T) {
	h, br, _, paste := uploadsHub(t)
	id := int64(1)
	h.inbound.respondFSRead(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id, Method: marotte.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": paste, "line": 2, "limit": 1}),
	})
	<-br.done
	if br.response.err != nil {
		t.Fatalf("read %s err = %v, want nil", paste, br.response.err)
	}
	if res, _ := br.response.result.(map[string]any); res["content"] != "text\n" {
		t.Errorf("content = %+v, want %q", br.response.result, "text\n")
	}
}

func TestKiroFSStatAndReadDirectory_SeeTheUploadsFolder(t *testing.T) {
	h, br, _, paste := uploadsHub(t)
	h.inbound.respondKiroFSStat(t.Context(), "c1", kiroFSMsg(t, 1, methodKiroFSStat, paste))
	<-br.done
	if body, ok := br.response.result.(kiroStatBody); br.response.err != nil || !ok || body.Type != fsTypeFile {
		t.Fatalf("stat %s = %+v err=%v, want a file", paste, br.response.result, br.response.err)
	}

	h, br, uploads, _ := uploadsHub(t)
	h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", kiroFSMsg(t, 2, methodKiroFSReadDirectory, uploads))
	<-br.done
	body, ok := br.response.result.(kiroReadDirBody)
	if br.response.err != nil || !ok || len(body.Entries) != 1 {
		t.Fatalf("read_directory %s = %+v err=%v, want the one paste", uploads, br.response.result, br.response.err)
	}
}

// The uploads folder is a READ grant: a write or a delete there still escapes the workspace.
func TestUploadsFolder_RefusesWriteAndDelete(t *testing.T) {
	h, br, _, paste := uploadsHub(t)
	id := int64(3)
	h.inbound.respondFSWrite(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id, Method: marotte.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": paste, "content": "overwritten"}),
	})
	<-br.done
	if br.response.err == nil {
		t.Error("write into the uploads folder err = nil, want refused")
	}

	h, br, _, paste = uploadsHub(t)
	h.inbound.respondKiroFSDelete(t.Context(), "c1", kiroFSMsg(t, 4, methodKiroFSDelete, paste))
	<-br.done
	if br.response.err == nil {
		t.Error("delete in the uploads folder err = nil, want refused")
	}
	if got, err := os.ReadFile(paste); err != nil || string(got) != "pasted\ntext\n" {
		t.Errorf("paste after refused write/delete = %q, %v; want it untouched", got, err)
	}
}

// A path outside both roots, or a relative one, still escapes; a symlink in the uploads
// folder pointing out of it is refused like one in the workspace.
func TestUploadsFolder_ConfinesLikeTheWorkspace(t *testing.T) {
	outside := canonDir(t, t.TempDir())
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(outsideSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(uploads string) string{
		"outside both roots": func(string) string { return secret },
		"dot-dot out":        func(u string) string { return u + "/../" + filepath.Base(outside) + "/secret.txt" },
		"relative name":      func(string) string { return "paste-2026-10-10T10-48-53.txt" },
		"symlink out": func(u string) string {
			link := filepath.Join(u, "link.txt")
			if err := os.Symlink(secret, link); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			return link
		},
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			h, br, uploads, _ := uploadsHub(t)
			id := int64(5)
			h.inbound.respondFSRead(t.Context(), "c1", &marotte.RPCResponse{
				ID: &id, Method: marotte.MethodFSRead,
				Params: mustJSON(t, map[string]any{"path": path(uploads)}),
			})
			<-br.done
			if br.response.err == nil {
				t.Errorf("read %s = %+v, want refused", path(uploads), br.response.result)
			}
		})
	}
}
