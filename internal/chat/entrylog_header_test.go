package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The header policy is total, so the log holds no nil check over the collaborator
// that carries it (design 2 §3.1). The census is a source read rather than a
// behavioural arm because the property is an ABSENCE: the guard that comes back is
// the one nobody has written yet, and a run root supplied noHeader behaves
// identically whether the call site is guarded or not.

// headerCollaboratorFields are the EntryLog fields that reach a root's header: the
// LogHeader collaborator, and the six hook fields R2 and R3 deleted. The retired
// spellings are named too, because restoring one is how the optional-hook policy
// returns.
var headerCollaboratorFields = []string{
	"header",
	"model",
	"counters",
	"sessionID",
	"markReconcile",
	"markDegraded",
	"revise",
}

// entrylogSourceFloor is the line count below which the read is measuring something
// other than the log: a census over the wrong file reports a clean sweep, which is
// the one answer it must never give.
const entrylogSourceFloor = 800

func TestEntryLog_HoldsNoNilGuardOverItsHeader(t *testing.T) {
	const path = "entrylog.go"
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	guards := make([]*regexp.Regexp, 0, len(headerCollaboratorFields))
	for _, field := range headerCollaboratorFields {
		guards = append(guards, regexp.MustCompile(
			`(?:l\.`+field+`\s*[!=]=\s*nil|nil\s*[!=]=\s*l\.`+field+`\b)`,
		))
	}

	var (
		lines    int
		declared bool
		hits     []string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lines++
		line := sc.Text()
		if strings.HasPrefix(line, "type EntryLog struct {") {
			declared = true
		}
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		for _, guard := range guards {
			if m := guard.FindString(code); m != "" {
				hits = append(hits, path+":"+itoa(lines)+": "+strings.TrimSpace(line))
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !declared || lines < entrylogSourceFloor {
		t.Fatalf("%s is not the log's source (lines = %d, want >= %d; EntryLog declared = %t)",
			path, lines, entrylogSourceFloor, declared)
	}
	if len(hits) > 0 {
		t.Errorf("%s holds %d nil guard(s) over a header collaborator, want 0:\n%s",
			path, len(hits), strings.Join(hits, "\n"))
	}
}

// itoa keeps the census free of a fmt import for one number.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestNeedsReconcile_TheRunRootIsNotReconcilable is item (d)'s last arm: the run root
// answers the reconcile predicate false through its header POLICY, not through a nil
// hook. The twin is what makes the arm non-vacuous — the same crash signal on a chat
// root answers true, so the run root's false is the policy and not the fixture.
func TestNeedsReconcile_TheRunRootIsNotReconcilable(t *testing.T) {
	t.Run("a run root is not reconcilable", func(t *testing.T) {
		ctx := t.Context()
		root := filepath.Join(t.TempDir(), "runs", "wf_1")
		lg, err := OpenEntryLog(ctx, root, NoHeader())
		if err != nil {
			t.Fatalf("open run log: %v", err)
		}
		step, err := lg.OpenTurn(ctx, &TurnSpec{
			Source: marotte.TurnOpenNameWorkflowStep, Run: "wf_1",
			NodePath: "build", SessionID: "sess_1",
		})
		if err != nil {
			t.Fatalf("open step turn: %v", err)
		}
		if err := lg.Close(); err != nil {
			t.Fatalf("close run log: %v", err)
		}
		reopened, err := OpenEntryLog(ctx, root, NoHeader())
		if err != nil {
			t.Fatalf("reopen run log: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })

		// The fixture's honesty guard, on the NEIGHBOURING fact: the store-open closer
		// left this turn the crash signal condition (ii) reads. Without it the
		// predicate is false for every root and the arm below measures nothing.
		wantUnterminated(t, reopened, step.Turn)

		if reopened.NeedsReconcile() {
			t.Errorf("NeedsReconcile() = true for a run root, want false: a run has no session to reconcile against")
		}
		if turns, session := reopened.ReconcileTargets(); len(turns) > 0 || session != "" {
			t.Errorf("ReconcileTargets() = (%v, %q), want (nil, \"\") for a run root", turns, session)
		}
	})

	t.Run("a chat root carrying the same signal is reconcilable", func(t *testing.T) {
		f := newLogFixture(t)
		turn := f.prompt("hello")
		f.reopen()

		wantUnterminated(t, f.log, turn)

		if !f.log.NeedsReconcile() {
			t.Errorf("NeedsReconcile() = false for a chat root holding an unterminated closer, want true")
		}
		if turns, _ := f.log.ReconcileTargets(); len(turns) != 1 || turns[0] != turn {
			t.Errorf("ReconcileTargets() named %v, want exactly [%s]", turns, turn)
		}
	})
}

// wantUnterminated fails unless turn's newest entry is the store-open closer's own
// turn_close, which is the signal the reconcile predicate's condition (ii) reads.
func wantUnterminated(t *testing.T, lg *EntryLog, turn string) {
	t.Helper()
	entries, err := lg.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("read turn %q: %v", turn, err)
	}
	if len(entries) == 0 {
		t.Fatalf("turn %q holds no entries, want its synthesized closer", turn)
	}
	last := entries[len(entries)-1]
	if last.Kind != marotte.EntryKindTurnClose {
		t.Fatalf("turn %q ends on a %s, want a turn_close", turn, last.Kind)
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(last.Payload, &footer); err != nil {
		t.Fatalf("parse the closer of %q: %v", turn, err)
	}
	if footer.StopReasonRaw != "unterminated" {
		t.Fatalf("the closer of %q reads stop_reason_raw %q, want %q",
			turn, footer.StopReasonRaw, "unterminated")
	}
}
