package specapproval

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

// hashA and hashB are two distinct sha256-shaped digests, written out so the assertion's
// value is visible.
var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
)

const specDir = ".kiro/specs/demo"

func newStoreIn(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) reported %v, want a clean load", dir, err)
	}
	return s
}

func writeDoc(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), fileMode); err != nil {
		t.Fatalf("plant document: %v", err)
	}
}

// TestApprove_IsReadBackByTheNextStoreFromA0600File pins durability and the 0600 mode.
func TestApprove_IsReadBackByTheNextStoreFromA0600File(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir)

	if err := s.Approve(t.Context(), specDir, "design", hashA, ""); err != nil {
		t.Fatalf("Approve(design) = %v, want nil", err)
	}

	info, err := os.Stat(filepath.Join(dir, fileName))
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

// TestApprove_MergesASecondPhaseRatherThanReplacingTheRecord pins the merge.
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

// TestApprove_RefusesWhatItWouldNotStoreAndWritesNothing pins that a refusal applies nothing.
func TestApprove_RefusesWhatItWouldNotStoreAndWritesNothing(t *testing.T) {
	cases := map[string]struct {
		dir, phase, hash, user string
		want                   error
	}{
		"empty dir":       {"", "design", hashA, "", errBadDir},
		"dir over bound":  {strings.Repeat("d", maxDirBytes+1), "design", hashA, "", errBadDir},
		"unknown phase":   {specDir, "other", hashA, "", errBadPhase},
		"empty phase":     {specDir, "", hashA, "", errBadPhase},
		"uppercase hash":  {specDir, "design", strings.ToUpper(hashA), "", errBadHash},
		"short hash":      {specDir, "design", hashA[:63], "", errBadHash},
		"empty hash":      {specDir, "design", "", "", errBadHash},
		"user over bound": {specDir, "design", hashA, strings.Repeat("u", maxUserBytes+1), errBadDir},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStoreIn(t, dir)

			err := s.Approve(t.Context(), tc.dir, tc.phase, tc.hash, tc.user)

			if !errors.Is(err, tc.want) {
				t.Errorf("Approve(%q, %q, %q) = %v, want %v", tc.dir, tc.phase, tc.hash, err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, fileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("a refused approval wrote the document")
			}
			if got := s.For(tc.dir); got != nil {
				t.Errorf("For after a refusal = %+v, want nothing", got)
			}
		})
	}
}

// TestApprove_RefusesANewSpecAtTheBoundAndStillTakesAKnownOne pins that maxSpecs bounds NEW
// specs only.
func TestApprove_RefusesANewSpecAtTheBoundAndStillTakesAKnownOne(t *testing.T) {
	dir := t.TempDir()
	full := make(map[string]map[string]record, maxSpecs)
	for i := range maxSpecs {
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

// TestNewStore_WarnsAndStartsEmptyOnADocumentItCannotRead pins warn-and-start-empty
// (invariant 6) with the error still returned.
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
	writeDoc(t, dir, `{"specs":{},"pad":"`+strings.Repeat("p", maxBytes)+`"}`)

	s, err := NewStore(dir)

	if err == nil {
		t.Error("NewStore accepted a document over the decode bound")
	}
	if got := s.For(specDir); got != nil {
		t.Errorf("the store holds %+v after refusing the document", got)
	}
}

// TestNewStore_RefusesASymlinkAtTheNameRatherThanParsingItsTarget pins that the mode verdict
// leads.
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
	if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
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

// TestNewStore_DropsAnEntryThisStoreCouldNotHaveWritten pins per-entry sanitize.
func TestNewStore_DropsAnEntryThisStoreCouldNotHaveWritten(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("d", maxDirBytes+1)
	writeDoc(t, dir, `{"specs":{
		"`+specDir+`": {
			"design": {"hash":"`+hashA+`"},
			"other": {"hash":"`+hashB+`"},
			"tasks": {"hash":"nope"},
			"requirements": {"hash":"`+hashB+`","user":"`+strings.Repeat("u", maxUserBytes+1)+`"}
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

// TestDerive_MarksAMovedOrMissingDocumentStale pins both stale shapes.
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

// TestDerive_ComparesTheFirstDocumentInDisplayOrderForASharedRole pins the first document as
// the comparand.
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

// TestPhases_AreTheApprovableRolesAndNotTheResidualBucket pins the derived membership.
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

// TestApprove_ConcurrentApprovalsOfDifferentPhasesAllSurvive pins the merge under concurrency
// (probabilistic, so generous).
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
