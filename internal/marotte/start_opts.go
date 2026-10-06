package marotte

import "context"

// --- Persistence ---

// There is no ChatStore interface here. Each consumer (internal/translate,
// internal/agent, internal/command) declares the narrow interface it needs of
// *chat.Store, and RegisterRoutes is reached only through internal/server's own
// routeHandler, never through a chat-store interface.

// --- Communication ---

// There is no Broadcaster interface here. Sending an event to every connected
// SSE client is ONE method, and its two consumers declare it themselves:
// internal/chat (chat and message lifecycle) and internal/forges (a forge
// connection change). internal/command declares the same method as a member of
// its ChatAccess role. *agent.Runtime satisfies all three.

// StartOpts collects the parameters for ACPBridge.Start. Lifetime is
// REQUIRED; every other field is optional, so a StartOpts carrying nothing but
// a Lifetime creates a new session with no model override.
//
// There is no MCPServers field. The user's MCP servers reach KAS through its own
// config file, which marotte renders — passing them on session/new would OUTRANK
// that file and freeze the set for the session's lifetime.
type StartOpts struct {
	// Lifetime bounds the kiro-cli SUBPROCESS: cancelling it closes its stdin and signals its tree.
	// REQUIRED (Start refuses nil), and NOT Start's ctx, which bounds only the handshake. Pass the
	// runtime's shutdown context: under a turn context the first prompt's end closes stdin, and the
	// children holding stdout keep the readLoop from seeing EOF, so the dead bridge stays registered.
	Lifetime context.Context
	// IgnoreFiles RESOLVES the ignore-file basenames KAS enforces for this connection, sent as
	// MethodPolicyIgnoreFilesChanged after initialize and before the first session verb (the only
	// door; CONNECTION-scope). A resolver read at send time, so a settings save between
	// registration and Start is not overwritten. The list leads with settings.AgentIgnoreFloor.
	// NIL or EMPTY sends nothing: `{files: []}` would CLEAR KAS's list. It sits ahead of the
	// []string fields for fieldalignment.
	IgnoreFiles func(context.Context) []string
	// TerminalTimeout RESOLVES the shell tool's default timeout (0 = unset) for
	// IgnoreFiles' reason: a save during the spawn finds a bridge that refuses the
	// live push. Start reads it before initialize and again after, and sends
	// MethodTerminalSettingsChanged when the two differ. Nil keeps
	// Features.TerminalCommandTimeoutMs.
	TerminalTimeout func(context.Context) int
	// ContentCollection RESOLVES whether model requests from this process may be used
	// for service improvement. KAS holds the value per PROCESS and persists none of it,
	// so the bridge asserts it on BOTH session doors, read when the session exists
	// rather than at spawn, so a save landing mid-spawn is not overwritten. Nil sends
	// nothing.
	ContentCollection func(context.Context) bool
	SessionID         string
	Model             string
	Effort            string
	AgentEngine       string
	Mode              string
	// Thinking is the chat's thinking choice (ThinkingOn, ThinkingOff or empty),
	// asserted after session/new and session/load when the session reports a
	// different value. Effort is already capped for it by the caller.
	Thinking string
	// Presets are the KAS policy-preset ids of the active security profile (policyfile.Profile),
	// sent as _meta.kiro.policyPreset on BOTH session/new and session/load since KAS persists
	// neither. EMPTY is the Custom profile and withholds the key. Per session: KAS cannot change a
	// running session's policy. Set on the utility bridge too, which answers GET /api/permissions.
	Presets []string
	// ExtraArgs are operator-supplied kiro-cli launch flags
	// (MAROTTE_KIRO_ACP_ARGS), already filtered, appended after the args
	// marotte derives itself. Set on CHAT bridges only — never on the utility
	// bridge, where an `--effort max` would spend real credits generating a
	// two-word title. See bridge.FilterACPArgs.
	ExtraArgs []string
	// Steering is the session door's client steering, sent on session/new and
	// session/load alike (KAS persists none of it); nil sends no key. Chat
	// bridges only: it carries the chat-user guidance a workflow step, the
	// utility session and the TUI have no use for.
	Steering []ClientSteeringDoc
	// Memory is the session's memory preference, sent on BOTH session doors as
	// `_meta.kiro.settings.memory`. The zero value means mode "disabled" (the
	// kascap gate maps an empty mode there), which is what the utility bridge
	// relies on. KAS freezes the mode per session; the bridge re-asserts the
	// reflection half after a session/load.
	Memory MemoryPreference
	// Features are the Agent-capabilities settings that ride KAS's doors,
	// resolved per spawn. The utility bridge leaves them zero.
	Features AgentFeatures
	// EnableHooks opts the session into KAS's v2 hook engine by declaring
	// _meta.kiro.hooks={enabled,v2} at initialize. Set on every bridge: the
	// utility bridge (the hooks list|setEnabled RPCs), chat bridges and run
	// bridges (workspace .kiro/hooks/*.json autofire during a turn or a step).
	// In v2 mode KAS runs the hooks itself and never calls the client back.
	EnableHooks bool
	// Supervised requests KAS's turn-approval gate for this session, by setting
	// the `autopilot` config option to FALSE at session/new.
	//
	// A value passed once at creation, not a flag marotte enforces: it persists
	// into KAS's own session metadata, so it survives session/load and never needs
	// re-asserting. Holding writes back is KAS's job, not marotte's.
	Supervised bool
	// SecretStorage declares `_meta.kiro.secretStorage` at initialize, so KAS asks this client to
	// hold its MCP OAuth credentials. A COMMITMENT: KAS rethrows a store failure into the MCP
	// connect path, so it is set only when the runtime opened a store (internal/secretstore);
	// undeclared, KAS re-registers per spawn. Set on chat and utility bridges alike.
	SecretStorage bool
	// ToolSearch ("Load MCP tools on demand") reaches KAS through the child
	// environment, not the wire: kascap's environment door writes
	// KIRO_FEATURE_TOOL_LOAD_ENABLED (kascap.ChildEnv), whose arm
	// keeps the tools array fixed. The `_meta.kiro.settings.toolSearch` mode
	// grew the array on every load and is withheld (see the kascap row).
	// Resolved per spawn; KAS freezes it at session creation.
	ToolSearch bool
	// Knowledge gates BOTH knowledge rows, the capability that lists the bases
	// in msg0 and the setting that builds the Knowledge tool: gating one alone
	// lists bases the agent cannot query. It does not reach `_kiro/knowledge`,
	// so the REST surface keeps working with the switch off. Resolved per
	// spawn; KAS freezes it at session creation.
	Knowledge bool
	// DisableSessionTitles writes KIRO_DISABLE_SESSION_TITLE_LLM=true into the
	// child environment. Run bridges only: nothing renders a run step session's
	// title, so the LLM title call is spend with no reader.
	DisableSessionTitles bool
	// DisableAutoCompaction turns KAS's own compaction off for this session (the
	// chat policy's switch off, or a point above 80%). Chat spawns only; run and
	// utility bridges leave it false. KAS freezes it per session, so it takes
	// effect at the next session/new or session/load.
	DisableAutoCompaction bool
}

// AgentFeatures carries the Agent-capabilities settings a spawn sends KAS.
// WorkValidation and InfraSafetyMonitor are "" (send nothing, kiro-cli's
// experiment decides), "on" or "off".
type AgentFeatures struct {
	// SpecPlan is "" (off), "quick" or "full"; it only changes Autonomous mode.
	SpecPlan           string
	WorkValidation     string
	InfraSafetyMonitor string
	// TerminalCommandTimeoutMs is the shell tool's default timeout; 0 is unset.
	TerminalCommandTimeoutMs int
	SpecAskClarification     bool
	InlineAgents             bool
	SteeringReminders        bool
	// Workflows gives the agent its workflow tools; the utility bridge's zero
	// value withholds them.
	Workflows bool
}

// MemoryPreference mirrors KAS's `settings.memory` object field for field.
// Mode is "disabled", "read_only" or "read_write"; Reflection is background
// learning from the conversation, meaningful only under "read_write".
type MemoryPreference struct {
	Mode       string
	Reflection bool
}

// No ACPBridge interface: internal/agent declares the subprocess contract at its consumer, at seven
// widths. StartOpts stays here as a type internal/bridge decodes.

// --- HTTP ---

// No RouteHandler interface: only internal/server consumes it, so it is declared there as
// routeHandler; internal/agent exports RouteRegistrar for the one value it hands out.

// There is no PushService interface here. Its consumers declare what they use:
// internal/agent 4 of the 8 methods (send, ask, reload, close), internal/server 2
// (mount the routes, write the toggles), internal/forges 2 (its PRNotifier).
//
// Subscribe and Unsubscribe were members no consumer ever reached through an
// interface — *push.Service's own HTTP handlers call them on itself — so they
// are simply methods on the concrete type now.

// --- AI Utilities ---

// There is no UtilityPrompter interface here. AI-backed prompt generation is a
// single method, and its two consumers declare it themselves: internal/server
// (explain-error, explain-diff) and internal/git (commit message, PR
// description, branch name). *agent.Runtime satisfies both.
