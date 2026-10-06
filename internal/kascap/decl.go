// Package kascap declares which capability keys marotte puts on the kiro-cli (KAS) ACP wire, on
// which call, why, and how KAS resolves each one. A map literal could state only the keys sent,
// not:
//
//   - which CALL carries a key (initialize, or session/new and session/load); a key on the wrong
//
// door silently resolves to its absent default;
//   - how KAS RESOLVES it (a capability compared against true versus a setting read through
//
// isSettingEnabled);
//   - whether an ABSENT key resolves TRUE (semanticReview);
//   - that a key is deliberately WITHHELD.
//
// table.go is the record; Capabilities and SessionMeta are its projections and the whole exported
// surface.
package kascap

// door names the ACP call that carries a capability key. KAS decides it: the connection door is
// read once per subprocess, the session door once per session.
type door string

const (
	// doorUnset is the zero value, and it is deliberately not a valid door: a
	// row that forgets to declare one must fail a well-formedness test rather
	// than default silently onto the connection door.
	doorUnset door = ""
	// doorConnection is the initialize handshake, sent once per kiro-cli
	// subprocess before any session exists.
	doorConnection door = "connection"
	// doorSession is the per-session door, sent on BOTH session/new and
	// session/load. Both, or a resumed chat quietly gets a different agent
	// from a fresh one: KAS resolves a session key from the call's own _meta
	// first and falls back to the value persisted at creation, so a session
	// created before a key existed never gains it on load unless the client
	// sends it there too.
	doorSession door = "session"
	// doorEnvironment is the kiro-cli child process environment, fixed at
	// spawn. It carries the KIRO_FEATURE_* overrides KAS's env provider reads
	// ahead of the experiment service, and the KIRO_DISABLE_* switches. A row on
	// it uses resolverEnv and its key is the variable's whole name.
	doorEnvironment door = "environment"
)

// resolver is how KAS reads a key, which decides where the key sits in the payload and what its
// value must look like.
type resolver string

const (
	// resolverUnset is the zero value and is not a valid resolver.
	resolverUnset resolver = ""
	// resolverCapability is a key at the TOP level of _meta.kiro that KAS
	// tests for truth directly (resolveCapabilities does `=== true`; a few
	// sites read it for truthiness). It is not a settings entry and does not
	// go through isSettingEnabled, so its value is a bare bool.
	resolverCapability resolver = "capability"
	// resolverSetting is a key under _meta.kiro.settings that KAS reads
	// through isSettingEnabled, which returns val.enabled for an object and
	// false for anything else, an absent key included. So its value is the
	// object {"enabled": true} and never a bare true, which would resolve
	// false and silently disable the feature.
	resolverSetting resolver = "setting"
	// resolverObject is a key at the top level of _meta.kiro whose value is an
	// OBJECT KAS destructures rather than a boolean it compares. hooks is the
	// instance: KAS requires the value to be an object carrying a v2 member
	// and then checks that member, so `hooks: true` would enable nothing.
	resolverObject resolver = "object"
	// resolverSettingObject is a key under _meta.kiro.settings that KAS reads
	// field by field rather than through isSettingEnabled, so its value is not
	// the {"enabled": …} object. memory ({mode, reflection}) is the instance.
	resolverSettingObject resolver = "setting-object"
	// resolverEnv is a child-environment variable. KAS's env provider accepts
	// only the strings "true" and "false" (anything else warns and is ignored),
	// so a sent row's value is one of those.
	resolverEnv resolver = "env"
)

// inSettings reports whether a resolver's key lands under _meta.kiro.settings.
func inSettings(r resolver) bool { return r == resolverSetting || r == resolverSettingObject }

// Spawn carries the per-bridge facts the gated rows read. Every field is a
// decision the caller has already made for THIS subprocess, never a preference
// this package could look up for itself.
type Spawn struct {
	// MemoryMode and MemoryReflection are the session's `settings.memory`
	// preference. An EMPTY mode is sent as "disabled", so a spawn that resolves
	// no setting (the utility bridge) fails closed.
	MemoryMode string
	// SpecPlan is "" (off), "quick" or "full"; SpecAskClarification asks the
	// spec's clarifying questions first. Value-gated, always sent: on load KAS
	// falls back to the persisted value when the key is absent.
	SpecPlan string
	// WorkValidation and InfraSafetyMonitor are "", "on" or "off". Empty
	// withholds the key so kiro-cli's own experiment decides.
	WorkValidation     string
	InfraSafetyMonitor string
	// Presets are the KAS policy-preset ids of the active security profile (policyfile.Profile).
	// EMPTY means the Custom profile, so the key is withheld. KAS re-reads the ids on session/new
	// and session/load and persists neither, so both doors must resolve from this one field.
	Presets []string
	// TerminalCommandTimeoutMs is the shell tool's default timeout; 0 withholds
	// the key (KAS's 120 s).
	TerminalCommandTimeoutMs int
	// SecretStorage is whether the caller has a credential store standing
	// behind the secretStorage capability. See that row's Because: declaring
	// the capability without a store is worse than declining it.
	SecretStorage bool
	// Hooks is whether this bridge opts into KAS's v2 hook engine.
	Hooks bool
	// Knowledge gates TWO rows, the `knowledge` capability and the `knowledge`
	// setting: splitting them shipped a knowledge UI over a store the agent had
	// no tool to query.
	//
	// Value-gated rather than presence-gated: the capability's resolver compares
	// `=== true`, so its key is present with a real boolean either way.
	Knowledge        bool
	MemoryReflection bool
	// ToolLoad is "Load MCP tools on demand", written to the child environment
	// as KIRO_FEATURE_TOOL_LOAD_ENABLED in both states.
	ToolLoad bool
	// DisableSessionTitles writes KIRO_DISABLE_SESSION_TITLE_LLM=true. Set on
	// run bridges only.
	DisableSessionTitles bool
	// DisableAutoCompaction turns KAS's own 80%/95% compaction off for this
	// session. The zero value keeps it on, which is what run and utility bridges
	// send; the disableAutoCompaction row is value-gated so false is sent too.
	DisableAutoCompaction bool
	SpecAskClarification  bool
	// InlineAgents and SteeringReminders are value-gated settings, always sent.
	InlineAgents      bool
	SteeringReminders bool
	// Workflows gives the agent its workflow tools; value-gated, always sent on
	// both session doors.
	Workflows bool
}

// decl is one capability key marotte can put on the wire, with everything a
// reader needs to judge it in one place.
type decl struct {
	// key is the wire key, unqualified. A resolverSetting row's key is its
	// name inside _meta.kiro.settings, not a dotted path, because the
	// resolver already says which container it lands in.
	key string

	// because is why marotte sends or withholds this key: what it buys, what breaks without it,
	// where the handler lives. Mandatory (TestEveryDeclHasABecause); one that restates the key
	// counts as missing.
	because string

	// value is the wire value for an ungated row. Set explicitly on every such
	// row rather than derived from the resolver, so the table never sends a
	// nil that JSON renders as null; TestDeclIsWellFormed enforces it.
	// A gated row leaves this nil and its gate supplies the value.
	value any

	// gate, when non-nil, decides this row at spawn time, returning the value and whether to send
	// the key at all: secretStorage is always present with a runtime value, hooks is present only
	// when enabled.
	gate func(*Spawn) (value any, present bool)

	// door is the call that carries this key.
	door door

	// resolver is how KAS reads it, which also decides where it sits.
	resolver resolver

	// absentTrue records that KAS resolves an ABSENT key to TRUE.
	//
	// It inverts the reading of send: on such a row, NOT sending the
	// key is what enables the feature, and sending {"enabled": false} is what
	// turns it off. semanticReview is the instance.
	absentTrue bool

	// send is whether marotte puts this key on the wire at all.
	//
	// A send:false row is a DECLARATION that marotte deliberately withholds a
	// key, which is exactly what a map literal cannot express: a literal has no
	// row for a key it omits. Such a row must say why in because, which
	// TestNoSendWithoutReason enforces.
	send bool
}
