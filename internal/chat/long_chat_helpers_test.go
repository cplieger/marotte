package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The long chat of tests/perf/synth/fixtures.py: 19 turns per scale of 147 entries each, in its kind mix
// (tool_call 55%, tool_result 23%, thinking 14%, text 2%, steer 3%), about 3.5 MB of log at x1.
const (
	longChatTurnsPerScale = 19
	longChatPerTurn       = 147
)

var synthWords = strings.Fields("the server reads each chat log once and the client renders every row it receives " +
	"so a long transcript costs what the log holds rather than what the screen shows today state store event " +
	"frame budget cursor index patch review gate lane step build test lint deadset coverage branch")

type synthText struct{ r *rand.Rand }

func (g synthText) words(n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = synthWords[g.r.IntN(len(synthWords))]
	}
	return strings.Join(w, " ")
}

func (g synthText) para(lo, hi int) string {
	s := g.words(lo + g.r.IntN(hi-lo+1))
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func (g synthText) markdown() string {
	parts := []string{"## " + g.words(4), g.para(30, 60)}
	items := make([]string, 2+g.r.IntN(4))
	for i := range items {
		items[i] = "- " + g.para(6, 14)
	}
	parts = append(parts, strings.Join(items, "\n"))
	if g.r.Float64() < 0.6 {
		var body strings.Builder
		for k := range 3 + g.r.IntN(10) {
			fmt.Fprintf(&body, "\tx%d := read(r)\n", k)
		}
		parts = append(parts, fmt.Sprintf("```go\nfunc handle%d(w http.ResponseWriter, r *http.Request) {\n%s}\n```",
			g.r.IntN(1000), body.String()))
	}
	return strings.Join(append(parts, g.para(20, 50)), "\n\n")
}

// toolShape is one tool call's title, kind, input and full output, in fixtures.py's tool mix.
func (g synthText) toolShape(k int) (title, kind string, input map[string]string, output string) {
	path := fmt.Sprintf("/workspace/app/internal/pkg%02d/file%03d.go", g.r.IntN(40), k%997)
	var out strings.Builder
	switch x := g.r.Float64(); {
	case x <= 0.45:
		out.WriteString("package pkg\n\n")
		for i := range 20 + g.r.IntN(41) {
			fmt.Fprintf(&out, "// %s\nfunc f%d() int { return %d }\n", g.words(8), i, i)
		}
		return "Read File", "read", map[string]string{"path": path}, out.String()
	case x <= 0.70:
		for i := range 10 + g.r.IntN(31) {
			fmt.Fprintf(&out, "=== RUN   TestCase%d\n--- PASS: TestCase%d (0.0%ds)\n", i, i, i%9)
		}
		out.WriteString("PASS\nok  \tapp/internal\t0.412s\n")
		return "Shell", "execute", map[string]string{"command": "cd /workspace/app && go test ./internal/..."}, out.String()
	case x <= 0.85:
		for i := range 5 + g.r.IntN(26) {
			fmt.Fprintf(&out, "%s:%d: %s\n", path, i*7, g.words(9))
		}
		return "Grep", "search", map[string]string{"pattern": g.words(2), "path": "/workspace/app"}, out.String()
	default:
		return "Edit File", "edit", map[string]string{"path": path, "oldStr": g.words(12), "newStr": g.words(14)}, "Edited " + path
	}
}

type pendingCall struct{ id, title, kind, output string }

// longChat is the entries of fixtures.py's long chat at scale, every turn closed, seeded so each run builds the same
// bytes. It names the newest turn and, in it, a tool call whose result is a separate entry.
type longChat struct {
	entries  []marotte.Entry
	newest   string
	toolCall string
}

func buildLongChat(scale int) longChat {
	g := synthText{rand.New(rand.NewPCG(11, uint64(scale)))}
	var lc longChat
	for t := 1; t <= longChatTurnsPerScale*scale; t++ {
		turn := fmt.Sprintf("t-longchat-%05d", t)
		seq := uint64(0)
		add := func(id string, kind marotte.EntryKind, payload any) {
			raw, err := json.Marshal(payload)
			if err != nil {
				panic(err)
			}
			if id == "" {
				id = fmt.Sprintf("%s-%d", turn, seq)
			}
			lc.entries = append(lc.entries, marotte.Entry{ID: id, Turn: turn, Kind: kind, Payload: raw, Seq: seq, Ts: int64(t)*60000 + int64(seq)})
			seq++
		}
		add(turn, marotte.EntryKindTurnOpen, map[string]any{
			"prompt": map[string]string{"id": fmt.Sprintf("m-%016x", g.r.Uint64()), "text": g.para(20, 120)},
			"source": "prompt", "n": t,
		})
		var pending []pendingCall
		for range longChatPerTurn - 2 {
			switch x := g.r.Float64(); {
			case x < 0.55:
				title, kind, input, output := g.toolShape(len(lc.entries))
				call := fmt.Sprintf("toolu_%08x_%05d", g.r.Uint32(), len(lc.entries))
				p := map[string]any{
					"id": call, "title": title, "kind": kind, "input": input,
					"locations": []map[string]string{{"path": input["path"]}},
				}
				if g.r.Float64() >= 0.42 {
					p["status"], p["output"], p["duration_ms"] = "completed", output[:min(len(output), 1500)], 20+g.r.IntN(9000)
				} else {
					p["status"] = "pending"
					pending = append(pending, pendingCall{call, title, kind, output})
				}
				add(call, marotte.EntryKindToolCall, p)
			case x < 0.78 && len(pending) > 0:
				c := pending[0]
				pending = pending[1:]
				addResult(add, c, 20+g.r.IntN(9000))
				if t == longChatTurnsPerScale*scale && lc.toolCall == "" {
					lc.toolCall = c.id
				}
			case x < 0.92:
				paras := make([]string, 1+g.r.IntN(3))
				for i := range paras {
					paras[i] = g.para(20, 70)
				}
				add("", marotte.EntryKindThinking, map[string]string{"text": strings.Join(paras, "\n\n")})
			case x < 0.95:
				add("", marotte.EntryKindSteer, map[string]any{"text": g.para(6, 30), "origin": "user", "state": "read"})
			default:
				add("", marotte.EntryKindText, map[string]string{"text": g.markdown()})
			}
		}
		for _, c := range pending {
			addResult(add, c, 0)
		}
		add("", marotte.EntryKindText, map[string]string{"text": g.markdown()})
		add(turn+":close", marotte.EntryKindTurnClose, map[string]any{
			"outcome": "completed", "stop_reason_raw": "end_turn", "model": "model-a", "elapsed_ms": 20000 + g.r.IntN(880000),
		})
		lc.newest = turn
	}
	return lc
}

func addResult(add func(string, marotte.EntryKind, any), c pendingCall, durationMS int) {
	add(marotte.ToolResultID(c.id), marotte.EntryKindToolResult, map[string]any{
		"title": c.title, "kind": c.kind, "status": "completed", "output": c.output, "output_bytes": len(c.output),
		"duration_ms": durationMS,
	})
}

// longChatStore is a store over a fresh directory holding the long chat at scale as chat id, written as the fixture
// writes it: a header, then the whole log in one write, which the store's first read scans.
func longChatStore(t *testing.T, id marotte.ChatID, scale int) (*Store, longChat) {
	t.Helper()
	dir := t.TempDir()
	seed, err := NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore: %v", err)
	}
	if _, err := seed.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "Long transcript"
		return true
	}); err != nil {
		t.Fatalf("Setup: write the header: %v", err)
	}
	lc := buildLongChat(scale)
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for i := range lc.entries {
		if err := enc.Encode(&lc.entries[i]); err != nil {
			t.Fatalf("Setup: encode entry %d: %v", i, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, string(id), EntriesFileName), log.Bytes(), 0o600); err != nil {
		t.Fatalf("Setup: write the log: %v", err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: reopen the store: %v", err)
	}
	return s, lc
}
