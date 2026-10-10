package command

// Every refusal case records NOTHING and broadcasts NOTHING.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/specapproval"
)

type approvalCall struct{ dir, phase, hash, user string }

// fakeApprovals is the record as this command sees it. err is what Approve
// answers, so a case can drive the store's own refusals without a filesystem.
type fakeApprovals struct {
	err   error
	calls []approvalCall
}

func (f *fakeApprovals) Approve(_ context.Context, dir, phase, hash, user string) error {
	f.calls = append(f.calls, approvalCall{dir, phase, hash, user})
	return f.err
}

// seedSpec writes a spec directory under a fresh workspace root and returns the
// workspace plus each document's real sha256, so a case's claimed hash is the one
// the handler will compute rather than a constant the test invented.
func seedSpec(t *testing.T, docs map[string]string) (Workspace, map[string]string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".kiro", "specs", "demo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("seed the spec directory: %v", err)
	}
	hashes := make(map[string]string, len(docs))
	for file, body := range docs {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %v", file, err)
		}
		sum := sha256.Sum256([]byte(body))
		hashes[file] = hex.EncodeToString(sum[:])
	}
	return Workspace{Dir: root, ConfigDir: t.TempDir()}, hashes
}

// approveReq builds the command envelope with NO chat id: a spec is
// workspace-global, so the handler must not need one.
func approveReq(t *testing.T, dir, phase, hash string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.ApproveSpecPhaseCommand{Dir: dir, Phase: phase, Hash: hash})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdApproveSpecPhase, Payload: payload}
}

func approvedFrames(t *testing.T, b *capturingBus) []marotte.SpecApprovedPayload {
	t.Helper()
	var out []marotte.SpecApprovedPayload
	for _, evt := range b.events {
		if evt.Type != marotte.EventSpecApproved {
			continue
		}
		p, ok := evt.Payload.(marotte.SpecApprovedPayload)
		if !ok {
			t.Fatalf("spec_approved payload = %T, want marotte.SpecApprovedPayload", evt.Payload)
		}
		out = append(out, p)
	}
	return out
}

// The success path, and the three things it owes: the record names exactly what
// was asked for, ONE workspace-global frame goes out naming the spec, and the
// response echoes what was approved so the client is not guessing.
func TestCmdApproveSpecPhase_RecordsTheApprovalAndAnnouncesTheSpec(t *testing.T) {
	ws, hashes := seedSpec(t, map[string]string{"design.md": "# Design\n"})
	approvals := &fakeApprovals{}
	bus := &capturingBus{}

	res, err := cmdApproveSpecPhase(t.Context(), approvals, ws, bus,
		approveReq(t, ".kiro/specs/demo", "design", hashes["design.md"]))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	want := []approvalCall{{".kiro/specs/demo", "design", hashes["design.md"], ""}}
	if len(approvals.calls) != 1 || approvals.calls[0] != want[0] {
		t.Errorf("recorded %+v, want %+v", approvals.calls, want)
	}
	frames := approvedFrames(t, bus)
	if len(frames) != 1 || frames[0].Dir != ".kiro/specs/demo" {
		t.Errorf("broadcast %+v, want one spec_approved naming the spec", frames)
	}
	body, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want map[string]any", res)
	}
	if body["phase"] != "design" || body["hash"] != hashes["design.md"] {
		t.Errorf("response = %+v, want the phase and hash that were recorded", body)
	}
}

// The user is recorded as the EMPTY STRING rather than invented: marotte knows no
// identity, and a fabricated one could not later be told apart from a real one.
func TestCmdApproveSpecPhase_RecordsNoUserRatherThanInventingOne(t *testing.T) {
	ws, hashes := seedSpec(t, map[string]string{"tasks.md": "- [ ] 1. work\n"})
	approvals := &fakeApprovals{}

	_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, &capturingBus{},
		approveReq(t, ".kiro/specs/demo", "tasks", hashes["tasks.md"]))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if len(approvals.calls) != 1 || approvals.calls[0].user != "" {
		t.Errorf("recorded user = %q, want the empty string", approvals.calls[0].user)
	}
}

// THE COMPARE-AND-SWAP IS THE POINT: a claim naming a version the document no
// longer carries is refused with the CURRENT hash, so the client can tell a
// document that moved from a claim it merely got wrong. Nothing is recorded, and
// no frame goes out for the approval that did not happen.
func TestCmdApproveSpecPhase_RefusesAStaleClaimAndNamesTheCurrentHash(t *testing.T) {
	ws, hashes := seedSpec(t, map[string]string{"design.md": "# Design\n"})
	approvals := &fakeApprovals{}
	bus := &capturingBus{}
	stale := strings.Repeat("a", 64)

	_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, bus,
		approveReq(t, ".kiro/specs/demo", "design", stale))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", statusOf(err), errText(err))
	}
	if got := reasonOf(err); got != "doc_changed" {
		t.Errorf("reason = %q, want doc_changed", got)
	}
	if got := currentHashOf(err); got != hashes["design.md"] {
		t.Errorf("current_hash = %q, want the document's live hash %q", got, hashes["design.md"])
	}
	if len(approvals.calls) != 0 || len(bus.events) != 0 {
		t.Errorf("a refused claim recorded %+v and broadcast %d frames", approvals.calls, len(bus.events))
	}
}

// A phase whose document is GONE answers the empty current hash, which is honest:
// there is nothing left to approve, so there is no version to re-read.
func TestCmdApproveSpecPhase_AnswersAnEmptyCurrentHashForAnAbsentDocument(t *testing.T) {
	ws, _ := seedSpec(t, map[string]string{"design.md": "# Design\n"})
	approvals := &fakeApprovals{}

	_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, &capturingBus{},
		approveReq(t, ".kiro/specs/demo", "tasks", strings.Repeat("a", 64)))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", statusOf(err), errText(err))
	}
	if got := currentHashOf(err); got != "" {
		t.Errorf("current_hash = %q, want empty: the document is not there", got)
	}
	if len(approvals.calls) != 0 {
		t.Errorf("recorded %+v for a phase with no document", approvals.calls)
	}
}

// TestCmdApproveSpecPhase_ComparesTheFirstDocumentInDisplayOrder: two documents share the
// requirements role, and the first is the one the reader saw and approved.
func TestCmdApproveSpecPhase_ComparesTheFirstDocumentInDisplayOrderForASharedRole(t *testing.T) {
	ws, hashes := seedSpec(t, map[string]string{
		"requirements.md": "# Requirements\n",
		"bugfix.md":       "# Bug\n",
	})
	approvals := &fakeApprovals{}

	_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, &capturingBus{},
		approveReq(t, ".kiro/specs/demo", "requirements", hashes["bugfix.md"]))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("approving against the FIRST document's hash: status = %d, want 200 (body %s)",
			statusOf(err), errText(err))
	}

	_, err = cmdApproveSpecPhase(t.Context(), approvals, ws, &capturingBus{},
		approveReq(t, ".kiro/specs/demo", "requirements", hashes["requirements.md"]))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("approving against the SECOND document's hash = %d, want 409", statusOf(err))
	}
	if got := currentHashOf(err); got != hashes["bugfix.md"] {
		t.Errorf("current_hash = %q, want the first document's hash %q", got, hashes["bugfix.md"])
	}
}

// The shape refusals happen BEFORE any document is read, and each carries the
// machine-readable class the client branches on. An unaddressable directory is a
// 404 rather than a traversal the loader has to refuse for us: spec.Address is
// the one resolver, so a path outside a .kiro/specs root has no spec to name.
func TestCmdApproveSpecPhase_RefusesWhatItCannotAddressOrRead(t *testing.T) {
	cases := map[string]struct {
		dir, phase, hash string
		wantStatus       int
		wantReason       string
	}{
		"unknown phase":      {".kiro/specs/demo", "other", strings.Repeat("a", 64), http.StatusBadRequest, "invalid_phase"},
		"empty phase":        {".kiro/specs/demo", "", strings.Repeat("a", 64), http.StatusBadRequest, "invalid_phase"},
		"malformed hash":     {".kiro/specs/demo", "design", "nope", http.StatusBadRequest, "invalid_hash"},
		"uppercase hash":     {".kiro/specs/demo", "design", strings.Repeat("A", 64), http.StatusBadRequest, "invalid_hash"},
		"dir outside a root": {"elsewhere/demo", "design", strings.Repeat("a", 64), http.StatusNotFound, ""},
		"traversal":          {".kiro/specs/../../etc", "design", strings.Repeat("a", 64), http.StatusNotFound, ""},
		"spec not on disk":   {".kiro/specs/gone", "design", strings.Repeat("a", 64), http.StatusNotFound, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws, _ := seedSpec(t, map[string]string{"design.md": "# Design\n"})
			approvals := &fakeApprovals{}
			bus := &capturingBus{}

			_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, bus,
				approveReq(t, tc.dir, tc.phase, tc.hash))

			if statusOf(err) != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.wantStatus, errText(err))
			}
			if got := reasonOf(err); got != tc.wantReason {
				t.Errorf("reason = %q, want %q", got, tc.wantReason)
			}
			if len(approvals.calls) != 0 || len(bus.events) != 0 {
				t.Errorf("a refusal recorded %+v and broadcast %d frames", approvals.calls, len(bus.events))
			}
		})
	}
}

// A payload the handler cannot decode is a 400, not a 500: the request is
// malformed rather than the server broken.
func TestCmdApproveSpecPhase_RefusesAMalformedPayload(t *testing.T) {
	ws, _ := seedSpec(t, map[string]string{"design.md": "# Design\n"})
	approvals := &fakeApprovals{}

	_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, &capturingBus{},
		&marotte.ClientCommand{Type: marotte.CmdApproveSpecPhase, Payload: json.RawMessage(`{{`)})

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body %s)", statusOf(err), errText(err))
	}
	if !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("error = %v, want ErrInvalidPayload rather than a refusal of the zero value", err)
	}
	if len(approvals.calls) != 0 {
		t.Errorf("recorded %+v for a payload it could not decode", approvals.calls)
	}
}

// A server with NO record keeping refuses rather than reporting success for an
// approval that vanishes at the next read. The nil check is what makes that
// answerable: a typed nil in the interface would pass a != nil test and panic.
func TestCmdApproveSpecPhase_RefusesWhenThereIsNoRecordToKeep(t *testing.T) {
	ws, hashes := seedSpec(t, map[string]string{"design.md": "# Design\n"})
	bus := &capturingBus{}

	_, err := cmdApproveSpecPhase(t.Context(), nil, ws, bus,
		approveReq(t, ".kiro/specs/demo", "design", hashes["design.md"]))

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (body %s)", statusOf(err), errText(err))
	}
	if len(bus.events) != 0 {
		t.Errorf("broadcast %d frames for an approval nothing kept", len(bus.events))
	}
}

// The store's own refusals reach the client as the class each one is: the decode
// bound is a 409 the reader can act on, and anything else is a 500 whose cause
// stays in the log rather than on the wire. Neither announces anything.
func TestCmdApproveSpecPhase_SurfacesTheStoresOwnRefusals(t *testing.T) {
	cases := map[string]struct {
		err        error
		wantStatus int
		wantReason string
	}{
		"at the spec bound": {specapproval.ErrTooMany, http.StatusConflict, "too_many_specs"},
		"write failed":      {errors.New("disk on fire"), http.StatusInternalServerError, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws, hashes := seedSpec(t, map[string]string{"design.md": "# Design\n"})
			approvals := &fakeApprovals{err: tc.err}
			bus := &capturingBus{}

			_, err := cmdApproveSpecPhase(t.Context(), approvals, ws, bus,
				approveReq(t, ".kiro/specs/demo", "design", hashes["design.md"]))

			if statusOf(err) != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.wantStatus, errText(err))
			}
			if got := reasonOf(err); got != tc.wantReason {
				t.Errorf("reason = %q, want %q", got, tc.wantReason)
			}
			if len(bus.events) != 0 {
				t.Errorf("broadcast %d frames for an approval that was not recorded", len(bus.events))
			}
			if statusOf(err) == http.StatusInternalServerError && strings.Contains(errText(err), "disk on fire") {
				t.Error("the store's own error text reached the client")
			}
		})
	}
}

// TestCmdApproveSpecPhase_AddressesASpecInARepository: a spec in a first-level repository's own
// .kiro tree is addressable, which is why Address takes the root list.
func TestCmdApproveSpecPhase_AddressesASpecInARepositorysOwnKiroTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kiro", "specs", "decoy"), 0o750); err != nil {
		t.Fatalf("seed the workspace's own root: %v", err)
	}
	dir := filepath.Join(root, "subrepo", ".kiro", "specs", "demo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("seed the nested spec: %v", err)
	}
	body := "# Design\n"
	if err := os.WriteFile(filepath.Join(dir, "design.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed design.md: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	approvals := &fakeApprovals{}

	_, err := cmdApproveSpecPhase(t.Context(), approvals, Workspace{Dir: root}, &capturingBus{},
		approveReq(t, "subrepo/.kiro/specs/demo", "design", hex.EncodeToString(sum[:])))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if len(approvals.calls) != 1 || approvals.calls[0].dir != "subrepo/.kiro/specs/demo" {
		t.Errorf("recorded %+v, want the nested spec's own dir", approvals.calls)
	}
}

// currentHashOf reads the compare-and-swap's current hash off a handler error,
// the same field writeErr lifts into the envelope.
func currentHashOf(err error) string {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.currentHash
	}
	return ""
}
