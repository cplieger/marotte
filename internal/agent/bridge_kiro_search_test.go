package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func stageRipgrep(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rg")
	syscall.ForkLock.RLock()
	err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	prev := resolveRipgrep
	resolveRipgrep = func() (string, bool) { return p, true }
	t.Cleanup(func() { resolveRipgrep = prev })
}

func kiroSearchMsg(t *testing.T, id int64, method string, params any) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{ID: &id, Method: method, Params: mustJSON(t, params)}
}

func awaitSearchAnswer(t *testing.T, br *respondingBridge) (string, error) {
	t.Helper()
	select {
	case <-br.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the search was never answered")
	}
	br.respMu.Lock()
	defer br.respMu.Unlock()
	raw, err := json.Marshal(br.response.result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), br.response.err
}

const textMatchEvent = `{"type":"match","data":{"path":{"text":"./repo/a.go"},"lines":{"text":"func Run() {}\n"},"line_number":12}}`

// The answer shapes are testdata/kas-probe/search-text.json's and search-files.json's.
func TestKiroSearch_AnswersInTheProbedShape(t *testing.T) {
	stageRipgrep(t, `case "$1" in
--files) echo ./repo/build.go ;;
*) printf '%s\n' '`+textMatchEvent+`' ;;
esac`)
	tests := map[string]struct {
		method string
		params map[string]any
		want   string
	}{
		"text_search": {
			methodKiroTextSearch,
			map[string]any{"sessionId": "sess_1", "pattern": "func Run", "caseSensitive": false},
			`{"matches":[{"filePath":"repo/a.go","lineText":"func Run() {}","lineNumber":12,"isContext":false}]}`,
		},
		"find_files": {
			methodKiroFindFiles,
			map[string]any{"sessionId": "sess_1", "pattern": "**/*build.go*", "maxResults": 11},
			`{"files":["repo/build.go"]}`,
		},
		"undecodable text_search": {
			methodKiroTextSearch, map[string]any{"pattern": 7}, `{"matches":[]}`,
		},
		"undecodable find_files": {
			methodKiroFindFiles, map[string]any{"maxResults": "x"}, `{"files":[]}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			h.inbound.respondKiroSearch(t.Context(), "c1", h.originOf("c1"), kiroSearchMsg(t, 1, tc.method, tc.params))
			got, err := awaitSearchAnswer(t, br)
			if err != nil {
				t.Fatalf("answer error = %v, want a result: KAS falls back to its walker on an error", err)
			}
			if got != tc.want {
				t.Errorf("answer = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKiroSearch_NoRipgrepAnswersEmpty(t *testing.T) {
	prev := resolveRipgrep
	resolveRipgrep = func() (string, bool) { return "", false }
	t.Cleanup(func() { resolveRipgrep = prev })
	h, br := hubForFSTest(t, t.TempDir())

	h.inbound.respondKiroSearch(t.Context(), "c1", h.originOf("c1"),
		kiroSearchMsg(t, 1, methodKiroFindFiles, map[string]any{"sessionId": "s", "pattern": "**/*x*", "maxResults": 11}))

	if got, err := awaitSearchAnswer(t, br); err != nil || got != `{"files":[]}` {
		t.Errorf("answer = (%s, %v), want {\"files\":[]}", got, err)
	}
}

// A turn cancel ends the chat's running search, which still answers with what it found.
// stageBlockingRipgrep stages an rg that prints textMatchEvent, starts a child that would outlive
// the search, writes the child's pid to pids and a line to ready, then waits: a search with a line
// in ready is tracked and has its match in the pipe.
func stageBlockingRipgrep(t *testing.T) (ready, pids string) {
	t.Helper()
	dir := t.TempDir()
	ready, pids = filepath.Join(dir, "ready"), filepath.Join(dir, "pids")
	stageRipgrep(t, `printf '%s\n' '`+textMatchEvent+`'
/bin/sleep 60 &
echo $! >> `+pids+`
echo x >> `+ready+`
wait`)
	return ready, pids
}

// awaitLines waits until path holds n lines.
func awaitLines(t *testing.T, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(path)
		if strings.Count(string(raw), "\n") >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d searches started", strings.Count(string(raw), "\n"), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startSearch runs one text_search from sessionID arriving on origin; the channel closes when it returns.
func startSearch(t *testing.T, h *Runtime, origin acpResponder, sessionID string, id int64) <-chan struct{} {
	t.Helper()
	msg := kiroSearchMsg(t, id, methodKiroTextSearch, map[string]any{"sessionId": sessionID, "pattern": "func Run", "caseSensitive": false})
	const chatID = "c1"
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.inbound.respondKiroSearch(t.Context(), chatID, origin, msg)
	}()
	return done
}

func awaitReturn(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not end the search well before its deadline", what)
	}
}

func awaitChildrenGone(t *testing.T, pids string) {
	t.Helper()
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pid := range strings.FieldsSeq(string(raw)) {
		for {
			stat, err := os.ReadFile("/proc/" + pid + "/stat") // #nosec G304 -- pid from the test's own fake rg
			_, state, _ := strings.Cut(string(stat), ") ")
			if err != nil || strings.HasPrefix(state, "Z") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("rg's child %s outlived the search", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestKiroSearch_TurnCancelEndsTheSearch(t *testing.T) {
	ready, _ := stageBlockingRipgrep(t)
	h, br := hubForFSTest(t, t.TempDir())
	startSearch(t, h, h.originOf("c1"), "s", 1)
	awaitLines(t, ready, 1)

	start := time.Now()
	if err := h.inbound.bus.CancelPendingAsks(t.Context(), "c1", command.ScopeChatSession); err != nil {
		t.Fatal(err)
	}
	got, err := awaitSearchAnswer(t, br)

	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("answer came %v after the cancel, want well before the search deadline", took)
	}
	if want := `{"matches":[{"filePath":"repo/a.go","lineText":"func Run() {}","lineNumber":12,"isContext":false}]}`; err != nil || got != want {
		t.Errorf("answer = (%s, %v), want (%s, nil)", got, err, want)
	}
}

// A chat-parented run's step searches arrive on the chat's own session; a cancel of the chat's turn
// alone leaves them running, and the run's end ends them.
func TestKiroSearch_ChatCancelLeavesItsRunsSearches(t *testing.T) {
	ready, _ := stageBlockingRipgrep(t)
	h, _ := hubForFSTest(t, t.TempDir())
	h.inbound.stepRun = func(id marotte.SessionID) (string, bool) { return "wf_1", id == "sess_step" }
	chatSearch := startSearch(t, h, h.originOf("c1"), "sess_chat", 1)
	stepSearch := startSearch(t, h, h.originOf("c1"), "sess_step", 2)
	awaitLines(t, ready, 2)

	if err := h.inbound.bus.CancelPendingAsks(t.Context(), "c1", command.ScopeChatSession); err != nil {
		t.Fatal(err)
	}
	awaitReturn(t, chatSearch, "the chat's turn cancel")
	select {
	case <-stepSearch:
		t.Fatal("the chat's turn cancel ended its run step's search")
	case <-time.After(500 * time.Millisecond):
	}

	h.inbound.bus.ClearPendingPermsForRun(t.Context(), "wf_1")
	awaitReturn(t, stepSearch, "the run's end")
}

func TestKiroSearch_ChatAndRunsCancelEndsEverySearch(t *testing.T) {
	ready, _ := stageBlockingRipgrep(t)
	h, _ := hubForFSTest(t, t.TempDir())
	h.inbound.stepRun = func(id marotte.SessionID) (string, bool) { return "wf_1", id == "sess_step" }
	chatSearch := startSearch(t, h, h.originOf("c1"), "sess_chat", 1)
	stepSearch := startSearch(t, h, h.originOf("c1"), "sess_step", 2)
	awaitLines(t, ready, 2)

	if err := h.inbound.bus.CancelPendingAsks(t.Context(), "c1", command.ScopeChatAndRuns); err != nil {
		t.Fatal(err)
	}
	awaitReturn(t, chatSearch, "a cancel of the chat and its runs")
	awaitReturn(t, stepSearch, "a cancel of the chat and its runs")
}

// Nothing can receive a search's answer once its session ends, so the search ends with it.
func TestKiroSearch_SessionEndEndsTheSearch(t *testing.T) {
	ready, pids := stageBlockingRipgrep(t)
	h, br := hubForFSTest(t, t.TempDir())
	// The session c1's forward runs on: the engine hands out every session guarded.
	search := startSearch(t, h, guardSession(br), "sess_chat", 1)
	awaitLines(t, ready, 1)

	h.coord.closeBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)

	awaitReturn(t, search, "the session's end")
	awaitChildrenGone(t, pids)
}

// KAS resolves an answer's paths against the roots its session opened with, so a session opened
// with a directory marotte no longer grants still has its paths written for those roots, and the
// directory is not searched.
func TestKiroSearch_PathsFollowTheSessionsOwnRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work, notes := t.TempDir(), filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(home, ".kiro", "sessions", "0123456789abcdef", "sess_old", "session.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, mustJSON(t, map[string]any{"workspacePaths": []string{work, notes}}), 0o600); err != nil {
		t.Fatal(err)
	}
	cwds := filepath.Join(t.TempDir(), "cwds")
	stageRipgrep(t, `pwd >> `+cwds+`
printf '%s\n' '{"type":"match","data":{"path":{"text":"./notes/x.md"},"lines":{"text":"hit\n"},"line_number":1}}'`)
	h, br := hubForFSTest(t, work)

	h.inbound.respondKiroSearch(t.Context(), "c1", h.originOf("c1"),
		kiroSearchMsg(t, 1, methodKiroTextSearch, map[string]any{"sessionId": "sess_old", "pattern": "hit", "caseSensitive": false}))
	got, err := awaitSearchAnswer(t, br)

	// "notes/x.md" alone would name notes/x.md in KAS's second root.
	want := `{"matches":[{"filePath":"` + filepath.Base(work) + `/notes/x.md","lineText":"hit","lineNumber":1,"isContext":false}]}`
	if err != nil || got != want {
		t.Errorf("answer = (%s, %v), want (%s, nil)", got, err, want)
	}
	if raw, _ := os.ReadFile(cwds); strings.TrimSpace(string(raw)) != work {
		t.Errorf("rg ran in %q, want only the workspace %q: the other root is no longer granted", raw, work)
	}
}
