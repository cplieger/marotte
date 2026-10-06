package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
)

// bareTerminals is a registry with no collaborators, for bookkeeping-only tests; reaching
// a collaborator nil-panics. Without a turn reader every terminal is collected at the next close.
func bareTerminals() *agentTerminals { return newAgentTerminals(nil, nil, nil, nil) }

func TestRingBuffer(t *testing.T) {
	r := newByteRing(10)
	r.Write([]byte("hello"))
	if r.String() != "hello" {
		t.Errorf("got %q, want %q", r.String(), "hello")
	}
	r.Write([]byte(" world!"))
	got := r.String()
	if len(got) > 10 {
		t.Errorf("buffer exceeded limit: len=%d", len(got))
	}
	if got != "lo world!" && got != "o world!" && got != " world!" {
		// The exact trim depends on UTF-8 boundary advancement.
		t.Logf("ring buffer output: %q (len=%d)", got, len(got))
	}
}

func TestRingBufferUTF8(t *testing.T) {
	r := newByteRing(8)
	r.Write([]byte("aaaaaé"))
	got := r.String()
	if len(got) > 8 {
		t.Errorf("buffer exceeded limit: len=%d, content=%q", len(got), got)
	}
}

func TestAgentTerminalsNewAndLookup(t *testing.T) {
	terms := bareTerminals()
	if len(terms.terms) != 0 {
		t.Errorf("expected empty, got %d", len(terms.terms))
	}
	terms.terms["test"] = newAgentTerminal(nil, "", 64)
	if _, ok := terms.terms["test"]; !ok {
		t.Error("expected to find terminal 'test'")
	}
}

func TestRingBuffer_StoresBoundedByteCount(t *testing.T) {
	t.Parallel()
	r := newByteRing(20)
	if got := len(r.Bytes()); got != 0 {
		t.Errorf("empty byteRing stored bytes = %d, want 0", got)
	}
	r.Write([]byte("hello"))
	if got := len(r.Bytes()); got != 5 {
		t.Errorf("byteRing stored bytes after 'hello' = %d, want 5", got)
	}
	r.Write([]byte(" world, this is long"))
	if got := len(r.Bytes()); got > 20 {
		t.Errorf("byteRing stored bytes = %d, exceeds limit 20", got)
	}
}

func TestParseRequest_NilParamsReturnsError(t *testing.T) {
	t.Parallel()
	msg := &marotte.RPCResponse{Params: nil}
	var target struct{ Name string }
	if err := parseRequest(msg, &target); err == nil {
		t.Error("parseRequest(nil params) should return error")
	}
}

func TestParseRequest_ValidJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"command":"ls","cwd":"/tmp"}`)
	msg := &marotte.RPCResponse{Params: raw}
	var target struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := parseRequest(msg, &target); err != nil {
		t.Fatalf("parseRequest(valid) returned error: %v", err)
	}
	if target.Command != "ls" {
		t.Errorf("Command = %q, want %q", target.Command, "ls")
	}
	if target.Cwd != "/tmp" {
		t.Errorf("Cwd = %q, want %q", target.Cwd, "/tmp")
	}
}

func TestParseRequest_MalformedJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{invalid json}`)
	msg := &marotte.RPCResponse{Params: raw}
	var target struct{ Name string }
	if err := parseRequest(msg, &target); err == nil {
		t.Error("parseRequest(malformed JSON) should return error")
	}
}

func BenchmarkByteRing_Write(b *testing.B) {
	for _, cap := range []int{4096, 65536, 262144} {
		for _, ws := range []int{64, 1024, 8192} {
			name := fmt.Sprintf("cap=%d/write=%d", cap, ws)
			b.Run(name, func(b *testing.B) {
				r := newByteRing(cap)
				data := make([]byte, ws)
				for i := range data {
					data[i] = byte(i)
				}
				b.ReportAllocs()
				b.SetBytes(int64(ws))
				b.ResetTimer()
				for b.Loop() {
					r.Write(data)
				}
			})
		}
	}
}

func FuzzByteRing_WriteRead(f *testing.F) {
	f.Add(uint16(10), []byte("hello"))
	f.Add(uint16(1), []byte{0xC3, 0xA9})
	f.Add(uint16(512), []byte{0})
	f.Fuzz(func(t *testing.T, capRaw uint16, data []byte) {
		cap := int(capRaw)%512 + 1
		r := newByteRing(cap)
		r.Write(data)
		out := r.Bytes()
		if len(out) > cap {
			t.Fatalf("Bytes() len %d exceeds capacity %d", len(out), cap)
		}
		s := r.String()
		if !utf8.ValidString(s) && utf8.Valid(data) {
			t.Fatalf("String() not valid UTF-8 for valid UTF-8 input")
		}
	})
}

// drainAll clears the maps even when a terminal already exited.
func TestDrainAll_ClearsExitedTerminals(t *testing.T) {
	at := bareTerminals()
	done := make(chan struct{})
	close(done) // already exited
	t1 := newAgentTerminal(nil, "", 64)
	t1.done = done
	at.terms["t1"] = t1
	at.byChatID["c1"] = []string{"t1"}

	at.drainAll()

	at.mu.Lock()
	n := len(at.terms)
	at.mu.Unlock()
	if n != 0 {
		t.Errorf("drainAll() left %d terminals, want 0", n)
	}
}

// release removes only the named terminal and reports (nil, false) for an unknown id.
func TestAgentTerminals_Release(t *testing.T) {
	at := bareTerminals()
	at.terms["t1"] = newAgentTerminal(nil, "c1", 64)
	at.terms["t2"] = newAgentTerminal(nil, "c1", 64)
	at.byChatID["c1"] = []string{"t1", "t2"}

	term, ok := at.release("t1")
	if !ok || term == nil || term.chatID != "c1" {
		t.Fatalf("release(t1) = %v, %v; want the t1 terminal, true", term, ok)
	}
	if _, exists := at.terms["t1"]; exists {
		t.Errorf("t1 still present in terms after release")
	}
	if got := at.byChatID["c1"]; len(got) != 1 || got[0] != "t2" {
		t.Errorf("byChatID[c1] = %v, want [t2] (only t1 should be dropped)", got)
	}

	if gotTerm, gotOK := at.release("nope"); gotOK || gotTerm != nil {
		t.Errorf("release(unknown) = %v, %v; want nil, false", gotTerm, gotOK)
	}
	if len(at.terms) != 1 {
		t.Errorf("terms size = %d, want 1", len(at.terms))
	}
}

// ringEvent is a decoded terminal_* SSE event from the replay ring.
type ringEvent struct {
	typ     string
	termID  string
	data    string
	eventID uint64
}

// captureTerminalEvents returns every terminal_* event in the replay ring, sorted by event
// id: insertion order can differ because seq.Add runs before the fan-out lock.
func captureTerminalEvents(t *testing.T, h *Runtime) []ringEvent {
	t.Helper()
	var out []ringEvent
	for _, e := range h.bus.fanout.Snapshot() {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				TerminalID string `json:"terminal_id"`
				Data       string `json:"data"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		switch env.Type {
		case string(marotte.EventTerminalCreated), string(marotte.EventTerminalOutput), string(marotte.EventTerminalExited):
			out = append(out, ringEvent{
				eventID: e.Offset,
				typ:     env.Type,
				termID:  env.Payload.TerminalID,
				data:    env.Payload.Data,
			})
		}
	}
	slices.SortFunc(out, func(a, b ringEvent) int { return cmp.Compare(a.eventID, b.eventID) })
	return out
}

func hasType(evs []ringEvent, typ marotte.EventType) bool {
	for _, e := range evs {
		if e.typ == string(typ) {
			return true
		}
	}
	return false
}

// firstEventID returns the lowest event id of the given type (evs is sorted).
func firstEventID(evs []ringEvent, typ marotte.EventType) (uint64, bool) {
	for _, e := range evs {
		if e.typ == string(typ) {
			return e.eventID, true
		}
	}
	return 0, false
}

// TestTerminalCreated_BroadcastBeforeOutputAndExited pins terminal_created ahead of any
// output or exit; otherwise the client drops them and the tab stays "running".
func TestTerminalCreated_BroadcastBeforeOutputAndExited(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	// The brief sleep lets the pump read the output before cmd.Wait closes the pipe.
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "printf hello; sleep 0.3"}, nil)

	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	// terminal_exited lands just after term.done closes, so poll for both.
	var evs []ringEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = captureTerminalEvents(t, h)
		if hasType(evs, marotte.EventTerminalOutput) && hasType(evs, marotte.EventTerminalExited) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	createdID, ok := firstEventID(evs, marotte.EventTerminalCreated)
	if !ok {
		t.Fatal("terminal_created was never broadcast")
	}
	sawOutput, sawExited := false, false
	for _, e := range evs {
		switch e.typ {
		case string(marotte.EventTerminalOutput):
			sawOutput = true
			if e.eventID <= createdID {
				t.Errorf("terminal_output event id %d <= terminal_created id %d (created must be broadcast first)", e.eventID, createdID)
			}
		case string(marotte.EventTerminalExited):
			sawExited = true
			if e.eventID <= createdID {
				t.Errorf("terminal_exited event id %d <= terminal_created id %d (created must be broadcast first)", e.eventID, createdID)
			}
		}
	}
	if !sawOutput {
		t.Error("no terminal_output event captured (pump never broadcast the command's stdout)")
	}
	if !sawExited {
		t.Error("no terminal_exited event captured")
	}
}

// chunkReader hands out one chunk per Read, placing a read boundary inside a rune.
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}

// A rune split across the 4 KB read boundary must arrive intact in the live broadcast while the ring keeps every raw byte.
func TestPumpTerminalOutput_RuneSplitAcrossReadBoundaryNotCorrupted(t *testing.T) {
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, newTestChatStore())
	term := newAgentTerminal(nil, "c1", 1024)
	// Reads split every multi-byte rune: é = C3 A9, € = E2 82 AC, 😀 = F0 9F 98 80.
	r := &chunkReader{chunks: [][]byte{
		{0x61},             // "a" (complete ASCII)
		{0xC3},             // é byte 1/2  → incomplete, held
		{0xA9, 0xE2},       // é byte 2/2 (completes é) + € byte 1/3 (held)
		{0x82},             // € byte 2/3  → still incomplete, held
		{0xAC, 0xF0, 0x9F}, // € byte 3/3 (completes €) + 😀 bytes 1,2/4 (held)
		{0x98},             // 😀 byte 3/4 → still incomplete, held
		{0x80},             // 😀 byte 4/4 (completes 😀)
	}}
	const want = "aé€😀"

	h.agentTerms.pumpOutput(term, "t1", "c1", r)

	// A split rune would marshal as U+FFFD, so equality proves the carry.
	var got bytes.Buffer
	for _, e := range captureTerminalEvents(t, h) {
		if e.typ == string(marotte.EventTerminalOutput) {
			got.WriteString(e.data)
		}
	}
	if got.String() != want {
		t.Errorf("broadcast reassembly = %q, want %q (a rune was split across the read boundary)", got.String(), want)
	}
	if ring := term.output.String(); ring != want {
		t.Errorf("ring content = %q, want %q", ring, want)
	}
}

func TestIncompleteTailLen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty", nil, 0},
		{"ascii", []byte("hello"), 0},
		{"complete 2-byte é", []byte{0xC3, 0xA9}, 0},
		{"complete 3-byte €", []byte{0xE2, 0x82, 0xAC}, 0},
		{"complete 4-byte 😀", []byte{0xF0, 0x9F, 0x98, 0x80}, 0},
		{"ascii then partial 2-byte lead", []byte{0x41, 0xC3}, 1},
		{"partial 3-byte 1of3", []byte{0xE2}, 1},
		{"partial 3-byte 2of3", []byte{0xE2, 0x82}, 2},
		{"partial 4-byte 3of4", []byte{0xF0, 0x9F, 0x98}, 3},
		{"real U+FFFD is complete", []byte{0xEF, 0xBF, 0xBD}, 0},
		{"lone continuation byte", []byte{0x80}, 0},
		{"invalid lead 0xFF", []byte{0xFF}, 0},
		{"complete multibyte then ascii", []byte{0xE2, 0x82, 0xAC, 0x41}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := incompleteTailLen(tc.in); got != tc.want {
				t.Errorf("incompleteTailLen(%x) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// sizeChunkReader hands out fixed-size slices, one per Read.
type sizeChunkReader struct {
	data []byte
	size int
	pos  int
}

func (r *sizeChunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	end := min(r.pos+r.size, len(r.data))
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	return n, nil
}

// pumpBroadcast runs the pump over data in size-byte reads and returns the ring plus the
// concatenated broadcast payloads.
func pumpBroadcast(t *testing.T, h *Runtime, data []byte, size int) (ring, broadcast []byte) {
	t.Helper()
	preSeq := h.bus.fanout.Position().Head
	term := newAgentTerminal(nil, "c1", 1<<20)
	h.agentTerms.pumpOutput(term, "t1", "c1", &sizeChunkReader{data: data, size: size})
	for _, e := range bufferedSince(h, preSeq) {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		if env.Type == string(marotte.EventTerminalOutput) {
			broadcast = append(broadcast, env.Payload.Data...)
		}
	}
	return term.output.Bytes(), broadcast
}

// FuzzPumpTerminalOutput_UTF8Broadcast asserts that the ring holds the input exactly and
// that read boundaries do not change the broadcast (chunked equals single-read; the
// emitter is not a passthrough). Bounded to 512 bytes, inside ansitext's maxPendingBytes.
// One runtime is reused to avoid leaking per-Runtime goroutines.
func FuzzPumpTerminalOutput_UTF8Broadcast(f *testing.F) {
	f.Add([]byte("hello"), uint8(1))
	f.Add([]byte("aé€😀z"), uint8(1))
	f.Add([]byte("aé€😀z"), uint8(3))
	f.Add([]byte{0xE2, 0x82, 0xAC}, uint8(1))
	f.Add([]byte{0xFF, 0x80, 0xE2}, uint8(1)) // invalid + incomplete tail
	f.Add([]byte("a\u202cb"), uint8(1))       // hidden Unicode the emitter deletes
	f.Add([]byte("a\x1b[31mred"), uint8(1))   // an escape the parser lifts into a span
	// A two-byte escape split across the boundary exercises the parser's cross-chunk state;
	// chunkSize 1 makes the boundary real.
	f.Add([]byte("\x1b0"), uint8(1))
	f.Add([]byte("a\x1b0b"), uint8(1))
	h := New(f.Context(), f.TempDir(), func() ACPBridge { return newFakeBridge() }, newTestChatStore())
	f.Fuzz(func(t *testing.T, data []byte, chunkRaw uint8) {
		if len(data) > 512 {
			data = data[:512] // keep this iteration's emits under the 1024-event ring cap
		}
		chunkSize := int(chunkRaw)%8 + 1

		ring, chunked := pumpBroadcast(t, h, data, chunkSize)
		if !bytes.Equal(ring, data) {
			t.Fatalf("ring content = %x, want raw input %x (chunkSize=%d)", ring, data, chunkSize)
		}
		// Clamped to 1 so an empty input still terminates the reader.
		_, whole := pumpBroadcast(t, h, data, max(len(data), 1))
		if !bytes.Equal(chunked, whole) {
			t.Fatalf("input %x broadcast as %q at chunkSize=%d but as %q in one read — a rune was split across the read boundary",
				data, chunked, chunkSize, whole)
		}
	})
}

// TestKillGroup_ReapsTheWholeTree pins a group kill: agent commands are trees with no stdin-EOF
// reclaim, and the assertion is on the grandchild a head-only kill stranded.
func TestKillGroup_ReapsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// Killing the group must take the child; killing the head alone leaves it running.
	script := "sleep 300 & echo $! > " + pidFile + "; wait"
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- fixed test script
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bait: %v", err)
	}
	t.Cleanup(func() {
		_ = procgroup.Kill(cmd.Process, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	childPID := waitForPIDFile(t, pidFile)
	if !processAlive(childPID) {
		t.Fatalf("bait child %d not alive before the kill; the test proves nothing", childPID)
	}

	if err := procgroup.Kill(cmd.Process, syscall.SIGKILL); err != nil {
		t.Fatalf("killGroup: %v", err)
	}
	_, _ = cmd.Process.Wait() // reap the head; the poll below is on the child

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived killGroup; the group form is not reaching the tree", childPID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Once the head is reaped its pid may lead a stranger's group; the fixture stands one in with a Setpgid sleep.
func TestKillTerminalGroup_AfterReapSignalsNothing(t *testing.T) {
	stranger := exec.Command("sleep", "30") // #nosec G204 -- fixed test command
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stranger.Start(); err != nil {
		t.Fatalf("Setup: start stranger: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-stranger.Process.Pid, syscall.SIGKILL)
		_, _ = stranger.Process.Wait()
	})

	term := newAgentTerminal(stranger, "c1", 64)
	term.pgid = stranger.Process.Pid
	term.reaped = true
	// done stays open: the reaped flag, not done, is the gate.

	err := killTerminalGroup(term, "t-reaped")
	if !procgroup.AlreadyGone(err) {
		t.Errorf("killTerminalGroup(reaped terminal) = %v, want an already-gone error", err)
	}
	if !processAlive(stranger.Process.Pid) {
		t.Error("killTerminalGroup on a reaped terminal killed the process now holding its pid")
	}
}

// startBaitTerminal drives `script` through terminal/create and returns the runtime, terminal id and the pid written to pidFile.
func startBaitTerminal(t *testing.T, script, pidFile string) (h *Runtime, termID string, childPID int) {
	t.Helper()
	h = hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", termCreateMsg(t, 1, "sh", []string{"-c", script}, nil))
	h.agentTerms.mu.Lock()
	for id := range h.agentTerms.terms {
		termID = id
	}
	h.agentTerms.mu.Unlock()
	if termID == "" {
		t.Fatal("Setup: no agent terminal registered")
	}
	return h, termID, waitForPIDFile(t, pidFile)
}

func termIDMsg(t *testing.T, id int64, method, termID string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{ID: &id, Method: method, Params: mustJSON(t, map[string]string{"terminalId": termID})}
}

// A running command's whole tree goes with terminal/kill, via the group captured at spawn.
func TestKillTerminalGroup_LiveHeadTakesTheTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	h, termID, childPID := startBaitTerminal(t, "sleep 300 & echo $! > "+pidFile+"; wait", pidFile)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	if !processAlive(childPID) {
		t.Fatalf("Setup: grandchild %d not alive before the kill; the test proves nothing", childPID)
	}

	h.translateACPEvent("c1", termIDMsg(t, 2, methodTermKill, termID))

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived terminal/kill; the group captured at spawn was not signalled", childPID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A finished command's backgrounded child is left running, as KAS's own terminal does (characterization pin).
func TestTerminalRelease_LeavesAFinishedCommandsBackgroundedChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	h, termID, childPID := startBaitTerminal(t,
		"sleep 30 >/dev/null 2>&1 </dev/null & echo $! > "+pidFile, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	h.agentTerms.mu.Lock()
	term := h.agentTerms.terms[termID]
	h.agentTerms.mu.Unlock()
	waitClosed(t, term.done, "terminal")

	h.translateACPEvent("c1", termIDMsg(t, 2, methodTermRelease, termID))

	if !processAlive(childPID) {
		t.Errorf("terminal/release killed %d, the backgrounded child of a command that had already finished", childPID)
	}
}

// waitForPIDFile polls for the bait script's pid file and returns the pid.
func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(path) // #nosec G304 -- t.TempDir path
		if err == nil {
			if pid, cErr := strconv.Atoi(strings.TrimSpace(string(b))); cErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bait never wrote its child pid to %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// processAlive reports whether pid is a live, non-zombie process, from /proc/<pid>/stat.
// Not kill(pid, 0): that answers alive for a zombie, so it would measure the ambient
// reaper rather than killGroup. The state follows the last ')' because comm may hold parens.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) // #nosec G304 -- pid from the test's own child
	if err != nil {
		return false // no /proc entry: reaped and gone
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z'
}

// A stream ending mid-escape must still deliver the held bytes, or a command killed mid-write loses its tail.
func TestPumpTerminalOutput_ReleasesATruncatedEscapeSequenceAtEOF(t *testing.T) {
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, newTestChatStore())

	// "\x1b[3" has no final byte, so the parser holds it and neutralizes the ESC on release.
	_, broadcast := pumpBroadcast(t, h, []byte("hi\x1b[3"), 8)

	const want = "hi\uFFFD[3"
	if got := string(broadcast); got != want {
		t.Errorf("pumpOutput broadcast %q for %q, want %q", got, "hi\x1b[3", want)
	}
}

// A clean exit is a code with no signal: exit 0 must not read as a signal death.
func TestExitStatusFromState_CleanExitIsACodeNotASignal(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Setup: sh -c 'exit 0' error = %v", err)
	}
	code, signal := exitStatusFromState(cmd.ProcessState)
	if code != 0 || signal != "" {
		t.Errorf("exitStatusFromState(exit 0) = (%d, %q), want (0, \"\")", code, signal)
	}

	cmd = exec.Command("sh", "-c", "exit 7")
	if err := cmd.Run(); err == nil {
		t.Fatal("Setup: sh -c 'exit 7' error = nil, want a non-zero exit")
	}
	code, signal = exitStatusFromState(cmd.ProcessState)
	if code != 7 || signal != "" {
		t.Errorf("exitStatusFromState(exit 7) = (%d, %q), want (7, \"\")", code, signal)
	}
}

// An incomplete rune held at EOF must still reach the transcript, matching the ring.
func TestPumpTerminalOutput_DeliversTheHeldRuneTailAtEOF(t *testing.T) {
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, newTestChatStore())

	// "hi" then the first two bytes of "€".
	data := []byte{'h', 'i', 0xE2, 0x82}
	ring, broadcast := pumpBroadcast(t, h, data, 8)

	if !bytes.Equal(ring, data) {
		t.Errorf("ring = %x, want the raw input %x", ring, data)
	}
	const want = "hi\uFFFD\uFFFD"
	if got := string(broadcast); got != want {
		t.Errorf("pumpOutput broadcast %q for %x, want %q (one replacement per held byte)", got, data, want)
	}
}
