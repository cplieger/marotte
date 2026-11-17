package marotte

import "time"

// SignInState is where the agent engine's Kiro sign-in stands, as every surface reports it.
type SignInState string

// The sign-in states.
const (
	// SignInOK: the engine's credential asks are answered.
	SignInOK SignInState = "ok"
	// SignInFailing: renewals have failed for the grace period and the identity provider still gives no usable one,
	// so marotte refuses the engine's credential asks until it does.
	SignInFailing SignInState = "failing"
	// SignInSignedOut: the sign-in ended without the user signing out, and only a sign-in brings it back.
	SignInSignedOut SignInState = "signed_out"
	// SignInNone: no account is signed in, never or on purpose.
	SignInNone SignInState = "none"
)

// SignInCause names why the state is not ok.
type SignInCause string

// The causes of a failing or signed_out state.
const (
	// SignInCauseProviderUnreachable: the identity provider gives no usable renewal (failing).
	SignInCauseProviderUnreachable SignInCause = "provider_unreachable"
	// SignInCauseRejected: the identity provider refused the renewal, so the record lapsed (signed_out).
	SignInCauseRejected SignInCause = "rejected"
	// SignInCauseRefused: Kiro refused a credential marotte served (signed_out).
	SignInCauseRefused SignInCause = "refused"
	// SignInCauseAPIKeyRefused: Kiro refused the KIRO_API_KEY the engine signs in with (signed_out). marotte refuses
	// every sign-in while the key is set, so only replacing it helps.
	SignInCauseAPIKeyRefused SignInCause = "api_key_refused"
)

// SignInHealthVersion orders snapshots that reach a reader by two paths, GET /api/whoami and the signin_health
// event, as EngineStatusVersion does: newer is the higher (BootMS, Seq), compared in that order.
type SignInHealthVersion struct {
	BootMS int64  `json:"boot_ms"`
	Seq    uint64 `json:"seq"`
}

// SignInHealth is the signin_health event's payload and GET /api/whoami's health.
type SignInHealth struct {
	// Since is when the episode began: the first failed renewal of a failing one, the moment a signed_out one was
	// seen. Absent while ok or none.
	Since   *time.Time          `json:"since,omitempty"`
	State   SignInState         `json:"state"`
	Cause   SignInCause         `json:"cause,omitempty"`
	Version SignInHealthVersion `json:"version"`
}

// The sentences every sign-in surface shares, so the banner, a failed turn's row, the push and the run tab's
// pause read the same cause.
const (
	// SignInUnreachableCause is the failing state's cause, the clause the run gate's pause reason starts with.
	SignInUnreachableCause = "the Kiro sign-in service is unreachable"
	// SignInRequiredCause is the signed_out state's remedy, the clause the run gate's sign-in pause starts with.
	SignInRequiredCause = "sign in to Kiro"
	// SignInFailedUnreachableReason is a turn's failure row while the identity provider is unreachable.
	SignInFailedUnreachableReason = "Kiro sign-in failed: " + SignInUnreachableCause +
		". marotte keeps trying, so send the message again once it answers."
	// SignInFailedRefusedReason is a turn's failure row once Kiro refused the sign-in itself.
	SignInFailedRefusedReason = "Kiro sign-in failed: Kiro refused the sign-in. Sign in again, then send the message again."
	// SignInAPIKeyCause is the api_key_refused cause, and SignInAPIKeyRemedy its remedy.
	SignInAPIKeyCause  = "Kiro refused the KIRO_API_KEY marotte signs in with"
	SignInAPIKeyRemedy = "Replace the key in marotte's environment, then restart marotte"
	// SignInFailedAPIKeyReason is a turn's failure row once Kiro refused the API key.
	SignInFailedAPIKeyReason = "Kiro sign-in failed: " + SignInAPIKeyCause + ". " + SignInAPIKeyRemedy + "."
	// SignInFailedReason is a replayed turn's failure row, whose cause at the time is not known.
	SignInFailedReason = "Kiro sign-in failed. Sign in again if it keeps failing."
)
