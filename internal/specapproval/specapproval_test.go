package specapproval

// The approval record: what it stores, what it refuses to store, what it drops
// from a file it did not write, and how Stale is derived.
//
// The properties worth pinning are the ones whose failure is SILENT. A merge that
// replaces instead of merging loses an approval and reports success. A sanitize
// that keeps a malformed entry puts a badge on screen against a hash nothing can
// compare. A Derive that omits a phase whose document is gone reports the spec as
// never reviewed. None of those surfaces as an error anywhere.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// hashA and hashB are two distinct sha256-shaped digests. Written out rather than
// hashed from bytes: nothing here reads a document, so a real digest would only
// hide which value the assertion is about.
var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
)

const specDir = ".kiro/specs/demo"

// newStoreIn opens a store in a fresh directory and fails the test on the
// diagnostic error, so a case that is not ABOUT a load failure cannot pass while
// silently starting empty.
func newStoreIn(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) reported %v, want a clean load", dir, err)
	}
	return s
}

// writeDoc plants a document at the store's own path, as a hand-edited or
// foreign writer would leave it.
func writeDoc(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), fileMode); err != nil {
		t.Fatalf("plant document: %v", err)
	}
}

// A record has to survive the process that wrote it, and the file it lands in is
// the sixth 0600 file in the config dir — the mode is the whole protection for a
// document naming workspace paths, so it is asserted rather than assumed.
func TestApprove_IsReadBackByTheNextStoreFromA0600File(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir)

	if err := s.Approve(t.Context(), specDir, "design", hashA, ""); err != nil {
		t.Fatalf("Approve(design) = %v, want nil", err)
	}

	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("stat the record: %v", err)
	}
	if got := info.Mode().Perm(); got != fileMode {
		t.Errorf("record mode = %04o, want %04o", got, fileMode)
	}

	reopened := newStoreIn(t, dir)
	got := reopened.For(specDir)
	if len(got) != 1 || got["design"].Hash != hashA {
		t.Errorf("For(%q) after reopen = %+v, want design at %s", specDir, got, hashA)
	}
	if got["design"].At.IsZero() {
		t.Error("the reopened record carries a zero At, so nothing says when it was approved")
	}
	if got["design"].Stale {
		t.Error("For reported Stale, which only Derive can decide")
	}
}

// THE MERGE IS WHY Approve EXISTS rather than a Set taking the whole map: a
// second phase written into the record must not drop the first. A replace-instead
// of-merge reports success and loses an approval, which is the failure this store
// exists to prevent.
func TestApprove_MergesASecondPhaseRatherThanReplacingTheRecord(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir)

	for phase, hash := range map[string]string{"requirements": hashA, "design": hashB} {
		if err := s.Approve(t.Context(), specDir, phase, hash, ""); err != nil {
			t.Fatalf("Approve(%s) = %v, want nil", phase, err)
		}
	}

	got := newStoreIn(t, dir).For(specDir)
	if len(got) != 2 {
		t.Fatalf("For(%q) = %+v, want both phases", specDir, got)
	}
	if got["requirements"].Hash != hashA || got["design"].Hash != hashB {
		t.Errorf("For(%q) = %+v, want requirements at %s and design at %s", specDir, got, hashA, hashB)
	}
}

// Re-approving a phase is what a re-review of a moved document means, so it
// OVERWRITES rather than being refused or kept alongside.
func TestApprove_ReApprovingAPhaseRecordsTheNewVersion(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir)

	if err := s.Approve(t.Context(), specDir, "tasks", hashA, ""); err != nil {
		t.Fatalf("first Approve = %v, want nil", err)
	}
	if err := s.Approve(t.Context(), specDir, "tasks", hashB, ""); err != nil {
		t.Fatalf("second Approve = %v, want nil", err)
	}

	got := newStoreIn(t, dir).For(specDir)
	if len(got) != 1 || got["tasks"].Hash != hashB {
		t.Errorf("For(%q) = %+v, want one tasks record at %s", specDir, got, hashB)
	}
}

// The store validates exactly what it STORES, and a refusal must apply NOTHING —
// a record half-written against a value the load path would then drop is worse
// than the refusal, because it reports success.
func TestApprove_RefusesWhatItWouldNotStoreAndWritesNothing(t *testing.T) {
	cases := map[string]struct {
		dir, phase, hash, user string
		want                   error
	}{
		"empty dir":       {"", "design", hashA, "", ErrBadDir},
		"dir over bound":  {strings.Repeat("d", MaxDirBytes+1), "design", hashA, "", ErrBadDir},
		"unknown phase":   {specDir, "other", hashA, "", ErrBadPhase},
		"empty phase":     {specDir, "", hashA, "", ErrBadPhase},
		"uppercase hash":  {specDir, "design", strings.ToUpper(hashA), "", ErrBadHash},
		"short hash":      {specDir, "design", hashA[:63], "", ErrBadHash},
		"empty hash":      {specDir, "design", "", "", ErrBadHash},
		"user over bound": {specDir, "design", hashA, strings.Repeat("u", MaxUserBytes+1), ErrBadDir},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStoreIn(t, dir)

			err := s.Approve(t.Context(), tc.dir, tc.phase, tc.hash, tc.user)

			if !errors.Is(err, tc.want) {
				t.Errorf("Approve(%q, %q, %q) = %v, want %v", tc.dir, tc.phase, tc.hash, err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, FileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("a refused approval wrote the document")
			}
			if got := s.For(tc.dir); got != nil {
				t.Errorf("For after a refusal = %+v, want nothing", got)
			}
		})
	}
}

// MaxSpecs is the outer wall against a hostile or broken writer, and it bounds
// NEW specs only: a phase of a spec already in the record can never be refused by
// it, or a full record would freeze the specs it does hold.
func TestApprove_RefusesANewSpecAtTheBoundAndStillTakesAKnownOne(t *testing.T) {
	dir := t.TempDir()
	full := make(map[string]map[string]record, MaxSpecs)
	for i := range MaxSpecs {
		full[".kiro/specs/s"+strconv.Itoa(i)] = map[string]record{"design": {Hash: hashA, At: time.Now().UTC()}}
	}
	data, err := json.Marshal(file{Specs: full})
	if err != nil {
		t.Fatalf("encode the seeded document: %v", err)
	}
	writeDoc(t, dir, string(data))
	s := newStoreIn(t, dir)

	if err := s.Approve(t.Context(), specDir, "design", hashA, ""); !errors.Is(err, ErrTooMany) {
		t.Errorf("Approve of a new spec at the bound = %v, want ErrTooMany", err)
	}
	if err := s.Approve(t.Context(), ".kiro/specs/s0", "tasks", hashB, ""); err != nil {
		t.Errorf("Approve of a KNOWN spec at the bound = %v, want nil", err)
	}
	if got := newStoreIn(t, dir).For(".kiro/specs/s0"); len(got) != 2 {
		t.Errorf("For(s0) = %+v, want the seeded design plus the new tasks", got)
	}
}

// A document this store cannot read is warn-and-start-empty, not a boot failure
// (invariant 6): an approval is re-creatable by approving again, so refusing to
// start would leave no way in to repair the record. The error is DIAGNOSTIC, so
// it must be returned rather than swallowed, and the store must still work.
func TestNewStore_WarnsAndStartsEmptyOnADocumentItCannotRead(t *testing.T) {
	cases := map[string]string{
		"not json":         "{{{",
		"wrong shape":      `{"specs": "a string"}`,
		"truncated object": `{"specs": {"` + specDir + `": {"design":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeDoc(t, dir, body)

			s, err := NewStore(dir)

			if err == nil {
				t.Error("NewStore reported no error for a document it could not read")
			}
			if s == nil {
				t.Fatal("NewStore returned no store, so the caller cannot warn and continue")
			}
			if got := s.For(specDir); got != nil {
				t.Errorf("the started-empty store holds %+v", got)
			}
			if err := s.Approve(t.Context(), specDir, "design", hashA, ""); err != nil {
				t.Errorf("Approve on a started-empty store = %v, want nil", err)
			}
		})
	}
}

// A document over the decode bound is refused at the READ rather than after it,
// so the hostile file is never fully allocated.
func TestNewStore_RefusesADocumentOverTheDecodeBound(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, `{"specs":{},"pad":"`+strings.Repeat("p", MaxBytes)+`"}`)

	s, err := NewStore(dir)

	if err == nil {
		t.Error("NewStore accepted a document over the decode bound")
	}
	if got := s.For(specDir); got != nil {
		t.Errorf("the store holds %+v after refusing the document", got)
	}
}

// THE MODE VERDICT LEADS, and this is the case that says so: a symlink planted at
// the name must be refused rather than having its target parsed as the record.
// filemode.EnforceFile opens O_NOFOLLOW, so the refusal is the kernel's.
func TestNewStore_RefusesASymlinkAtTheNameRatherThanParsingItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	data, err := json.Marshal(file{Specs: map[string]map[string]record{
		specDir: {"design": {Hash: hashA, At: time.Now().UTC()}},
	}})
	if err != nil {
		t.Fatalf("encode the target document: %v", err)
	}
	if err := os.WriteFile(target, data, fileMode); err != nil {
		t.Fatalf("write the target document: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	s, err := NewStore(dir)

	if err == nil {
		t.Error("NewStore accepted a symlink at its own name")
	}
	if got := s.For(specDir); got != nil {
		t.Errorf("the store read %+v through the symlink", got)
	}
}

// sanitize drops each entry this store could not have written INDIVIDUALLY, so
// the failure mode is one missing badge rather than a record that will not load.
// Every dropped shape here would otherwise put a badge on screen against a value
// nothing can compare.
func TestNewStore_DropsAnEntryThisStoreCouldNotHaveWritten(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("d", MaxDirBytes+1)
	writeDoc(t, dir, `{"specs":{
		"`+specDir+`": {
			"design": {"hash":"`+hashA+`"},
			"other": {"hash":"`+hashB+`"},
			"tasks": {"hash":"nope"},
			"requirements": {"hash":"`+hashB+`","user":"`+strings.Repeat("u", MaxUserBytes+1)+`"}
		},
		"": {"design": {"hash":"`+hashA+`"}},
		"`+long+`": {"design": {"hash":"`+hashA+`"}},
		".kiro/specs/allbad": {"design": {"hash":"nope"}}
	}}`)

	s := newStoreIn(t, dir)

	got := s.For(specDir)
	if len(got) != 1 || got["design"].Hash != hashA {
		t.Errorf("For(%q) = %+v, want the design entry alone", specDir, got)
	}
	for _, dropped := range []string{"", long, ".kiro/specs/allbad"} {
		if kept := s.For(dropped); kept != nil {
			t.Errorf("For(%q) = %+v, want the whole entry dropped", dropped, kept)
		}
	}
}

// Stale is DERIVED per read, never stored, and the two shapes that must both read
// stale are a document that moved and a document that is gone. Omitting the gone
// one would report the spec as never reviewed, which is the opposite of what the
// record says.
func TestDerive_MarksAMovedOrMissingDocumentStale(t *testing.T) {
	approved := map[string]marotte.SpecApproval{
		"requirements": {Hash: hashA},
		"design":       {Hash: hashA},
		"tasks":        {Hash: hashA},
	}
	docs := []marotte.SpecDoc{
		{File: "requirements.md", Role: marotte.SpecDocRoleRequirements, Hash: hashA},
		{File: "design.md", Role: marotte.SpecDocRoleDesign, Hash: hashB},
		// tasks.md is absent: nothing is left for its approval to describe.
	}

	got := Derive(approved, docs)

	want := map[string]bool{"requirements": false, "design": true, "tasks": true}
	if len(got) != len(want) {
		t.Fatalf("Derive returned %d phases, want %d: a phase is never dropped", len(got), len(want))
	}
	for phase, wantStale := range want {
		if got[phase].Stale != wantStale {
			t.Errorf("Derive[%s].Stale = %t, want %t", phase, got[phase].Stale, wantStale)
		}
		if got[phase].Hash != hashA {
			t.Errorf("Derive[%s].Hash = %q, want the approved %q", phase, got[phase].Hash, hashA)
		}
	}
}

// Two documents can share a role — requirements.md and bugfix.md both map to
// requirements — and the FIRST in display order is the one the client's phase
// segment shows, so it is the one the reader approved and the one Stale compares
// against. Reading the last would report a fresh approval as stale.
func TestDerive_ComparesTheFirstDocumentInDisplayOrderForASharedRole(t *testing.T) {
	got := Derive(
		map[string]marotte.SpecApproval{"requirements": {Hash: hashA}},
		[]marotte.SpecDoc{
			{File: "requirements.md", Role: marotte.SpecDocRoleRequirements, Hash: hashA},
			{File: "bugfix.md", Role: marotte.SpecDocRoleRequirements, Hash: hashB},
		},
	)

	if got["requirements"].Stale {
		t.Error("Derive marked the first-in-order document stale, so it compared a later one")
	}
}

// A spec nobody approved is the common case, and both halves answer nothing
// rather than an empty map a reader would have to tell apart.
func TestForAndDerive_AnswerNothingForASpecNobodyApproved(t *testing.T) {
	s := newStoreIn(t, t.TempDir())

	if got := s.For(specDir); got != nil {
		t.Errorf("For on an empty store = %+v, want nil", got)
	}
	if got := Derive(nil, []marotte.SpecDoc{{Role: marotte.SpecDocRoleDesign, Hash: hashA}}); got != nil {
		t.Errorf("Derive(nil) = %+v, want nil", got)
	}
}

// Phases is DERIVED from the role vocabulary rather than listed twice, so a role
// added to marotte.SpecDocRole cannot be silently unapprovable. What this pins is
// the membership either way: every phase in the set is approvable, and the
// residual bucket is not.
func TestPhases_AreTheApprovableRolesAndNotTheResidualBucket(t *testing.T) {
	for _, phase := range Phases() {
		if !ValidPhase(string(phase)) {
			t.Errorf("ValidPhase(%q) = false for a phase Phases names", phase)
		}
	}
	if ValidPhase(string(marotte.SpecDocRoleOther)) {
		t.Error("ValidPhase accepted the residual role, which names no single document")
	}
	if got := len(Phases()); got != 3 {
		t.Errorf("Phases() has %d members, want 3: a change here is a wire change, not a tidy-up", got)
	}
}

// A CONCURRENT approval of a different phase must not lose the other, which is
// the property the merge-inside-the-write-lock exists for: a snapshot taken
// outside it lets two writers clone the same record and the later persist wins.
// Probabilistic, so it is deliberately generous — four phases' worth of writers
// across a real fsync, over four fresh stores.
func TestApprove_ConcurrentApprovalsOfDifferentPhasesAllSurvive(t *testing.T) {
	for round := range 4 {
		dir := t.TempDir()
		s := newStoreIn(t, dir)
		phases := []string{"requirements", "design", "tasks"}

		var wg sync.WaitGroup
		errs := make([]error, len(phases))
		for i, phase := range phases {
			wg.Go(func() {
				errs[i] = s.Approve(t.Context(), specDir, phase, hashA, "")
			})
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: Approve(%s) = %v, want nil", round, phases[i], err)
			}
		}
		if got := s.For(specDir); len(got) != len(phases) {
			t.Fatalf("round %d: the published record holds %+v, want all %d phases", round, got, len(phases))
		}
		if got := newStoreIn(t, dir).For(specDir); len(got) != len(phases) {
			t.Fatalf("round %d: the FILE holds %+v, want all %d phases", round, got, len(phases))
		}
	}
}
