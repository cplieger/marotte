// Package signin owns marotte's sign-in to the agent backends: one identity record per backend,
// kept in <configDir>/signin.json, renewed when the backend asks for it, and answered to the
// running engine on the backend's host callback.
//
// The vault is backend-neutral. It stores each backend's record opaquely and never compares a
// backend id: how a backend signs in, when its record needs renewing, what its callback answer and
// refusals look like and whether a running engine can take a new account live are all the
// Backend's. The vault owns storage, the generation fence that discards a renewal a sign-in or
// sign-out overtook, the per-backend renewal singleflight, and the commit order that makes a
// change visible only once the file holds it.
package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// BackendID names a backend ("kiro").
type BackendID string

// FlowKind names a sign-in flow a backend offers.
type FlowKind string

// FlowDevice is the device-code flow: the user opens a URL and enters a code.
const FlowDevice FlowKind = "device"

// Declaration is how a backend authenticates, read by whoever spawns that backend.
type Declaration struct {
	Backend BackendID
	// HostCallback is the agent-to-client method the backend asks for a credential on, or "" when
	// the backend signs itself in.
	HostCallback string
	// SpawnArgs are the argv the backend needs for that choice.
	SpawnArgs []string
	Flows     []FlowKind
}

// StatusKind is the arm of a Status.
//
//deadset:ignore DS1101 -- the type of Status.Kind and Change's edges, so a caller can name what it compares
type StatusKind string

// The three arms of a Status.
const (
	StatusSignedIn    StatusKind = "signed_in"
	StatusSignedOut   StatusKind = "signed_out"
	statusUnavailable StatusKind = "unavailable"
)

// Identity is the account label a backend derives from its record, every field user-displayable.
type Identity struct {
	// Method is the sign-in method in user words ("Logged in with Builder ID").
	Method      string
	AccountType string
	StartURL    string
	Region      string
	// Email is "" when the backend could not learn it.
	Email string
}

// Status is one backend's sign-in state.
type Status struct {
	Kind StatusKind
	// Reason is user text, unavailable only.
	Reason string
	// Label is the desired record's, signed_in only.
	Label Identity
	// SignIn is the latest sign-in attempt. The attempt StartSignIn numbered has finished once
	// SignIn.ID has moved past it or State is not AttemptPending; a signed_in Kind alone proves
	// nothing while a switch keeps the old account in place.
	SignIn Attempt
}

// AttemptState is where a sign-in attempt stands.
//
//deadset:ignore DS1101 -- the type of Attempt.State, read through Status.SignIn
type AttemptState string

// The states of an Attempt. Every attempt leaves AttemptPending for exactly one of the others.
const (
	AttemptPending   AttemptState = "pending"
	AttemptSignedIn  AttemptState = "signed_in"
	AttemptFailed    AttemptState = "failed"
	attemptCancelled AttemptState = "cancelled"
)

// Attempt is one sign-in attempt, the zero value when none was made since Open.
//
//deadset:ignore DS1101 -- the type of Status.SignIn
type Attempt struct {
	State AttemptState
	// Error is the user text of a failed attempt.
	Error string
	// ID numbers a vault's attempts from 1.
	ID uint64
}

// FlowParams selects a flow and its inputs.
type FlowParams struct {
	Flow FlowKind
	// StartURL is the organisation's sign-in portal, or "" for the backend's personal account.
	StartURL string
	// Region is the organisation portal's region, or "" for the backend's default.
	Region string
}

// Pending is what the user must see to approve a device sign-in.
type Pending struct {
	VerificationURL string
	UserCode        string
	ExpiresIn       time.Duration
	// Attempt is the attempt's Attempt.ID; DeviceStart carries none.
	Attempt uint64
}

// DeviceStart is a begun device flow: what the user sees, plus the backend's own continuation,
// which the vault hands back to Poll untouched.
type DeviceStart struct {
	State any
	Pending
}

// Change reports a committed edge of a backend's desired record. AccountPending is the pair's
// state after the change.
type Change struct {
	Backend        BackendID
	From           StatusKind
	To             StatusKind
	AccountPending bool
}

// Answerability is the last state the vault observed for answering the running engine's host callback, read off
// the record the engine is answered from, which a pending account switch keeps apart from Status's. It contacts no
// provider: a record whose lease went stale reads Answerable until a renewal fails.
type Answerability int

// The arms of Answerability.
const (
	// Answerable: the record is signed in, and no renewal since its last usable one failed as ProviderUnreachable
	// describes.
	Answerable Answerability = iota + 1
	// NeedsSignIn: no record, or a lapsed one; only a sign-in changes it.
	NeedsSignIn
	// ProviderUnreachable: the record needs a renewal and the identity provider gave no usable one, either none
	// within an asker's window or only a lease too short to use. The vault keeps renewing it and reports
	// Answerable once a renewal returns a usable lease.
	ProviderUnreachable
)

// Credential is a backend's record, stored and handed back by the vault without being read.
type Credential struct {
	Data json.RawMessage
}

// Refusal is why a host callback is refused.
type Refusal int

// The refusals a backend words for its host callback.
const (
	// RefusedSignedOut: no record, or its grant was rejected earlier.
	RefusedSignedOut Refusal = iota + 1
	// RefusedRejected: the identity provider refused the renewal just now.
	RefusedRejected
	// RefusedUnreachable: the renewal failed transiently and the record can no longer be served.
	RefusedUnreachable
)

func (a Answerability) String() string {
	switch a {
	case Answerable:
		return "answerable"
	case NeedsSignIn:
		return "needs_sign_in"
	case ProviderUnreachable:
		return "provider_unreachable"
	default:
		return fmt.Sprintf("answerability(%d)", int(a))
	}
}

// Backend is one agent backend's sign-in. The vault calls it with no lock held.
type Backend interface {
	Declaration() Declaration
	// Begin starts a flow and returns what the user must see; ctx is the HTTP request's. Bad
	// params are an error wrapping ErrInvalidParams.
	Begin(ctx context.Context, p FlowParams) (DeviceStart, error)
	// Poll blocks until the user approved, denied or the code expired, or ctx ends. A failure the
	// user should read is a *FailureError.
	Poll(ctx context.Context, d DeviceStart) (Credential, error)
	// Refresh renews c; an error wrapping ErrRejected means the grant is dead.
	Refresh(ctx context.Context, c Credential) (Credential, error)
	// Validate refuses a credential the backend cannot use; the vault calls it on every record it
	// reads from the file or receives from Poll.
	Validate(c Credential) error
	Label(c Credential) Identity
	// LiveSwitchable reports whether an engine last answered from can take to with no restart.
	LiveSwitchable(from, to Credential) bool

	// Fresh reports whether c can be answered at now with no renewal first.
	Fresh(c Credential, now time.Time) bool
	// Servable reports whether c, which is not Fresh, can still be answered; the vault answers no
	// credential that is neither.
	Servable(c Credential, now time.Time) bool
	// Result is the host-callback result for c at now.
	Result(c Credential, now time.Time) (json.RawMessage, error)
	// Refusal is the host-callback error for r.
	Refusal(r Refusal) *marotte.RPCError
}

// FailureError is a sign-in failure whose Text the user reads as-is.
type FailureError struct {
	Err  error
	Text string
}

func (e *FailureError) Error() string {
	if e.Err == nil {
		return e.Text
	}
	return e.Text + ": " + e.Err.Error()
}

func (e *FailureError) Unwrap() error { return e.Err }

var (
	// ErrRejected marks a renewal the identity provider refused: the grant is dead.
	ErrRejected = errors.New("signin: the identity provider rejected the renewal")
	// ErrInvalidParams marks FlowParams a backend refuses.
	ErrInvalidParams = errors.New("signin: invalid sign-in parameters")
	// ErrSignInInProgress is StartSignIn while the same backend's previous attempt is running.
	ErrSignInInProgress = errors.New("signin: a sign-in is already in progress")
	// ErrSignInCancelled is StartSignIn whose attempt a SignOut cancelled before it returned.
	ErrSignInCancelled = errors.New("signin: the sign-in was cancelled")
	errUnknownBackend  = errors.New("signin: no such backend")
	errFlowUnsupported = errors.New("signin: the backend does not offer this flow")
	// errNotSaved is SignOut whose change the file did not take: the account is still signed in.
	errNotSaved = errors.New("signin: the saved sign-in could not be changed")
	// ErrInDoubt is SignOut whose outcome was not known in time: the filesystem still held the
	// write, or shutdown came before the file could be read back. Status keeps the last state the
	// file was known to hold, and shows the sign-out once the file does.
	ErrInDoubt = errors.New("signin: marotte could not confirm which sign-in is saved")
)
