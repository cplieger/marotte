package command

// The spec-phase approval command.
//
// It RECORDS a human sign-off, it does not ENFORCE one: there is no phase-order
// gate, nothing here refuses to run a task because design is unapproved, and no
// control is disabled by an approval. The agent writes a spec's documents through
// its own file tools, so this server cannot enforce an order without owning that
// access; what it can do is preserve which exact version was signed off.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/specapproval"
)

// SpecApprovals is the approval record as this command uses it. Declared at the
// consumer; the store is internal/specapproval's.
type SpecApprovals interface {
	Approve(ctx context.Context, dir, phase, hash, user string) error
}

// CmdApproveSpecPhase records that a human approved one phase of a spec, against
// the version they approved.
//
// The chat id is EMPTY on purpose and is not validated: a spec is
// workspace-global rather than a chat's, which is why the invalidation event is
// workspace-global too.
//
// COMPARE-AND-SWAP, per Kiro Crew's own handler: the phase and the hash are
// validated for SHAPE first, then the document is RE-READ and its hash compared,
// and a mismatch is refused with the CURRENT hash so the client can re-read
// rather than have its claim trusted. Approving a version you have not seen
// records nothing meaningful.
//
// The re-read goes through internal/spec's confined loader, never an ambient
// path: the spec roots are os.Root-confined so a symlink out of the tree is
// refused by the kernel, and reading the document any other way would bypass
// exactly that.
func CmdApproveSpecPhase(ctx context.Context, approvals SpecApprovals, ws Workspace, bus Broadcaster, cmd *marotte.ClientCommand) (any, error) {
	if approvals == nil {
		// No config dir, so nothing would keep the record. Refusing is the honest
		// answer: reporting success for an approval that vanishes at the next
		// read is worse than saying the surface is unavailable.
		return nil, StatusError(http.StatusServiceUnavailable, errApprovalsUnavailable)
	}
	var p marotte.ApproveSpecPhaseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !specapproval.ValidPhase(p.Phase) {
		return nil, StatusErrorReason(http.StatusBadRequest, "invalid_phase", errInvalidPhase)
	}
	if !specapproval.ValidHash(p.Hash) {
		return nil, StatusErrorReason(http.StatusBadRequest, "invalid_hash", errInvalidHash)
	}
	root, name, ok := spec.Address(spec.Roots(ws.Dir), p.Dir)
	if !ok {
		return nil, StatusError(http.StatusNotFound, errSpecNotFound)
	}

	sp, err := spec.Load(ctx, root, name, filebrowse.MaxFileSize)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, spec.ErrNoDocs):
		return nil, StatusError(http.StatusNotFound, errSpecNotFound)
	case err != nil:
		slog.Warn("approve_spec_phase: load failed",
			"dir", logsafe.Field(p.Dir), keyError, logsafe.Field(err.Error()))
		return nil, StatusError(http.StatusBadGateway, errSpecUnreadable)
	}

	// The FIRST document with that role in display order, which is the document
	// the client's phase segment shows and therefore the one the reader
	// approved: requirements.md and bugfix.md share the requirements role, and
	// spec.Load has already ordered them.
	current := ""
	for i := range sp.Docs {
		if string(sp.Docs[i].Role) == p.Phase {
			current = sp.Docs[i].Hash
			break
		}
	}
	if current != p.Hash {
		// The current hash rides the refusal so a client can tell the document
		// moved from a claim it got wrong, and can say so. It is NOT a value to
		// re-POST unread — the reader has to review the new version. An absent
		// document answers the empty string, which is honest: there is nothing
		// left to approve.
		return nil, StatusErrorCurrentHash(http.StatusConflict, "doc_changed", current, errDocChanged)
	}

	// User is empty because marotte knows no identity to record (see
	// marotte.SpecApproval.User). Recorded as the empty string rather than
	// invented.
	if err := approvals.Approve(ctx, p.Dir, p.Phase, p.Hash, ""); err != nil {
		if errors.Is(err, specapproval.ErrTooMany) {
			return nil, StatusErrorReason(http.StatusConflict, "too_many_specs", err)
		}
		slog.Warn("approve_spec_phase: record failed",
			"dir", logsafe.Field(p.Dir), "phase", logsafe.Field(p.Phase), keyError, err)
		return nil, StatusError(http.StatusInternalServerError, errApprovalNotRecorded)
	}

	// Pure INVALIDATION, workspace-global: the client refetches that spec, which
	// is where the derived Stale comes from. Broadcast after the record is
	// durable, so a client that refetches on the frame cannot read the old state.
	bus.Broadcast(ctx, marotte.ServerEvent{
		Type:    marotte.EventSpecApproved,
		Payload: marotte.SpecApprovedPayload{Dir: p.Dir},
	})

	return responseWith(map[string]any{"phase": p.Phase, "hash": p.Hash}), nil
}

// The prose this command answers with. Each is a sentence a reader can act on;
// the machine-readable half is the reason on the status error.
var (
	errInvalidPhase         = errors.New("phase must be requirements, design or tasks")
	errInvalidHash          = errors.New("hash must be a sha256 hex digest")
	errSpecNotFound         = errors.New("spec not found")
	errSpecUnreadable       = errors.New("the spec could not be read")
	errDocChanged           = errors.New("the document changed since you reviewed it — reload before approving")
	errApprovalNotRecorded  = errors.New("the approval could not be recorded")
	errApprovalsUnavailable = errors.New("spec approvals are unavailable on this server")
)
