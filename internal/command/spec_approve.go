package command

// The spec-phase approval RECORDS a sign-off and enforces nothing: the agent writes spec documents
// through its own file tools, so the server cannot enforce an order; it preserves which exact
// version was signed off.

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

// CmdApproveSpecPhase records that a human approved one phase of a spec, against the version they
// approved. The chat id is empty on purpose: a spec is workspace-global.
// Compare-and-swap: phase and hash are shape-validated, the document is RE-READ through
// internal/spec's os.Root-confined loader, and a mismatch is refused with the CURRENT hash so the
// client re-reads rather than have its claim trusted.
func CmdApproveSpecPhase(ctx context.Context, approvals SpecApprovals, ws Workspace, bus Broadcaster, cmd *marotte.ClientCommand) (any, error) {
	if approvals == nil {
		// Without a config dir nothing keeps the record, so refuse rather than report an approval
		// that vanishes.
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

	// The FIRST document with that role in display order is the one the client's phase segment
	// shows: requirements.md and bugfix.md share the role.
	current := ""
	for i := range sp.Docs {
		if string(sp.Docs[i].Role) == p.Phase {
			current = sp.Docs[i].Hash
			break
		}
	}
	if current != p.Hash {
		// The current hash lets the client say the document moved; it is not a value to re-POST
		// unread. An absent document answers "".
		return nil, StatusErrorCurrentHash(http.StatusConflict, "doc_changed", current, errDocChanged)
	}

	if err := approvals.Approve(ctx, p.Dir, p.Phase, p.Hash, ""); err != nil {
		if errors.Is(err, specapproval.ErrTooMany) {
			return nil, StatusErrorReason(http.StatusConflict, "too_many_specs", err)
		}
		slog.Warn("approve_spec_phase: record failed",
			"dir", logsafe.Field(p.Dir), "phase", logsafe.Field(p.Phase), keyError, err)
		return nil, StatusError(http.StatusInternalServerError, errApprovalNotRecorded)
	}

	// Pure invalidation, broadcast after the record is durable so a refetch cannot read the old
	// state.
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
	errDocChanged           = errors.New("the document changed since you reviewed it. Reload before approving")
	errApprovalNotRecorded  = errors.New("the approval could not be recorded")
	errApprovalsUnavailable = errors.New("spec approvals are unavailable on this server")
)
