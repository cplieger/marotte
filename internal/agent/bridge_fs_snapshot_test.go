package agent

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// snapshotFixture is a KIRO_HOME holding two runs' directories as KAS writes them, and a hub whose router knows
// sess_step_a as a step of wf_a and sess_step_b as one of wf_b.
type snapshotFixture struct {
	h       *Runtime
	br      *respondingBridge
	home    string
	runA    string
	runB    string
	outside string
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	tmp := canonDir(t, t.TempDir())
	t.Setenv("HOME", tmp)
	work := canonDir(t, t.TempDir())
	f := &snapshotFixture{home: filepath.Join(tmp, ".kiro"), outside: filepath.Join(tmp, "outside.txt")}
	workflows := filepath.Join(f.home, "sessions", workspace.SessionsBucket(work), "workflows")
	f.runA, f.runB = filepath.Join(workflows, "wf_a"), filepath.Join(workflows, "wf_b")
	for path, body := range map[string]string{
		filepath.Join(f.runA, "edit-snapshot-r1.md"):                                                      "line one\nline two\nline three\n",
		filepath.Join(f.runA, "edit-replaced-r1.json"):                                                    `{"replaced":["coder"]}`,
		filepath.Join(f.runA, "workflow-state.json"):                                                      `{"status":"running"}`,
		filepath.Join(f.runB, "edit-snapshot-r1.md"):                                                      "run b's snapshot\n",
		filepath.Join(f.home, "settings", "cli.json"):                                                     `{"chat.defaultModel":"x"}`,
		filepath.Join(f.home, "sessions", "0000000000000000", "workflows", "wf_a", "edit-snapshot-r1.md"): "another workspace\n",
		f.outside: "outside the run\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(f.outside, filepath.Join(f.runA, "edit-snapshot-r2.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("workflow-state.json", filepath.Join(f.runA, "edit-snapshot-r3.md")); err != nil {
		t.Fatal(err)
	}
	f.h, f.br = hubForFSTest(t, work)
	f.h.router.seedSteps("wf_a", []string{"sess_step_a"})
	f.h.router.seedSteps("wf_b", []string{"sess_step_b"})
	return f
}

func fsMsg(t *testing.T, method, session, path string, extra map[string]any) *marotte.RPCResponse {
	t.Helper()
	id := int64(1)
	params := map[string]any{"path": path}
	if session != "" {
		params["sessionId"] = session
	}
	maps.Copy(params, extra)
	return &marotte.RPCResponse{ID: &id, Method: method, Params: mustJSON(t, params)}
}

func (f *snapshotFixture) read(t *testing.T, session, path string, extra map[string]any) (string, error) {
	t.Helper()
	f.h.inbound.respondFSRead(t.Context(), "c1", f.h.originOf("c1"), fsMsg(t, marotte.MethodFSRead, session, path, extra))
	<-f.br.done
	res, _ := f.br.response.result.(map[string]any)
	content, _ := res["content"].(string)
	return content, f.br.response.err
}

// The workflow creator step reads the edit snapshot KAS wrote into its run's directory and named in its brief.
func TestRespondFSRead_AStepReadsItsRunsEditSnapshot(t *testing.T) {
	t.Run("the snapshot in windows", func(t *testing.T) {
		f := newSnapshotFixture(t)
		got, err := f.read(t, "sess_step_a", filepath.Join(f.runA, "edit-snapshot-r1.md"), map[string]any{"line": 2, "limit": 1})
		if err != nil || got != "line two\n" {
			t.Errorf("read of its run's snapshot = %q, %v; want %q", got, err, "line two\n")
		}
	})
	t.Run("the replaced-nodes file", func(t *testing.T) {
		f := newSnapshotFixture(t)
		got, err := f.read(t, "sess_step_a", filepath.Join(f.runA, "edit-replaced-r1.json"), nil)
		if err != nil || got != `{"replaced":["coder"]}` {
			t.Errorf("read of its run's replaced file = %q, %v; want the file", got, err)
		}
	})
	t.Run("a KIRO_HOME reached through a link", func(t *testing.T) {
		f := newSnapshotFixture(t)
		linked := filepath.Join(canonDir(t, t.TempDir()), "home")
		if err := os.Symlink(filepath.Dir(f.home), linked); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", linked)
		rel, err := filepath.Rel(f.home, f.runA)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.read(t, "sess_step_a", filepath.Join(linked, ".kiro", rel, "edit-snapshot-r1.md"), nil)
		if err != nil || got != "line one\nline two\nline three\n" {
			t.Errorf("read through a linked home = %q, %v; want the snapshot", got, err)
		}
	})
	t.Run("a stat of the snapshot", func(t *testing.T) {
		f := newSnapshotFixture(t)
		f.h.inbound.respondKiroFSStat(t.Context(), "c1", f.h.originOf("c1"),
			fsMsg(t, methodKiroFSStat, "sess_step_a", filepath.Join(f.runA, "edit-snapshot-r1.md"), nil))
		<-f.br.done
		if body, ok := f.br.response.result.(kiroStatBody); f.br.response.err != nil || !ok || body.Type != fsTypeFile {
			t.Errorf("stat of its run's snapshot = %+v, %v; want a file", f.br.response.result, f.br.response.err)
		}
	})
	t.Run("a snapshot never written", func(t *testing.T) {
		f := newSnapshotFixture(t)
		missing := filepath.Join(f.runA, "edit-snapshot-r9.md")
		if got, err := f.read(t, "sess_step_a", missing, nil); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("read of an unwritten snapshot = %q, %v; want not found", got, err)
		}
		f.h.inbound.respondKiroFSStat(t.Context(), "c1", f.h.originOf("c1"), fsMsg(t, methodKiroFSStat, "sess_step_a", missing, nil))
		<-f.br.done
		if !errors.Is(f.br.response.err, os.ErrNotExist) {
			t.Errorf("stat of an unwritten snapshot = %+v, %v; want not found", f.br.response.result, f.br.response.err)
		}
	})
}

// The admission is that run's snapshot files, read-only, for that run's steps; everything else under KIRO_HOME
// still escapes the workspace.
func TestRespondFSRead_TheSnapshotGrantGoesNoFurther(t *testing.T) {
	type read struct {
		name, session string
		path          func(f *snapshotFixture) string
	}
	for _, tc := range []read{
		{
			name: "another run's snapshot", session: "sess_step_a",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runB, "edit-snapshot-r1.md") },
		},
		{
			name: "its run's state file", session: "sess_step_a",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runA, "workflow-state.json") },
		},
		{
			name: "a session that is no step", session: "sess_chat",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runA, "edit-snapshot-r1.md") },
		},
		{
			name: "a request naming no session",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runA, "edit-snapshot-r1.md") },
		},
		{
			name: "the same name under another workspace's bucket", session: "sess_step_a",
			path: func(f *snapshotFixture) string {
				return filepath.Join(f.home, "sessions", "0000000000000000", "workflows", "wf_a", "edit-snapshot-r1.md")
			},
		},
		{
			name: "another KIRO_HOME file", session: "sess_step_a",
			path: func(f *snapshotFixture) string { return filepath.Join(f.home, "settings", "cli.json") },
		},
		{
			name: "a dot-dot spelling from another run's directory", session: "sess_step_b",
			path: func(f *snapshotFixture) string { return f.runB + "/../wf_a/edit-snapshot-r1.md" },
		},
		{
			name: "a snapshot name linking out of the run", session: "sess_step_a",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runA, "edit-snapshot-r2.md") },
		},
		{
			name: "a snapshot name linking to its run's state file", session: "sess_step_a",
			path: func(f *snapshotFixture) string { return filepath.Join(f.runA, "edit-snapshot-r3.md") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSnapshotFixture(t)
			if got, err := f.read(t, tc.session, tc.path(f), nil); err == nil {
				t.Errorf("read %s = %q, want refused", tc.path(f), got)
			}
		})
	}
	t.Run("its run's directory linking to another run's", func(t *testing.T) {
		f := newSnapshotFixture(t)
		if err := os.RemoveAll(f.runA); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(f.runB, f.runA); err != nil {
			t.Fatal(err)
		}
		if got, err := f.read(t, "sess_step_a", filepath.Join(f.runA, "edit-snapshot-r1.md"), nil); err == nil {
			t.Errorf("read through a linked run directory = %q, want refused", got)
		}
	})
	t.Run("a directory above the run linking elsewhere", func(t *testing.T) {
		f := newSnapshotFixture(t)
		workflows := filepath.Dir(f.runA)
		if err := os.RemoveAll(workflows); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(f.home, "sessions", "0000000000000000", "workflows"), workflows); err != nil {
			t.Fatal(err)
		}
		if got, err := f.read(t, "sess_step_a", filepath.Join(f.runA, "edit-snapshot-r1.md"), nil); err == nil {
			t.Errorf("read through a linked workflows directory = %q, want refused", got)
		}
	})
	t.Run("a stat of a snapshot name linking to its run's state file", func(t *testing.T) {
		f := newSnapshotFixture(t)
		f.h.inbound.respondKiroFSStat(t.Context(), "c1", f.h.originOf("c1"),
			fsMsg(t, methodKiroFSStat, "sess_step_a", filepath.Join(f.runA, "edit-snapshot-r3.md"), nil))
		<-f.br.done
		if f.br.response.err == nil {
			t.Errorf("stat through an in-run link = %+v, want refused", f.br.response.result)
		}
	})
	t.Run("a write over the snapshot", func(t *testing.T) {
		f := newSnapshotFixture(t)
		snap := filepath.Join(f.runA, "edit-snapshot-r1.md")
		f.h.inbound.respondFSWrite(t.Context(), "c1", f.h.originOf("c1"),
			fsMsg(t, marotte.MethodFSWrite, "sess_step_a", snap, map[string]any{"content": "overwritten"}))
		<-f.br.done
		if f.br.response.err == nil {
			t.Error("write over its run's snapshot err = nil, want refused")
		}
		if got, _ := os.ReadFile(snap); string(got) != "line one\nline two\nline three\n" {
			t.Errorf("snapshot after a refused write = %q, want it untouched", got)
		}
	})
	t.Run("a delete of the snapshot", func(t *testing.T) {
		f := newSnapshotFixture(t)
		snap := filepath.Join(f.runA, "edit-snapshot-r1.md")
		f.h.inbound.respondKiroFSDelete(t.Context(), "c1", f.h.originOf("c1"), fsMsg(t, methodKiroFSDelete, "sess_step_a", snap, nil))
		<-f.br.done
		if _, err := os.Stat(snap); f.br.response.err == nil || err != nil {
			t.Errorf("delete of its run's snapshot err = %v, file err = %v; want refused and the file kept", f.br.response.err, err)
		}
	})
	t.Run("a listing of the run's directory", func(t *testing.T) {
		f := newSnapshotFixture(t)
		f.h.inbound.respondKiroFSReadDirectory(t.Context(), "c1", f.h.originOf("c1"),
			fsMsg(t, methodKiroFSReadDirectory, "sess_step_a", f.runA, nil))
		<-f.br.done
		if f.br.response.err == nil {
			t.Errorf("read_directory of its run's directory = %+v, want refused", f.br.response.result)
		}
	})
}
