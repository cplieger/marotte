package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// agentRewrite writes out.txt at perm, rewrites it through
// fs/write_text_file, and returns the mode the rewritten file carries.
func agentRewrite(t *testing.T, perm os.FileMode) os.FileMode {
	t.Helper()
	work := t.TempDir()
	path := filepath.Join(work, "out.txt")
	if err := os.WriteFile(path, []byte("old"), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(9)
	msg := &marotte.RPCResponse{
		ID:     &id,
		Method: marotte.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "out.txt", "content": "new"}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", msg)
	<-br.done
	if br.response.err != nil {
		t.Fatalf("fs/write_text_file over a %#o file: %v", perm, br.response.err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestRespondFSWrite_KeepsAGroupReadableMode(t *testing.T) {
	if got := agentRewrite(t, 0o640); got != 0o640 {
		t.Errorf("rewriting a 0640 file left mode %#o, want 0640", got)
	}
}

func TestRespondFSWrite_EnforcesAnOwnerOnlyModeWithoutAChmod(t *testing.T) {
	var chmods int
	orig := chmodInRoot
	chmodInRoot = func(r *os.Root, name string, mode os.FileMode) error {
		chmods++
		return orig(r, name, mode)
	}
	t.Cleanup(func() { chmodInRoot = orig })

	if got := agentRewrite(t, 0o600); got != 0o600 {
		t.Errorf("rewriting a 0600 file left mode %#o, want 0600", got)
	}
	if chmods != 0 {
		t.Errorf("rewriting a 0600 file ran %d post-write chmods, want 0: the mode must be enforced on the write itself", chmods)
	}
}
