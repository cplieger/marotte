package marotte

import "context"

// --- Persistence ---

// No ChatStore interface: internal/translate, internal/agent and internal/command each declare
// the narrow interface they need of *chat.Store.

// --- Communication ---

// No Broadcaster interface: the one method is declared by internal/chat, internal/forges and
// internal/command's ChatAccess; *agent.Runtime satisfies all three.

// EngineOpts collects what the one kiro-cli engine is spawned with. Every field is engine-wide:
// KAS reads it at initialize or from the environment, so a change applies only after the engine
// restarts. Lifetime is REQUIRED.
//
// There is no MCPServers field. The user's MCP servers reach KAS through its own config file,
// which marotte renders: passing them on session/new would OUTRANK that file and freeze the set
// for the session's lifetime.
type EngineOpts struct {
	// Lifetime bounds the engine PROCESS: cancelling it closes its stdin and signals its tree.
	// REQUIRED (Start refuses nil), and NOT Start's ctx, which bounds only the handshake.
	Lifetime context.Context
	// Background runs fn on a goroutine the runtime waits for at shutdown, reporting false (and
	// running nothing) once it drains. The engine runs its late-reply hooks, close retries and
	// overflow answers on it. Nil runs them on a plain goroutine.
	Background func(fn func()) bool
	// ReapSession removes a KAS session's on-disk state; the engine calls it for a session KAS
	// created whose door was abandoned. Nil reaps nothing.
	ReapSession func(SessionID)
	// IgnoreFiles RESOLVES the ignore-file basenames KAS enforces for the connection, sent as
	// MethodPolicyIgnoreFilesChanged after initialize (CONNECTION-scope). Read at send time, so a
	// settings save during the start is not overwritten. The list leads with
	// settings.AgentIgnoreFloor. NIL or EMPTY sends nothing: `{files: []}` would CLEAR KAS's list.
	IgnoreFiles func(context.Context) []string
	// TerminalTimeout RESOLVES the shell tool's default timeout (0 = unset) for IgnoreFiles'
	// reason. Start reads it before initialize and again after, and sends
	// MethodTerminalSettingsChanged when the two differ. Nil keeps Features.TerminalCommandTimeoutMs.
	TerminalTimeout func(context.Context) int
	// ContentCollection RESOLVES whether model requests may be used for service improvement. KAS
	// holds the value per PROCESS and persists none of it, so every session door asserts it, read
	// when the session exists. readable false means the settings could not be read: a session door
	// still sends enabled (kiro-cli starts opted in), a live refresh keeps a value KAS confirmed
	// and sends enabled when none is confirmed. Nil sends nothing.
	ContentCollection func(context.Context) (enabled, readable bool)
	// ExtraArgs are the operator's kiro-cli flags (MAROTTE_KIRO_ACP_ARGS, bridge.ParseACPArgs),
	// appended after marotte's own.
	ExtraArgs []string
	// AuthArgs are the sign-in backend declaration's SpawnArgs, copied from nowhere else, so the
	// declaration alone decides the engine's authentication argv.
	AuthArgs []string
	// Features are the engine half of the Agent-capabilities settings.
	Features EngineFeatures
	// HeapMiB is the JavaScript heap limit in MiB composed into NODE_OPTIONS; 0 adds no flag and
	// leaves kiro-cli's default.
	HeapMiB int
	// SecretStorage declares `_meta.kiro.secretStorage` at initialize, so KAS asks marotte to hold
	// its MCP OAuth credentials. A COMMITMENT: KAS rethrows a store failure into the MCP connect
	// path, so it is set only when the runtime opened a store (internal/secretstore).
	SecretStorage bool
	// Knowledge gates BOTH knowledge rows, the capability that lists the bases in msg0 and the
	// setting that builds the Knowledge tool. It does not reach `_kiro/knowledge`, so the REST
	// surface keeps working with the switch off.
	Knowledge bool
	// DisableTelemetry turns KAS's agent telemetry off at initialize and the Rust binary's toolkit
	// client off in the environment; the zero value leaves both at kiro-cli's default, on.
	DisableTelemetry bool
}

// EngineFeatures are the Agent-capabilities settings KAS reads per engine. InfraSafetyMonitor,
// AutoRouting and AutoDelegation are "" (send nothing, kiro-cli's experiment decides), "on" or "off".
type EngineFeatures struct {
	InfraSafetyMonitor string
	AutoRouting        string
	AutoDelegation     string
	// TerminalCommandTimeoutMs is the shell tool's default timeout; 0 is unset.
	TerminalCommandTimeoutMs int
	SteeringReminders        bool
	// ModelFallback sends KAS's refusal and availability fallback flags.
	ModelFallback bool
}

// SessionRole names what a session is for: a chat's, a parentless run's host, or an observer
// pass that reads one answer and closes.
type SessionRole string

// The session roles.
const (
	SessionRoleChat     SessionRole = "chat"
	SessionRoleHost     SessionRole = "host"
	SessionRoleObserver SessionRole = "observer"
)

// SessionOpts collects what one session door sends. KAS persists none of the presets, steering
// or memory reflection, so a load sends them again.
type SessionOpts struct {
	// SessionID is the session to load; empty creates one under an id the engine mints.
	SessionID string
	Role      SessionRole
	// Model, Effort and Mode ride session/new's _meta.kiro and are repaired after it; a load
	// re-asserts the effort only.
	Model  string
	Effort string
	Mode   string
	// Thinking is the chat's thinking choice (ThinkingOn, ThinkingOff or empty), asserted after
	// either door when the session reports a different value. Effort is already capped for it.
	Thinking string
	// Presets are the KAS policy-preset ids of the active security profile (policyfile.Profile),
	// sent as _meta.kiro.policyPreset on BOTH doors since KAS persists neither. EMPTY is the Custom
	// profile and withholds the key.
	Presets []string
	// Steering is the chat's client steering, sent on both doors; nil sends no key. Chat sessions
	// only: it carries chat-user guidance a workflow host and an observer have no use for.
	Steering []ClientSteeringDoc
	// AdditionalDirectories are the directories beyond the workspace the session may reach, sent
	// on BOTH doors: KAS adopts the list per call, so a door without it drops them.
	AdditionalDirectories []string
	// Memory is the session's memory preference, sent on BOTH doors as
	// `_meta.kiro.settings.memory`. The zero value means mode "disabled". KAS freezes the mode per
	// session; the door re-asserts the reflection half after a load.
	Memory MemoryPreference
	// Features are the session half of the Agent-capabilities settings; an observer leaves them zero.
	Features SessionFeatures
	// Supervised sets `autopilot` FALSE after the door, requesting KAS's turn-approval gate.
	Supervised bool
	// DisableAutoCompaction turns KAS's own compaction off for this session (the chat policy's
	// switch off, or a point above 80%). KAS freezes it per session, so it takes effect at the
	// next door.
	DisableAutoCompaction bool
}

// SessionFeatures are the Agent-capabilities settings KAS reads per session. WorkValidation is
// "" (kiro-cli's experiment decides), "on" or "off".
type SessionFeatures struct {
	// SpecPlan is "" (off), "quick" or "full"; it only changes Autonomous mode.
	SpecPlan       string
	WorkValidation string
	// Workflows gives the agent its workflow tools; the zero value withholds them.
	Workflows            bool
	SpecAskClarification bool
}

// MemoryPreference mirrors KAS's `settings.memory` object field for field.
// Mode is "disabled", "read_only" or "read_write"; Reflection is background
// learning from the conversation, meaningful only under "read_write".
type MemoryPreference struct {
	Mode       string
	Reflection bool
}

// No ACPSession interface: internal/agent declares the session contract at its consumer.
// EngineOpts and SessionOpts stay here as types internal/bridge decodes.

// --- HTTP ---

// No RouteHandler interface: only internal/server consumes it, so it is declared there as
// routeHandler; internal/agent exports RouteRegistrar for the one value it hands out.

// No PushService interface: internal/agent and internal/server each declare the methods they use;
// internal/forges notifies through *agent.Runtime (PRNotifier).
