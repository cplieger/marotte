// Package kascap declares which capability keys marotte puts on the kiro-cli (KAS) ACP wire, on
// which call, why, and how KAS resolves each one. A map literal could state only the keys sent,
// not:
//
//   - which CALL carries a key (initialize, session/new and session/load, or session/prompt); a
//     key on the wrong door silently resolves to its absent default;
//   - how KAS RESOLVES it (a capability compared against true versus a setting read through
//     isSettingEnabled);
//   - whether an ABSENT key resolves TRUE (semanticReview);
//   - that a key is deliberately WITHHELD.
//
// table.go is the record; Capabilities, SessionMeta, ChildEnv and PromptMeta are its projections
// and the whole exported surface.
package kascap

// KAS decides it: the connection and environment doors are read once per engine, the session door once per
// session, the prompt door once per prompt. The zero value is deliberately not a door: a row
// that forgets to declare one fails TestDeclIsWellFormed rather than default onto a door.
type door string

const (
	// doorConnection is the initialize handshake, sent once per engine before any session exists.
	doorConnection door = "connection"
	// doorSession is the per-session door, sent on BOTH session/new and session/load. Both, or a
	// resumed chat quietly gets a different agent from a fresh one: KAS resolves a session key from
	// the call's own _meta first and falls back to the value persisted at creation, so a session
	// created before a key existed never gains it on load unless the client sends it there too.
	doorSession door = "session"
	// doorEnvironment is the engine's process environment, fixed at spawn. It carries the KIRO_FEATURE_* overrides KAS's env provider reads
	// ahead of the experiment service, and the KIRO_DISABLE_* switches. A row on
	// it uses resolverEnv (or resolverEnvJSON) and its key is the variable's whole name.
	doorEnvironment door = "environment"
	// doorPrompt is session/prompt's _meta.kiro, sent per prompt. Its rows read a Prompt through
	// promptGate.
	doorPrompt door = "prompt"
)

// resolver is how KAS reads a key, which decides where the key sits in the payload and what its
// value must look like. The zero value is not a resolver.
type resolver string

const (
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
	// resolverEnum is a key at the top level of _meta.kiro whose value is a string KAS maps onto
	// its own id set (outputStyle, shellType), so a bool there resolves to the default.
	resolverEnum resolver = "enum"
	// resolverText is a top-level key whose value is free text KAS stores verbatim (displayText).
	resolverText resolver = "text"
	// resolverEnumList is a top-level key whose value is an array of such ids. policyPreset is the
	// instance: KAS throws on a non-array and validatePresetIds on an unknown id.
	resolverEnumList resolver = "enum-list"
	// KAS's env provider accepts only the strings "true" and "false" (anything else warns and is
	// ignored), so a sent row's value is one of those.
	resolverEnv resolver = "env"
	// resolverEnvJSON is a KIRO_FEATURE_*_CONFIG arm, which KAS JSON-parses ahead of its experiment's
	// config (not through the env provider), so a sent value is a JSON object string.
	resolverEnvJSON resolver = "env-json"
)

func inSettings(r resolver) bool { return r == resolverSetting || r == resolverSettingObject }

// EngineSpawn carries the engine-wide facts the connection and environment rows read: each is fixed when the engine
// starts, so changing one needs an engine restart. Every field is a decision the caller already made.
type EngineSpawn struct {
	// InfraSafetyMonitor, AutoRouting and AutoDelegation are "", "on" or "off". Empty withholds the key so kiro-cli's
	// own experiment decides.
	InfraSafetyMonitor string
	AutoRouting        string
	AutoDelegation     string
	// TerminalCommandTimeoutMs is the shell tool's default timeout; 0 withholds the key (KAS's 120 s).
	TerminalCommandTimeoutMs int
	// SecretStorage is whether a credential store stands behind the secretStorage capability. See that row's because:
	// declaring the capability without a store is worse than declining it.
	SecretStorage bool
	// Knowledge gates TWO rows, the `knowledge` capability and the `knowledge` setting: splitting them shipped a
	// knowledge UI over a store the agent had no tool to query. Value-gated: the capability's resolver compares
	// `=== true`, so its key is present with a real boolean either way.
	Knowledge bool
	// SteeringReminders is a value-gated setting, always sent.
	SteeringReminders bool
	// DisableTelemetry sends telemetryEnabled false at initialize; the zero value is KAS's own default, on.
	DisableTelemetry bool
	// ModelFallback writes both KIRO_FEATURE_MODEL_FALLBACK_*_ENABLED=true; false writes neither.
	ModelFallback bool
}

// SessionSpawn carries the per-session facts the session rows read, sent on both session/new and session/load.
type SessionSpawn struct {
	// OverrideSessionID is the id marotte minted for a session/new; empty on session/load, which names its id itself.
	OverrideSessionID string
	// MemoryMode and MemoryReflection are the session's `settings.memory` preference. An EMPTY mode is sent as
	// "disabled", so a session that resolves no setting fails closed.
	MemoryMode string
	// SpecPlan is "" (off), "quick" or "full"; SpecAskClarification asks the spec's clarifying questions first.
	// Value-gated, always sent: on load KAS falls back to the persisted value when the key is absent.
	SpecPlan string
	// WorkValidation is "", "on" or "off"; empty withholds the key so kiro-cli's own experiment decides.
	WorkValidation string
	// Presets are the KAS policy-preset ids of the active security profile (policyfile.Profile). EMPTY means the
	// Custom profile, so the key is withheld. KAS persists neither door's ids, so both doors resolve from this field.
	Presets          []string
	MemoryReflection bool
	// DisableAutoCompaction turns KAS's own 80%/95% compaction off for this session; the row is value-gated, so false
	// is sent too.
	DisableAutoCompaction bool
	SpecAskClarification  bool
	// Workflows gives the agent its workflow tools; value-gated, always sent on both session doors.
	Workflows bool
}

// Prompt carries the per-prompt facts the prompt door's rows read.
type Prompt struct {
	// OutputStyle is the chosen output style id; empty is KAS's default and withholds the key.
	OutputStyle string
	// StepMessage is the user's words when the prompt is a message to a paused run step rather than
	// a chat prompt; empty for a chat prompt.
	StepMessage string
	// Label is a chat prompt's display label (PromptCommand.DisplayText); empty for a typed one.
	Label string
}

type decl struct {
	// key is the wire key, unqualified. A resolverSetting row's key is its
	// name inside _meta.kiro.settings, not a dotted path, because the
	// resolver already says which container it lands in.
	key string

	// value is the wire value for an ungated row. Set explicitly on every such
	// row rather than derived from the resolver, so the table never sends a
	// nil that JSON renders as null; TestDeclIsWellFormed enforces it.
	// A gated row leaves this nil and its gate supplies the value.
	value any

	// engineGate decides a connection or environment row when the engine starts, returning the value and whether to
	// send the key at all: secretStorage is always present with a runtime value, the terminal setting only when set.
	engineGate func(*EngineSpawn) (value any, present bool)

	// sessionGate decides a session row at each session door.
	sessionGate func(*SessionSpawn) (value any, present bool)

	// promptGate decides a prompt row per prompt.
	promptGate func(*Prompt) (value any, present bool)

	// door is the call that carries this key.
	door door

	// resolver is how KAS reads it, which also decides where it sits.
	resolver resolver

	// send is whether marotte puts this key on the wire at all.
	//
	// A send:false row is a DECLARATION that marotte deliberately withholds a
	// key, which is exactly what a map literal cannot express: a literal has no
	// row for a key it omits. Such a row must say why in table_because_test.go,
	// which TestNoSendWithoutReason enforces.
	send bool

	// settingOwned marks a sent environment row a Settings control decides in every state (OwnsEnv).
	settingOwned bool
}
