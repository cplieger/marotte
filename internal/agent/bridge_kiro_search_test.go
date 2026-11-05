package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
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
func TestKiroSearch_TurnCancelEndsTheSearch(t *testing.T) {
	stageRipgrep(t, `printf '%s\n' '`+textMatchEvent+`'
/bin/sleep 60`)
	h, br := hubForFSTest(t, t.TempDir())
	msg := kiroSearchMsg(t, 1, methodKiroTextSearch, map[string]any{"sessionId": "s", "pattern": "func Run", "caseSensitive": false})
	go h.inbound.respondKiroSearch(t.Context(), "c1", h.originOf("c1"), msg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.inbound.bus.searches.mu.Lock()
		running := len(h.inbound.bus.searches.byChat["c1"])
		h.inbound.bus.searches.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the search never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // lets the fake rg print its match before the cancel

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
