package marotte

// SteerIDPrefix is what KAS puts in front of a steer's message id. It also keeps
// a steer's id space clear of an agent notice's, which takes `notify-` instead.
const SteerIDPrefix = "steer-"

// SteerIDFor mints the id a steer will be known by: `steer-<messageID>`. Derivable because KAS's
// handleSessionSteer prefixes the sent messageId and stamps it on the response and on
// `steering_queued`, so the ledger can be written before the RPC. static-src/store.ts steerIDFor is
// the twin and must agree.
func SteerIDFor(messageID string) string {
	return SteerIDPrefix + messageID
}

// SteerOrigin says WHOSE words a mid-turn steer carries.
//
// KAS's steering buffer carries the user's correction, a step's report into the
// chat that launched it, a run-completion nudge, and that chat's send_message
// into a step. The live steering frames carry no sender (`notificationSeverity`
// is only a text-prefix sniff), so the server records the steers IT sent and
// everything else is the agent's. Only a replayed row carries KAS's
// `notification.sender`, which is how the parent's message to a step is told apart.
type SteerOrigin string

// The origins. Each string is the wire value AND the client's SteerOrigin union
// member, so a rename here is a cross-language change.
//
// There is deliberately no "unknown": every producer answers one of these, and
// another value would put a label the client has no wording for on the wire.
const (
	// SteerOriginUser is a steer this server sent on the user's behalf.
	SteerOriginUser SteerOrigin = "user"
	// SteerOriginAgent is a steer that arrived from KAS's own buffer: a
	// workflow step's report, or a run-completion nudge.
	SteerOriginAgent SteerOrigin = "agent"
	// SteerOriginParent is the launching chat's send_message into one of its
	// run's steps, read in that step's session.
	SteerOriginParent SteerOrigin = "parent"
)

// SteerState says whether the model ever READ a mid-turn steer. Rendering an
// undelivered correction like a delivered one is a false statement about the
// reader's own message.
//
// ABSENT means NOT KNOWN, a third answer rather than a missing value: neither the
// legacy population nor a session/load replay row carries one, because KAS's log
// records a steer without saying whether the model consumed it. Unknown renders as
// the NEUTRAL note; dropped would claim non-delivery for a steer that may have landed.
type SteerState string

// The two states marotte can observe, each from its own frame. There is
// deliberately no "unknown" member: absence carries that, and a third value
// would put on the wire a state the note has no wording for.
const (
	// SteerStateRead is a steer the model read, from steering_injected.
	SteerStateRead SteerState = "read"
	// SteerStateDropped is a steer a turn boundary cleared unread, from
	// steering_cleared for an id KAS's buffer still held. The wire word is the
	// buffer's; the reader sees "Not delivered".
	SteerStateDropped SteerState = "dropped"
)
