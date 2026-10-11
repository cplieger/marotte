package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
)

// specRecorder swaps the Notifier for a zero-window one that hands each directory to a channel, so tests wait on the mark.
func specRecorder(h *Runtime) <-chan string {
	dirs := make(chan string, 8)
	n := spec.NewNotifier(0, func(dir string) { dirs <- dir })
	h.specs = n
	h.inbound.specs = n
	return dirs
}

func awaitSpecMark(t *testing.T, dirs <-chan string) string {
	t.Helper()
	select {
	case dir := <-dirs:
		return dir
	case <-time.After(10 * time.Second):
		t.Fatal("no spec_changed mark arrived")
		return ""
	}
}

func fsWriteMsg(t *testing.T, id int64, path string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		ID:     &id,
		Method: marotte.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"sessionId": "sess_x", "path": path, "content": "- [ ] 1 one\n"}),
	}
}

func checkpointMsg(t *testing.T, artifactPath string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		Method: methodV3SpecPhaseCheckpoint,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x", "featureName": "x", "phase": "tasks", "artifactPath": artifactPath,
		}),
	}
}

// The non-marking operation runs first, so a wrong mark could only come from it.
func TestSpecProducers_MarkTheSpecDirectory(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, h *Runtime, work string)
		want string
	}{
		{
			name: "fs_write_marks_the_spec_dir_and_not_a_source_file",
			run: func(t *testing.T, h *Runtime, _ string) {
				h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), fsWriteMsg(t, 1, "src/a.go"))
				h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), fsWriteMsg(t, 2, ".kiro/specs/x/tasks.md"))
			},
			want: ".kiro/specs/x",
		},
		{
			name: "kiro_fs_delete_of_a_sidecar_marks_the_spec_dir",
			run: func(t *testing.T, h *Runtime, work string) {
				for _, p := range []string{"src/b.go", ".kiro/specs/x/tasks.meta.json"} {
					full := filepath.Join(work, p)
					if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
						t.Fatalf("Setup: mkdir %s: %v", p, err)
					}
					if err := os.WriteFile(full, []byte("{}"), 0o600); err != nil {
						t.Fatalf("Setup: write %s: %v", p, err)
					}
				}
				h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 1, methodKiroFSDelete, "src/b.go"))
				h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 2, methodKiroFSDelete, ".kiro/specs/x/tasks.meta.json"))
			},
			want: ".kiro/specs/x",
		},
		{
			name: "phase_checkpoint_absolute_path",
			run: func(t *testing.T, h *Runtime, work string) {
				h.translateACPEvent("c1", h.originOf("c1"), checkpointMsg(t, filepath.Join(t.TempDir(), ".kiro/specs/elsewhere/design.md")))
				h.translateACPEvent("c1", h.originOf("c1"), checkpointMsg(t, filepath.Join(work, "myrepo/.kiro/specs/y/design.md")))
			},
			want: "myrepo/.kiro/specs/y",
		},
		{
			name: "phase_checkpoint_cwd_relative_path",
			run: func(t *testing.T, h *Runtime, _ string) {
				h.translateACPEvent("c1", h.originOf("c1"), checkpointMsg(t, "../outside/.kiro/specs/z/tasks.md"))
				h.translateACPEvent("c1", h.originOf("c1"), checkpointMsg(t, "src/main.go"))
				h.translateACPEvent("c1", h.originOf("c1"), checkpointMsg(t, ".kiro/specs/z/tasks.md"))
			},
			want: ".kiro/specs/z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			h, _ := hubForFSTest(t, work)
			dirs := specRecorder(h)
			tc.run(t, h, work)
			if got := awaitSpecMark(t, dirs); got != tc.want {
				t.Errorf("spec mark = %q, want %q", got, tc.want)
			}
			select {
			case extra := <-dirs:
				t.Errorf("a second spec mark %q arrived, want exactly one", extra)
			default:
			}
		})
	}
}

// A refused write (a path escaping the workspace) marks nothing; the later good write is the one mark.
func TestSpecProducers_ARefusedWriteMarksNothing(t *testing.T) {
	work := t.TempDir()
	h, _ := hubForFSTest(t, work)
	dirs := specRecorder(h)
	h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), fsWriteMsg(t, 1, "../.kiro/specs/x/tasks.md"))
	h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), fsWriteMsg(t, 2, ".kiro/specs/ok/tasks.md"))
	if got := awaitSpecMark(t, dirs); got != ".kiro/specs/ok" {
		t.Errorf("spec mark = %q, want .kiro/specs/ok", got)
	}
}
