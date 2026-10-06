package kascap

import (
	"cmp"
	"strconv"
)

// enabledMember is the one member name KAS reads inside a settings entry (isSettingEnabled returns
// val.enabled). A misspelling is invisible on the wire: it resolves to undefined, neither true nor
// false.
const enabledMember = "enabled"

// enabled is the shape every _meta.kiro.settings entry marotte SENDS takes. A
// helper rather than inline literals because KAS reads each one through the same
// absent-key-means-false resolver, so the shape is a contract shared by all of
// them rather than a coincidence repeated at each row.
func enabled() map[string]any { return map[string]any{enabledMember: true} }

// enabledIf is enabled()'s runtime twin, for a row whose value comes from a Spawn field.
// `{"enabled": false}` is how KAS is told a feature is OFF, a different statement from an absent
// key where a key must veto something or answer a resolver comparing a real boolean.
func enabledIf(on bool) map[string]any { return map[string]any{enabledMember: on} }

// hooksValue is the v2 hook-engine opt-in object. Not a bare true: KAS requires
// an object carrying a v2 member and then checks that member (resolverObject).
func hooksValue() map[string]any { return map[string]any{"enabled": true, "v2": true} }

// specPlanValue builds settings.specPlan: off sends {"enabled": false}; on adds
// KAS's workflow and the inverted clarification flag.
func specPlanValue(s *Spawn) map[string]any {
	if s.SpecPlan == "" {
		return enabledIf(false)
	}
	return map[string]any{enabledMember: true, "workflow": s.SpecPlan, "skipClarification": !s.SpecAskClarification}
}

// choiceValue maps a follow-kiro-cli setting onto a row: empty withholds the
// key so KAS's own resolution decides.
func choiceValue(choice string) (any, bool) {
	switch choice {
	case "on":
		return enabledIf(true), true
	case "off":
		return enabledIf(false), true
	}
	return nil, false
}

// table is every capability key marotte knows about, sent or withheld.
//
// Row order is presentation only. Both builders emit maps, and encoding/json
// sorts map keys, so no wire byte depends on this order.
var table = []decl{
	{
		key:      "resolvesSteeringCommands",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
		because: `WITHHELD on purpose. KAS reads clientMeta.resolvesSteeringCommands at every
non-agent-initiated prompt and, unless it is true, activates the manual or
auto steering document a leading /<name> names for that turn. marotte relies on
that native activation (the slash menu lists those documents), so sending true
would turn every /<steering> into prose the model reads.`,
	},
	{
		key:      "notifications",
		door:     doorConnection,
		resolver: resolverObject,
		send:     false,
		because: `WITHHELD on purpose. notifications.documentsChanged !== false gates
_kiro/steering/documents_changed, which marotte consumes for each steering
document's configIssues (the /docs badges). Withholding the object keeps the
default, on.`,
	},
	{
		key:      "openExternalUrl",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.openExternalUrl opts into KAS's _kiro/openExternalUrl
request (open a URL for the user, e.g. an MCP OAuth page).
openExternalUrl advertises that we can open a URL for the user; KAS (v3) gates
its _kiro/openExternalUrl request on it (proactively opening an MCP server's
OAuth page — the client surfaces a clickable banner, no auto-open; see
agent/bridge_v3_hostreq.go).`,
	},
	{
		key:      "infrastructureSafety",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.infrastructureSafety opts into KAS's Infrastructure-Safety
gate for infrastructure-as-code tool calls. It is NOT dormant on individual
accounts: initialize installs the gate (on a hooks-enabled bridge) when this
capability is declared AND the monitor is on, and the monitor reads the client's
infraSafetyMonitor setting when one is sent, else the kiroInfraSafetyMonitor
experiment, which AWS has ramped on for individual accounts (measured on
2.27.0's "Active experiments" log line). marotte sends no infraSafetyMonitor, so
the experiment decides. Required for the gate's statusChanged/propertiesChanged
notifications to surface (translate/safety.go); infraSafetyEnforce can block
infra-as-code writes. Distinct from supervised mode (KAS's autopilot gate).`,
	},
	{
		key:      "userInput",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.userInput opts into KAS's _kiro/userInput request (2.14+):
the agent's structured questions (plan-mode clarifications, spec
gates) arrive as answerable A→C requests with full option metadata
(descriptions, recommended, sub-options) instead of being flattened
into permission prompts — and free-form questions are SURFACED
instead of silently skipped (without the capability KAS advances
past them). Handled by translate/user_input.go; answered via the
user_input_response command.`,
	},
	{
		key:      "backgroundProcesses",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.backgroundProcesses opts into KAS's background-process tools
(list_processes, get_process_output and a start/stop tool) over standard
terminal/create + terminal/output, which marotte already implements
(agent/agent_terminal.go). Without process tools the agent cannot run a dev
server or a watcher without blocking its turn on a foreground command.

From 2.27.0 it is the FALLBACK: the backgroundExecution session-door setting
outranks it and selects the engine's control_process class over the same
client-terminal executor. This key answers only for a session whose resolved
backgroundExecution is false, such as the steps of a run persisted with false
before that row existed, which would otherwise get no process tools.`,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Value-gated, always present: resolveCapabilities compares `=== true`, so
		// the key has to carry a real boolean either way. Gated on the SAME field
		// as the knowledge SETTING row below, because the two are two thirds of one
		// gate and turning one off alone breaks it.
		gate: func(s *Spawn) (any, bool) { return s.Knowledge, true },
		because: `_meta.kiro.knowledge gates getKnowledgeListing into the system prompt,
i.e. it tells the agent WHICH knowledge bases are indexed (four lines
per base, undefined when none). marotte ships the knowledge UI, so the
index exists and /knowledge works — the agent just could not see what
was in it.

GATED since 2026-08, on marotte's own knowledge_enabled setting. The
control used to write kiro-cli's chat.enableKnowledge, which measured as
unable to reach a running chat at all (KAS's ACP path reads no kiro-cli
setting — see the toolSearch row for the counts), so the switch in
Settings → General appeared to turn knowledge off and did nothing. It
now drives this key and the setting below, which are the two KAS reads.

Deliberately NOT declared on the UTILITY bridge, which therefore sends
false. That session is text-only and enforces it, so it can lose no tool;
what it loses is a knowledge listing in msg0 on a cheap-model prompt
whose whole job is a commit message or an error explanation, which was
wasted context. It does not affect the knowledge PANEL: handleKnowledge
consults neither this key nor the setting, so _kiro/knowledge lists and
edits the store whatever the switch says. That split is the intent — the
switch decides what the AGENT can reach, not what a person can.`,
	},
	{
		key:      "secretStorage",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Always present, value from the spawn: a false is a real declaration
		// here, not an omission. See because.
		gate: func(s *Spawn) (any, bool) { return s.SecretStorage, true },
		because: `_meta.kiro.secretStorage opts into KAS's AcpSecretStorage: it then asks
the client to HOLD the MCP OAuth credentials it derives (the DCR result,
the token set, the PKCE verifier) via _kiro/secret/{get,store,delete}.
KAS keeps only an in-process memory copy and has no file of its own, so
without this flag the store is never constructed and every bridge spawn
re-runs discovery and a fresh POST /register — measured, and measured to
stop at zero DCRs once a stored blob is replayed. Answered by
agent/bridge_v3_secret.go against internal/secretstore. Declaring it is a
COMMITMENT: KAS rethrows a client-side store/delete failure into the MCP
connect path, so the handlers must answer on every bridge that declares
it, the utility bridge included.

Which is why it is CONDITIONAL rather than a literal true. The store is
best-effort (no configDir, or a mode internal/secretstore cannot verify as
0600, leaves it nil), and declaring the capability over a nil store made
every MCP OAuth connect fail on the -32603 from secretStoreResult — worse
than not offering it, because undeclared merely costs one DCR per spawn.
The runtime reads its store per spawn (StartOpts.SecretStorage); a false here
means KAS never asks, so the unanswerable request is never made.`,
	},
	{
		key:      "hooks",
		door:     doorConnection,
		resolver: resolverObject,
		send:     true,
		// Presence-gated, not value-gated: when hooks are off the key is absent
		// entirely, which is what the pre-kascap literal did with an if.
		gate: func(s *Spawn) (any, bool) { return hooksValue(), s.Hooks },
		because: `hooks opts into KAS's v2 hook engine. Set on the utility bridge (so
_kiro/hooks/list|setEnabled|triggerHook are available for the
hooks-management dashboard) AND on chat bridges (so the workspace's
user-authored .kiro/hooks/*.json hooks autofire on their triggers
during an agent turn). In v2 mode KAS loads the hook files and runs
runCommand hooks internally — it does not call back the client to run
autofired hooks. See agent/hooks.go and agent/bridge_coord.go.`,
	},
	{
		key:      "codeIntelligence",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.codeIntelligence opts every session into KAS's
native code tool (tree-sitter symbol navigation always; LSP-backed
rename/references/diagnostics once the workspace is initialized —
agent/code_intel.go). This is the client-owned settings channel KAS
reads into clientMeta (the sqlite chat.enableCodeIntelligence
setting does NOT apply to acp mode); lab-verified against 2.13.0.
Costs nothing when unused: with no lsp.json and no servers on
PATH the LSP operations degrade gracefully and tree-sitter still
works.`,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     true,
		// Value-gated on the same field as the capability row above. enabledIf
		// rather than enabled(), because the resolver needs the object shape in
		// both states: isSettingEnabled reads .enabled unchecked, so {"enabled":
		// false} is the off state and an absent member would be undefined.
		gate: func(s *Spawn) (any, bool) { return enabledIf(s.Knowledge), true },
		because: `_meta.kiro.settings.knowledge is the THIRD part of the knowledge
gate, and without it the other two are decoration.
isSettingEnabled(settings, "knowledge") treats an absent key as
false, and that is the sole gate on KAS constructing its Knowledge
TOOL. So before this key: chat.enableKnowledge made the index
exist, _meta.kiro.knowledge told the agent WHAT was indexed, and
no tool existed to read it. marotte shipped the whole knowledge UI,
the REST surface, the progress polling and a system-prompt listing
over a store the agent could not query, silently in both
directions (no error, no -32601).

GATED since 2026-08 on knowledge_enabled, together with the capability
row above — see that row for why the switch moved off
chat.enableKnowledge and why the knowledge PANEL is unaffected. The
member name is the only thing this key shares with the index CONFIG KAS
reads off the same object: ResolvedKnowledgeConfig.resolve reads
indexType, includePatterns, excludePatterns, maxFiles and chunking and
never touches enabled, so sending {"enabled": false} withdraws the tool
without disturbing how the store is built.`,
	},
	{
		key:      "toolSearch",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, because "Load MCP tools on demand" now drives KAS's other
deferral mode through the child environment (the KIRO_FEATURE_TOOL_LOAD_ENABLED
environment row below) and this key is the mode it replaces.
_meta.kiro.settings.toolSearch makes KAS ship tool_search in place of every MCP
tool's description, and each activation ADDS the loaded tools to the request's
tools array (2.26.1 bundle ~11,121,900). Growing the array per load invalidates
the prompt cache and walks a thinking model into a signature mismatch, and a
narrow agent allowlist drops the tool_search loader itself (filterTools, @403,369).
The tool_load arm keeps the array fixed for the session, runs a loaded tool through
tool_call, and its tool pair carries @mcp-disclosure, which the allowlist filter
always re-admits.

Not sent as a fallback beside the env var: with the arm on KAS forces this key off
(Nr = !toolLoad && isSettingEnabled(settings, "toolSearch"), @11,119,827), so it is
inert while the arm exists, and if upstream ever removed the arm a still-sent key
would silently reinstate the array-growing mode. Withheld, the census and the next
release read surface that removal instead.

History worth keeping: the toggle once wrote kiro-cli's own toolSearch.enabled
through /api/kiro-settings, and measured on the stock 2.19.2 bundle that store is
unreachable from KAS's ACP path: zero occurrences of cli.json, kiro-cli/settings,
readSettingsFile and loadCliSettings, and each chat.* literal appearing exactly
once, every one a "@see kiro-cli:" cross-reference inside the settings schema.`,
	},
	{
		key:      "workflows",
		door:     doorSession,
		resolver: resolverSetting,
		// Value-gated, always present: on load KAS takes the client value over
		// the persisted workflowsEnabled, so an explicit false is what turns a
		// resumed chat's workflow tools off.
		gate: func(s *Spawn) (any, bool) { return enabledIf(s.Workflows), true },
		send: true,
		because: `_meta.kiro.settings.workflows gates the agent's workflow TOOLS the
same way: resolveWorkflows resolves an absent key to false, which
removes the whole workflowChatTools array (run_workflow,
inspect_workflow, update_workflow, validate_workflow, send_message)
plus the workflow steering doc. marotte drives the workflow surface
from the CLIENT side (POST /api/runs, GET /api/recipes, the
/docs/workflows tab, a per-run bridge), so the run half worked while
the agent had no way to reach a workflow itself.

It rides the SESSION door, and that is the whole defect this row
records. KAS resolves it per session, not per connection: the only
readers are createNewSessionState, which calls resolveWorkflows(parsed2)
over parseSettings(kiroMeta?.settings) off the session call's own _meta
with NO persisted default, and hydrateSessionForLoad, which passes
persisted.metadata.workflowsEnabled as that default. Nothing on the
connection door reads settings.workflows at all — on 2.18.0 the literal
isSettingEnabled key set is codeIntelligence, goal, inlineAgents,
knowledge, subagentOrchestration, toolSearch and _providerPowers, and
workflows is absent from every isSettingEnabled AND every
isFeatureEnabled call in the bundle (the latter matters because a
connection-door closure bridges initialize's settings onto the feature-flag
provider, so a key read that way WOULD be connection-scoped) — so
declaring it at
initialize resolved absent-to-false on every session and cost the agent
the entire workflow tool array with no error, no log and no -32601.

Sent on session/load as well as session/new, because the persisted
default only carries a value a PREVIOUS create put there: every session
created before this row existed persisted workflowsEnabled false, so a
resumed chat would keep losing the capability a fresh one has. The
client value wins over the persisted one, which is what lets a load
repair such a session.

What makes the session door work at all: KiroSessionMetaSchema declares
no settings field, but it ends in .passthrough(), so the key survives
parseKiroMeta rather than being stripped. That is a property of somebody
else's schema, which is why TestSessionNewCarriesWorkflowsAtSessionDoor
pins the wire and the census pins the version it was read from.

Driven by the "Workflows" setting (workflows_enabled, default on), which
gates what the AGENT can reach: KAS's workflowsGatedOn hides the run tool,
the chat workflow tools and the steering doc, while _kiro/workflow/new reads
no workflowsEnabled, so marotte's own run surfaces work either way. The
utility bridge resolves no setting and sends false.`,
	},
	{
		key:      "backgroundExecution",
		door:     doorSession,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `SENT {"enabled": true} on both session doors, so every session marotte
creates (chat, utility, run bridge) and every workflow step (KAS copies the
parent's resolved value into run state) uses KAS's control_process class: a start
whose turn was cancelled, whose approval went stale or that is now blocked is
refused just before terminal/create, and stopping an untracked id says so. Same
tool ids, schema and terminal/* traffic as the backgroundProcesses class, because
the process manager is chosen separately (terminal:true and no sandbox means
marotte's terminal handlers). KAS reads it per session, setting over persisted
over the experiment registry, so it also pins the class against a ramp. The census
cannot see it: KAS reads it through a variable-key resolver, so this row is its
only record.`,
	},
	{
		key:      "shellType",
		door:     doorSession,
		resolver: resolverCapability,
		value:    "bash",
		send:     true,
		because: `SENT on both session doors so KAS resolves the session's shell from
metadata instead of a _kiro/terminal/shell_type round trip during session
creation. KAS's own workflow step reloads carry no shellType and still probe, so
agent/bridge_v3_hostreq.go keeps answering; its value is marotte.HostShellType and
must equal this one (pinned by a bridge test, since kascap imports nothing).`,
	},
	{
		key:      "goal",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.goal makes KAS's own /goal parser reachable.
Two sites read it, both off the stored initialize block
(isSettingEnabled(this.clientMeta?.settings ?? {}, "goal"), so this is a
CONNECTION-door key even though its sibling workflows is not): the
slash-command source that publishes /goal in available_commands_update,
and the prompt path, where parseGoalCommand turns "/goal <text> [--max
N]" into launchGoal — a bundled repeat workflow that iterates toward the
goal until it self-declares success or the iteration budget runs out.

Worth sending for one reason: without it, typed /goal reaches the MODEL
as prose and the model answers as though it had run something, which is
exactly the lie typed /compact used to tell. With it, the verb either
launches a real run or is not offered.

This row is LOAD-BEARING for a real affordance, not a muscle-memory
fallback: the composer's chat-actions menu has a Set-a-goal row, and it
composes exactly "/goal <text> [--max N]" and sends it through the
ordinary prompt path, because the parser is the only route that can set
the iteration bound. The bundled recipe's repeat node is written
maxIterations: 200 and launchGoal applies the user's bound by mutating
that node on a clone, so launching the recipe by source instead bounds
every goal at 200. Stop sending this key and that row goes back to
reaching the model as prose.

The composer's / menu lists /goal from available_commands_update (the
catalog keeps only kind workflow with commandId goal), so the typed verb
is discoverable there too. Note the loop it starts
is an ordinary workflow run parented on the calling session, so it lands
in the same unsupervised population as an agent-launched run — and its
frames arrive on the calling chat's topic, which is why the row opens no
run tab.`,
	},
	{
		key:      "workspaceTrusted",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `workspaceTrusted is the trust verdict every workspace-scoped read in
KAS is gated on, and marotte sends true because that is what it already
gets: the mount is the user's own repository tree, the container hands the
agent a root shell over it, and nothing about that is safer for the agent
reading the repo's own steering files.

What it gates, measured in 2.18.0: scanNestedAgentsMd returns an empty
map outright when it is false (the only hard gate on the nested AGENTS.md
walk); NodeSteeringDocumentSource and ProgressiveContextManager filter
workspace-scoped docs and items out; executionAllowed(v2) is exactly
this flag, so v2 hooks load but never fire
("hooks.v2.executionDisabledUntrustedWorkspace");
buildUntrustedAutoloadAskRules injects ask rules over the config
directories; MCPConfigManager skips the workspace server files; and the
agent-profile watcher is not started.

Sending it changes NOTHING today, and that is deliberate rather than a
happy accident: this version takes workspaceTrusted as a KiroAgent
CONSTRUCTOR option and BOTH entry points hardcode it to true, so no
client key by this name is read anywhere in the bundle — resolveCapabilities
does not map it and neither clientMeta nor kiroMeta is ever its receiver.
The row's value is therefore the record, not the mechanism: it states
which side of the gate marotte means to be on, in the one place a reader
looks for that, so an upstream release that starts reading a client key
finds marotte's answer already written down instead of inheriting a
default nobody chose. It is also the same gate that widens the
untrusted-repository surface in a filed security report, which is the
reason to want the answer visible as a line a human can flip rather than
implied by silence.

Harmless to send meanwhile: initialize reads _meta.kiro as a plain object
(no schema, no strict()), so an unread key is ignored. If this row ever
needs to be false, the WITHHOLDING is what expresses it — an absent key
reads as false at every one of the sites above.`,
	},
	{
		key:      "subagentOrchestration",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `subagentOrchestration swaps the agent's delegation tool: absent
gives one-shot invoke_sub_agent, present gives
orchestrate_subagent, which wraps the same invoke config and adds
pipeline stages with depends_on and bounded loops. Same
absent-means-false resolver as the two keys above, and kiro-cli's
own TUI sends it, so withholding it diverged marotte's agent from
the reference client's for no stated reason.

The cost this does NOT remove, recorded because it is the reason to
think twice before reaching for a pipeline: a subagent has no
session of its own, so its whole run lands in the PARENT
transcript and every later turn re-bills it: a trivial delegation
costs roughly sixteen times the transcript bytes of the same work
as a workflow step. So this key makes pipelines
expressible, not cheap; real fan-out still belongs in a workflow
run, and A4.2's tool-output cap is what bounds the damage when the
agent chooses otherwise.`,
	},
	{
		key:      "session_title_llm",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD because it is INERT, and it was sent for a year on a
premise measurement refuted. session_title_llm names KAS's LLM session
title: one fire-and-forget call to its fast model (qdev::simple-task) on a
session's FIRST prompt, asking for a 3-to-6-word Title Case name, with a
15s abort budget.

The refutation, off the pinned bundle. The key has exactly TWO sites: a
feature-key-to-env-var map, and the kickoff's gate
"process.env.KIRO_DISABLE_SESSION_TITLE_LLM === 'true' ||
!t.featureConfig.get(SESSION_TITLE_LLM)". So it is read through
featureConfig, NOT through isFeatureEnabled, and buildFeatureConfigRegistry
constructs that registry with exactly two providers — the KAS process
environment and upstream's experiment service. There is no client provider,
so a key in clientCapabilities._meta.kiro.settings can never reach this
gate. This row turned nothing on and could turn nothing off; every reader
of the old because was reading a claim about isFeatureEnabled that does not
apply to this key. Same class as memoryEnable's eligibility term, which
decl.go's Memory field already records as unreachable from the settings
bridge.

So the feature is on because upstream RAMPED the experiment (FEATURES
default false, in-source "Ships dark … until the experiment ramps"), not
because marotte asked, and the only lever marotte holds over the producer
is KIRO_DISABLE_SESSION_TITLE_LLM=true in the child environment — tested
first, so it beats both providers. The two levers over the RESULT are
translate.TitleRefusal at the adoption door and that variable; nothing on
the wire is one.

Kept as a row rather than deleted because a map literal has no line for a
key it omits, and this key is worth a line: it looks like a client setting,
it is spelled like one, and the next reader to find it in the schema will
reach for exactly the row this replaces.

What the feature buys, unchanged and now credited to upstream: GET
/api/sessions reads each row's title straight off _kiro/session/list, which
is KAS's OWN stored title and never marotte's chat name, so a closed chat
whose agent never called update_session_information used to show
deriveSessionTitle's 80-char truncation of the first prompt. It also renames
the TAB, and that is not separable — the title arrives on the shared
focus_update channel, so applyFocusTitle adopts it about a second after the
first prompt. There is no discriminator that would let History have it
alone: KAS emits a title-only focus_update here while the
update_session_information tool usually carries a description or status too,
but the tool's fields are all optional, so filtering on title-only would
drop a genuine agent rename to protect a truncation.

And the cost, which is why the adoption door exists: a reply that passes
titleIsPromptDerived is not thereby a title. Measured on the live volume —
first prompt "test", so KAS asked its fast model to title a one-word
conversation and the model correctly answered by asking for the message; the
reply arrived 1.4s after the derivation and marotte stored it as the chat's
name, where it stayed, because a name only moves UP the precedence.

If upstream ever wires a client provider into featureConfig, this row
becomes live and both spelling constraints apply: the CONNECTION door (the
settings bridge is installed inside initialize, so a session-door row is
never consulted) and NO leading underscore, because rawKeys is the client's
object keys verbatim while the authoritative schema declares the
experimental family with one.`,
	},
	{
		key:      "sessionEviction",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, pending a probe that prices it. sessionEviction is an
opt-in disk budget: resolveSessionEviction reads {enabled, maxBytes} off
the session call's own settings, and when enabled createNewSessionState
fires checkStorageBudget, which calls runSessionEviction to DELETE the
least recently modified sessions until the tree is under budget.

The reason to withhold it is not cost, it is authority. marotte already
owns retention end to end: chat_retention_days drives its own reaper, and
kiro-cli's competing purge is pinned off (cleanup.periodDays=0) for
exactly this reason. Turning this on would install a SECOND retention
authority with a different key (bytes, not age), a different unit of
deletion (a KAS session, not a marotte chat) and no knowledge of the
chain: a chat's acp_session_id plus its prior_acp_session_ids are one
session chain, retention keys on the whole chain, and an LRU that walks
sessions by mtime would happily evict an earlier segment of a LIVE chat's
chain. Nothing in this tier would notice, and the visible symptom would be
a chat whose older turns stopped replaying.

What a probe has to answer before this can flip: whether eviction
respects a session marotte still references, what the default budget is
against a real /config volume, and whether the reaper and the budget can
be expressed as one policy rather than two. Until then the disk is
bounded by the reaper, which is the authority that knows about chains.`,
	},
	{
		key:      "specPlan",
		door:     doorSession,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return specPlanValue(s), true },
		send:     true,
		because: `SENT on both session doors from Spec planning (spec_planning, default
off, matching kiro-cli, the IDE and Crew). resolveSpecPlan reads
{enabled, workflow, skipClarification} per session, and its only effect on a
kiro-cli client is Autonomous mode: the {{#specPlanEnabled}} arms live only in
the four Autonomous planner subagent templates, which then MUST plan through
subagent/create-spec (an explicit-visibility delegate no other mode reaches)
and write .kiro/specs/<feature>/, which the spec tab renders. Every other
mode's prompt and tools are unchanged.

Always present, off included: session/load takes the client value first and
falls back to the persisted specPlanEnabled, so withholding false would keep a
chat created while the setting was on planning through specs forever. KAS's
defaults are workflow quick and skipClarification true; the checkbox maps to
skipClarification false.`,
	},
	{
		key:      "inlineAgents",
		door:     doorConnection,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return enabledIf(s.InlineAgents), true },
		send:     true,
		because: `SENT from "Inline helper agents" (inline_agents_enabled, default off,
matching kiro-cli's yf("inlineAgents","off")). Read once at initialize
(isSettingEnabled on the initialize settings) to add an inlineAgent parameter
to the delegation tool, so the agent can define a one-off helper with a model
and effort it picks instead of only a registered agent. The helper inherits the
parent's permissions and tools, so the permission surface is unchanged. Never in
spec mode, which builds its tools with inline agents off. Value-gated so off is
an explicit pin rather than an absence.`,
	},
	{
		key:      "steeringReminders",
		door:     doorConnection,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return enabledIf(s.SteeringReminders), true },
		send:     true,
		because: `SENT from "Steering reminders" (steering_reminders_enabled, default off,
matching kiro-cli). The live key is the un-underscored one: the schema declares
_steeringReminders, but the reader is Ty("steeringReminders") through the
initialize settings bridge, so the schema spelling is inert. When on, a long
conversation re-injects the steering docs that fit a size budget as one
<steering-reminder> block, skipped when they do not fit. Value-gated so off holds
against a ramp.`,
	},
	{
		key:      "validation",
		door:     doorSession,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return choiceValue(s.WorkValidation) },
		send:     true,
		because: `SENT only when the user has chosen ("Work validation", work_validation:
"on" or "off"); the default sends nothing, so kiro-cli's own resolution decides
(the client value, then the persisted validationSettingEnabled, then the
AB_VALIDATION experiment). When on, KAS registers its bundled validation agent in
the default and vibe subagent sets, a reviewer the agent can call to challenge a
substantial, under-verified change. Both session doors: KAS re-reads and persists
it on load, so a choice reaches a resumed chat. fta (the workflow coder's twin)
stays withheld.`,
	},
	{
		key:      "infraSafetyMonitor",
		door:     doorConnection,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return choiceValue(s.InfraSafetyMonitor) },
		send:     true,
		because: `SENT only when the user has chosen ("AWS CloudFormation safety check",
cloudformation_safety_check: "on" or "off"); the default sends nothing, so the
KIRO_INFRA_SAFETY_MONITOR experiment decides, as in kiro-cli. Read inside
initialize (Object.hasOwn on the settings), and honoured only on a connection
that declares hooks and the infrastructureSafety capability: on a CloudFormation
template write or a cdk deploy/destroy, Kiro's service derives safety properties
and reports a would-violate as a safety status. Monitor mode never blocks.`,
	},
	{
		key:      "steeringSupervisor",
		door:     doorSession,
		resolver: resolverSetting,
		value:    enabledIf(false),
		send:     true,
		because: `SENT as a compiled {"enabled": false} on both session doors: a veto.
vGt reads it with the experiment as fallback (N??K), so an explicit false holds
against AB_STEERING_SUPERVISOR. In 2.27.x the supervisor runs in shadow mode only
(buildPreToolUseGate logs "shadow: non-blocking, verdict not applied"): one fast
model call per allow-listed tool call whose verdict is discarded, so enabling it
buys cost and nothing else. Revisit when verdicts are applied.`,
	},
	{
		key:      "_providerPowers",
		door:     doorSession,
		resolver: resolverSetting,
		gate:     func(s *Spawn) (any, bool) { return enabledIf(s.ToolLoad), true },
		send:     true,
		because: `SENT with its underscore, tied to "Load MCP tools on demand": both defer
tool descriptions until needed. When on, installed Powers' MCP tools are reached
through the kiro_powers tool instead of the direct list. KAS reads it only on
session/load (session/new initialises providerPowersEnabled false), so a chat's
first session keeps the direct list until it resumes; that is upstream's read
site.`,
	},
	{
		key:      "fta",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, matching kiro-cli (no client sends it; default off). fta's prompt
arms live only in the bundled workflow coder agents, so it is the workflow twin of
validation and has no chat behaviour of its own.`,
	},
	{
		key:      "infraSafetyEnforce",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD so only an account-level governance setting can turn it on.
Enforce mode blocks infrastructure writes remotely and asks for an override;
pinning it false would override an organization that requires it.`,
	},
	{
		key:      "terminal",
		door:     doorConnection,
		resolver: resolverSettingObject,
		gate: func(s *Spawn) (any, bool) {
			if s.TerminalCommandTimeoutMs <= 0 {
				return nil, false
			}
			return map[string]any{enabledMember: true, "commandTimeoutMs": s.TerminalCommandTimeoutMs}, true
		},
		send: true,
		because: `SENT when "Shell command timeout" is set (terminal_command_timeout_ms).
initialize stores wGt(settings), which returns commandTimeoutMs only while
enabled is true, and the shell tool uses it as the DEFAULT when the model passes
no timeout of its own (clamped to 1,800,000; unset is 120,000). Same semantics as
the Kiro IDE's kiroAgent.terminalCommandTimeout. A running bridge is updated live
through _kiro/terminal/settings_changed, which is how clearing it reaches KAS
({"enabled": false}).`,
	},
	{
		key:      "specPhaseCheckpoints",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `specPhaseCheckpoints is a capability KAS lifts onto its agentContext at
initialize (specPhaseCheckpoints: capabilities?.specPhaseCheckpoints) and
reads as === true when building the spec-mode prompt, where it selects a
different workflow-selection process and injects a "# Phase Checkpoints"
section.

It is a promise about the CLIENT, not a feature request: declaring it
tells the agent to stop at phase boundaries and expect the client to
carry the user across them. The checkpoint arrives as an ordinary
_kiro/userInput ask, so the interaction dock renders it and the
answer rides user_input_response like any other.

The promise is now kept, which is why this is sent: the spec tab
(static-src/spec-view.ts) renders the document under review and mounts a
dock host of its own, so the ask and the document are one tab apart. The
one contract the flag could not be flipped without: after tasks.md the
checkpoint offers "Run required tasks", "Run required and optional
tasks" and "Not now", and KAS's prompt tells the agent that the CLIENT
carries a Run answer out and then ends the turn — so the page dispatches
its own Run all for both (the second with the not-marked-optional clause
dropped), or the reader picks Run and the turn ends with nothing running.`,
	},
	{
		key:      "requirementsAnalysis",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `requirementsAnalysis is the sibling of specPhaseCheckpoints on the same
lift (requirementsAnalysis: capabilities?.requirementsAnalysis, also read
off clientMeta) and the same === true gate in the spec-mode prompt
builder, where it turns on a requirements-analysis step ahead of the plan
and adds an "Analyze requirements" option to the requirements-phase
checkpoint.

It needs NO client work of its own: the analysis is a tool the agent
calls and a document it rewrites, so what renders for it is the
requirements document the spec tab already shows and the extra option on
a checkpoint card the dock already draws.

Both spec capabilities are sent together for the reason they were
withheld together: they are two thirds of one spec-mode prompt arm, and
neither is a security decision.`,
	},
	{
		key:      "policyPreset",
		door:     doorSession,
		resolver: resolverCapability,
		// Gated on the active security profile. Present only for a NON-EMPTY set, which is the
		// Custom profile's whole implementation: no key makes the permissions files the entire
		// policy. resolvePresetIds treats an empty array as absent anyway.
		gate: func(s *Spawn) (any, bool) { return s.Presets, len(s.Presets) > 0 },
		send: true,
		because: `policyPreset restores the fs_read floor that KAS grants a bundled mode
and denies a custom one, which kiro-cli 2.19.1 turned from a latent asymmetry
into a silent capability loss.

The 2.19.1 mechanism: filterSearchResults now runs every grep_search and
file_search result through evaluateSingleResource({capability:"fs_read"}) and
admits only effect === "allow". mostRestrictive([]) returns an implicit ASK, so
"no matching rule" is a DROP, not a pass. The rule that normally makes that a
non-event is DEFAULT_AGENT_POLICY's fs_read allow ./** — and
resolveAgentPermissions hands that agent-scope policy ONLY to KAS-shipped
profiles: a user- or workspace-authored agent "stays fail-closed and contributes
no agent-scope rules".

marotte is exactly the client that loses. It seeds ZERO Cedar rules by decision,
and its mode pill offers every workspace custom agent as a one-click mode
threaded to StartOpts.Mode. A custom agent that declares a permissions block
REPLACES the default rather than extending it, and custom agents commonly
declare no fs_read rule at all. So without this
row, a chat switched to any custom agent gets zero search results after the pin
bump, and file_search compounds it: hasMoreResults is computed from the
PRE-filter provider count while the FILTERED list is sliced, so the agent is
told an empty result set is "incomplete" and to keep refining a query that can
never return anything. Builtin and bundled modes are unaffected, and a subagent
under a builtin parent is safe (combineResults returns the parent's allow).

Why a preset and not a rule file: read-workspace resolves to
[FS_READ_WORKSPACE_RULE], the same object a builtin mode already gets, so this
grants NOTHING beyond the status quo for the default mode. It is session-scope,
so precedence by restrictiveness means it can never override a user or workspace
deny. And it needs no permissions.yaml, which keeps marotte's seeds-zero-rules
posture intact — the alternative would have been writing a real allow rule to
disk, which is a standing policy decision this row deliberately does not touch.

It rides the SESSION door because resolvePresetIds reads _meta.kiro.policyPreset
off the session call's own _meta, on BOTH session/new and session/load, and the
value is NOT persisted in session metadata — so a key sent only on new dies at
the first resume. withSessionMeta covers both verbs already.

Two traps. validatePresetIds THROWS InvalidParamsError on an unknown id, so an
upstream rename of read-workspace would fail session/new OUTRIGHT rather than
degrade — every chat refusing to start, which is why VALID_PRESET_IDS is worth
watching at a bump. And this key is in neither the isSettingEnabled nor the
isFeatureEnabled population, so the census cannot see it and will not list it in
unclaimed.txt: this row is the only record it exists.`,
	},
	{
		key:      "disableAutoCompaction",
		door:     doorSession,
		resolver: resolverSetting,
		// Always present: KAS persists the resolved value, so a session once
		// opened with true (by another client or an earlier marotte setting)
		// stays off unless every load sends false explicitly.
		gate: func(s *Spawn) (any, bool) { return enabledIf(s.DisableAutoCompaction), true },
		send: true,
		because: `SENT, value-gated, on the session door only. The chat's compaction
policy (auto_compaction_enabled, auto_compact_pct) sets it true when the switch is
off or the point is above 80%, where marotte compacts the chat itself; every other
session sends false.

resolveDisableAutoCompaction reads three inputs, the session call's own _meta, the
initialize _meta and KAS's persisted session metadata, and freezes the answer per
session until the next load. Absence falls through to the persisted value, so
withholding false would leave an earlier true in force: that is why the row is
always present. It stays off the initialize door because KAS-created workflow step
sessions get settings {workflows, memory} only and must resolve false from the
initialize input.

The flag disables more than the 80% summarization: KAS's 95% truncation and its
context-overflow recovery go with it, so an overflow fails the turn with
ContextWindowExceededError (-32000) and internal/command names the Compact button.
In-chat subagents share the parent's session services, so they run without
compaction too; nothing marotte can call compacts a subagent.`,
	},
	{
		key:      "compaction",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD because there is nothing to send TO. KAS declares the object in
its own settings schema — CompactionSettingSchema, with excludePercent and
excludeMessages — validates it, and reads neither member anywhere. Measured on the
stock 2.19.2 bundle: excludePercent has exactly one occurrence, its own
declaration, and the object itself has zero reads in all three of KAS's reader
shapes (isSettingEnabled, isFeatureEnabled, and a .data.<key> resolver).

This row is here because the ABSENCE of a record is what let two Settings controls
survive a capability audit. marotte shipped "Keep last N exchanges" and "Context
buffer (%)" writing kiro-cli's compaction.excludeMessages and
compaction.excludeContextWindowPercent, and the context ring drew its compaction
threshold from the second one — a wedge positioned by a number that governs
nothing. Both fields and the wedge are gone. A row saying "declared upstream with
zero readers, so there is nothing to send and nothing to withhold" is the honest
record, and it keeps unclaimed.txt meaningful by claiming the key.

Five sibling keys are in the same state and share this row's reasoning rather than
getting one each: thinking, tangentMode, checkpoint, _subagent and _delegate are
all declared in KAS's schema with zero readers. chat.enableCheckpoint's marotte
toggle is deleted; chat.enableTodoList's went too, and its todoList key left the
schema at 2.27.0. _subagent's live counterpart is subagentOrchestration, which
this table already sends, so behaviour there is correct today.`,
	},
	{
		key:      "memory",
		door:     doorSession,
		resolver: resolverSettingObject,
		// Always present: an absent key falls back to KAS's own default
		// {read_write, reflection: true}, so Off has to be SENT.
		gate: func(s *Spawn) (any, bool) {
			return map[string]any{"mode": cmp.Or(s.MemoryMode, "disabled"), "reflection": s.MemoryReflection}, true
		},
		send: true,
		because: `SENT on both session doors, from the Memory dropdown (settings.KeyMemoryMode),
always explicit. KAS reads it through U$ (data.memory), not isSettingEnabled, so its
value is {mode, reflection} and not the {enabled} object. mode is disabled, read_only
or read_write; reflection is background learning and only acts under read_write.
KAS's policy builder applies mode AFTER the entitlement gate, so disabled turns
injection, the memory tool, writes and extraction off whatever the experiment says.

Session door, not connection door, because only a SESSION-door preference upgrades
a session persisted with memoryConfigSource "legacy" (every session marotte created
before this row) to "explicit" on load. A legacy session's persisted config is KAS's
default {read_write, reflection: true}, so omitting this key on a load turns memory
fully on for that chat. Once explicit, KAS keeps the session's persisted mode on
every later load; the bridge re-sends the reflection half through the
memoryReflection config option after a load.

An empty mode is sent as disabled, so a spawn that resolves no setting (the utility
bridge) fails closed. Sending any memory field flips the source to explicit, which
is also what makes KAS stop reading userMemoryOptIn.`,
	},
	{
		key:      "userMemoryOptIn",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD since the memory row went out. It was the legacy veto
({"enabled": false} on every session). Any settings.memory field on the session
door makes KAS's memory config source explicit, and an explicit session no longer
reads this key, so it would veto nothing. Sending it would still refuse the
sessionless _kiro/memory/* calls the Memories tab makes: without a sessionId KAS
resolves memory through the legacy path, which reads this key. Kept as a row so
the census keeps it claimed.`,
	},
	{
		key:      "memoryEnable",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, like its sibling userMemoryOptIn but for a different reason:
that key is redundant beside the explicit memory row and would refuse sessionless
CRUD, while this one must not be sent in either direction, because it is read at
two sites and only one of them is about the memory gate.

Site one IS the memory gate: resolveMemoryEnabled takes
isSettingEnabled(settings,"memoryEnable") as an isInsiderChannel substitute "for
older CLI nightly builds". So the settings bridge does reach the gate here, which
an earlier version of this row denied. It reaches the ELIGIBILITY term through
nothing, though: eligibility needs the experiment value to be "all", or "insider"
with this flag, and the internal arm defaults to "disabled" while the external one
defaults to false. Sending it can therefore only turn memory ON, in the one
configuration where AWS has shipped the insider arm.

Site two is the reason to withhold it whatever site one does. The key is ALSO read
through isFeatureEnabled("memoryEnable"), which feeds resolveRemoteToolAllowlist,
and for a kiro-cli client that pushes the remote searchMemories tool into the
allowlist. So sending it hands the model a remote memory-search tool marotte does
not offer, beside the local store the memory row already controls.

Withholding is sufficient at both sites: absent resolves false through
isSettingEnabled and through the bridged isFeatureEnabled provider alike. The memory
preference that matters is the memory row's, and it is a different key.`,
	},
	{
		key:      "streamingShellContent",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
		because: `WITHHELD, and unreachable rather than merely unwanted — the obvious
reason is the wrong one, which is why this row spells it out.

kiro-cli 2.19.1 added _kiro/tools/content_chunk, an A→C notification carrying
live shell output while a command runs, gated on this capability. The guess a
reader makes is that marotte simply has not adopted it yet. The real gate is a
SECOND one the capability does not open: the producer is ExecuteBash's
StreamCoalescer fed by onOutputChunk, subscribed behind
if (input.onOutputChunk && term.onOutputChunk). marotte declares terminal:true
with no sandbox, so KAS builds an ACPTerminal, whose entire method set is
runCommand, readOutputLines, ensureCommandRunsInCwd, close and focus — no
onOutputChunk — and whose runCommand awaits completion, so there is no mid-flight
output on this path at all. Declaring the flag would emit ZERO frames.

That also relocates the trap. The emit path returns, so declaring the capability
REPLACES the mid-flight tool_call_update for kind === "execute" rather than
adding to it — a real hazard, but for a client whose terminals KAS hosts
in-process (DefaultTerminalManager), not for this one.

And marotte already ships the feature, better, because marotte owns the pid:
pipe-rate 4 KiB reads against 32 ms coalescing, an explicit per-chunk UTF-16
offset against arrival order with no sequence number, and a 64 KiB rolling ring
that keeps the TAIL against a hard 256 KiB cap that keeps the head. That last
pair is the one that matters for a long build: the tail is where the error is.
Adopting would deliver a coarser, unordered second copy of bytes already on the
wire.

ANSI is NOT a difference, and an earlier revision of this row said it was.
sanitizeOutput filters only invisible Unicode (tag characters, zero-width and
bidi ranges); ESC is U+001B and is in none of its ranges, and the streaming
redactor only replaces four named env-var VALUES that are unset here. So escapes
reach the client intact either way. The confusion was a name collision with
marotte's own SanitizeOutput, which IS StripANSI composed with SanitizeUnicode —
agent_terminal.go documents having been burned by exactly that, measuring spans=0
with it and spans=2 without.

The ONE condition that reverses this: a sandbox. The sandbox && capabilities.terminal
arm hands back DefaultTerminalManager, whose DefaultTerminal DOES implement
onOutputChunk — at which point marotte's own terminal handlers go silent and the
stream moves to this frame. No sandbox can start in this container because bwrap
is not in the image, so KAS's bubblewrap backend fails isAvailable() and resolves
to its no-op. Enabling one also needs an explicit session/set_config_option or
_kiro/sandbox/applyConfig request with configId "sandbox" and value "enabled";
both reach applySandboxConfigOption, whose absent config defaults to backend
"auto" (bubblewrap on Linux), and marotte sends neither request.

If the premise ever flips, the order is: a content_chunk handler registered and
green, THEN a toolCallId→card join (marotte keys on terminal_id today), and only
THEN this row's send. Never the flag first.`,
	},
	{
		key:        "semanticReview",
		door:       doorConnection,
		resolver:   resolverSetting,
		absentTrue: true,
		send:       false,
		because: `WITHHELD, and withholding it is what turns it ON. semanticReview is the
one settings key KAS does not read through isSettingEnabled: its resolver
is parsed2.data.semanticReview?.enabled ?? persistedDefault ?? true, so an
absent key resolves TRUE where every other settings key resolves false.
marotte therefore gets the semantic-reviewer subagent in autonomous mode
today by saying nothing at all.

This row exists because that was previously unrecordable. A map literal
has no line for a key it omits, so the state marotte relies on read as an
oversight, and the obvious "fix" of adding semanticReview: {enabled: true}
alongside the others would have looked like tightening a gap while
changing nothing. The trap is the opposite direction: sending
{"enabled": false} here is the only way to disable it, and a future row
that sets send:true must carry a value deliberately rather than inherit
the enabled() shape the other settings rows use.`,
	},
	{
		key:      "KIRO_FEATURE_AUTH_EXPIRY_RETRY_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Retries a turn once on AccessDeniedError before anything is emitted (default on).`,
	},
	{
		key:      "KIRO_FEATURE_BACKGROUND_EXECUTION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Selects KAS's newer ControlProcess tool class; the backgroundExecution session-door
row already pins it, and a session setting outranks this arm.`,
	},
	{
		key:      "KIRO_FEATURE_CGS_DELEGATION_V2_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Subagent delegation v2; no marotte surface depends on which version runs.`,
	},
	{
		key:      "KIRO_FEATURE_KIRO_INFRA_SAFETY_MONITOR_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The Infrastructure-Safety monitor arm. The infraSafetyMonitor settings row
carries the user's choice when there is one; the env arm stays with the
experiment, which is what decides when the setting is left at kiro-cli's default.`,
	},
	{
		key:      "KIRO_FEATURE_KUTS_TELEMETRY_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Picks which endpoint set KAS's standalone telemetry exports to (default on).
Whether telemetry is sent is telemetry.enabled, which the Data sharing control owns.`,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The external memory eligibility arm. marotte used to write it from the memory
setting; the session-door memory row's explicit mode replaces that, and mode
"disabled" holds whatever the arm says.`,
	},
	{
		key:      "KIRO_FEATURE_SESSION_TITLE_LLM_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The LLM session title on chat bridges. Run bridges turn the title off through
KIRO_DISABLE_SESSION_TITLE_LLM instead (row below).`,
	},
	{
		key:      "KIRO_FEATURE_STALL_TOOL_CONTINUATION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Continues a turn stalled after a tool result (default on).`,
	},
	{
		key:      "KIRO_FEATURE_STEERING_SUPERVISOR_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The steering supervisor; the steeringSupervisor settings row vetoes it on the
session door, which the client value does without pinning the env arm.`,
	},
	{
		key:      "KIRO_FEATURE_STREAM_IDLE_WATCHDOG_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The 60s/300s stream-idle watchdog and its _kiro/system/notify text (default on).`,
	},
	{
		key:      "KIRO_FEATURE_UNIFIED_AGENT_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Derived by the bundle's experiment-gate helper from settingKey unifiedAgent; also
un-gates the bundled default-v2 mode.`,
	},
	{
		key:      "KIRO_FEATURE_USER_AGENT_REFACTORING_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Backend user-agent shape; not pinned by decision.`,
	},
	{
		key:      "KIRO_FEATURE_VALIDATION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
The bundled validation reviewer; the validation settings row carries the user's
choice when there is one, and otherwise this arm's experiment decides.`,
	},
	{
		key:      "KIRO_DISABLE_EXPERIMENT_CONFIG",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
Turning off the experiment service would stop every ramp at once; not pinned by
decision.`,
	},
	{
		key:      "KIRO_DISABLE_RECAP",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
		because: `Withheld so the arm follows AWS's experiment ramp, as it does in kiro-cli's own TUI
(decision: pin only where a marotte setting owns the behaviour).
marotte does not enable sessionRecap, so there is no recap to disable.`,
	},
	{
		key:      "KIRO_FEATURE_TOOL_LOAD_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		gate:     func(s *Spawn) (any, bool) { return strconv.FormatBool(s.ToolLoad), true },
		send:     true,
		because: `SENT in both states from "Load MCP tools on demand" (tool_search_enabled,
default off, Crew c2). The env provider outranks the experiment, so an explicit
"false" holds the arm shut against a server-side ramp. With it on, KAS keeps the
tools array fixed (tool_load + tool_call) and forces settings.toolSearch off,
which is why the toolSearch settings row is withheld.`,
	},
	{
		key:      "KIRO_DISABLE_SESSION_TITLE_LLM",
		door:     doorEnvironment,
		resolver: resolverEnv,
		gate: func(s *Spawn) (any, bool) {
			if !s.DisableSessionTitles {
				return nil, false
			}
			return "true", true
		},
		send: true,
		because: `SENT on RUN bridges only (Spawn.DisableSessionTitles). Nothing renders a
workflow step session's title, so the fast-model title call is spend with no
reader. KAS tests this variable before the feature registry, so it beats the
ramped experiment. Chat bridges keep the title: it names History rows.`,
	},
}
