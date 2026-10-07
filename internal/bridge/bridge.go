// Package bridge manages a single kiro-cli ACP subprocess.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/marotte/internal/version"
)

// scannerLineCap is the per-frame cap on the bridge's stdout: a full fsWriteCap (4 MiB) payload plus worst-case
// escaping. A larger frame is drained and dropped (bridge_frame.go).
const scannerLineCap = 16 << 20

// stdoutBufSize is the ReadSlice window, not the frame cap: a larger frame is assembled across ErrBufferFull reads.
const stdoutBufSize = 64 * 1024

// stderrLineCap bounds one forwarded kiro-cli stderr line; a longer one is marked truncated and drained through its
// newline so later diagnostics still reach the log.
const stderrLineCap = 64 * 1024

// errBridgeExited aliases the exported sentinel Call returns when readLoop's post-exit drain unblocks a waiter. An
// alias, so errors.Is holds and the retry loop does not spin on a dead bridge.
var errBridgeExited = marotte.ErrBridgeExited

// errBridgeNotStarted aliases the exported sentinel a write returns with no stdin handle; an alias so errors.Is
// holds at the call sites.
var errBridgeNotStarted = marotte.ErrBridgeNotStarted

// ACP RPC method names, re-exported for package-local use; marotte/methods.go is canonical.
const (
	methodInitialize  = marotte.MethodInitialize
	methodSessionNew  = marotte.MethodSessionNew
	methodSessionLoad = marotte.MethodSessionLoad
	methodSetMode     = marotte.MethodSetMode
)

// metaKeyKiro is the vendor namespace inside an ACP `_meta` object. KAS ignores, rather than rejects, a block one
// level off.
const metaKeyKiro = "kiro"

// The per-session keys marotte adds inside _meta.kiro beside kascap's: the composer choices on session/new
// (`modelId`, `effortLevel`, `modeId`) and the chat's client steering on both verbs.
const (
	metaKeyModelID     = "modelId"
	metaKeyEffortLevel = "effortLevel"
	metaKeyModeID      = "modeId"
	metaKeySteering    = "steering"
)

// session/set_config_option param keys, shared by three call sites.
const (
	keyConfigID    = "configId"
	keyConfigValue = "value"
)

// AgentKiroCapabilities is the backend capability advertisement from initialize. Raw keeps keys not modelled yet.
type AgentKiroCapabilities struct {
	Raw              map[string]json.RawMessage
	Logging          KASLogging
	ExtensionMethods []string
	ReplayMarking    bool
}

// KASLogging is initialize's logging block. Only the log directory is read; Raw keeps the rest.
type KASLogging struct {
	LogDir string `json:"logDir"`
}

// stdinPipe wraps the subprocess's write end so Bridge.stdin is one atomically published word.
type stdinPipe struct{ w io.WriteCloser }

// Bridge is one kiro-cli ACP subprocess tied to one chat.
type Bridge struct {
	// lifecycleCtx bounds the subprocess: StartOpts.Lifetime's receiving half, set by Start, which refuses nil. Never a
	// request or turn context.
	lifecycleCtx context.Context
	// stdin is the subprocess's write end, published atomically: startProcess assigns it, writeFrame writes, Stop and
	// cmd.Cancel close it, on different goroutines. Not writeMu: writeFrame holds that up to writeDeadline (30 s), and
	// shutdown stops every bridge before inflight.Wait(). The wrapper keeps the load to a single word.
	stdin   atomic.Pointer[stdinPipe]
	modes   atomic.Pointer[[]marotte.SessionMode]
	stdout  *frameReader
	pending map[int64]chan pendingReply
	notifCh chan marotte.Notification
	done    chan struct{}
	// catalog is the unfiltered advertised set: Models and ApplyServedModels derive from it, so a deprecated model the
	// account holds still counts for entitlement.
	catalog   atomic.Pointer[[]marotte.SessionModel]
	agentKiro atomic.Pointer[AgentKiroCapabilities]
	cmd       *exec.Cmd
	// envAllow re-permits names the credential screen would drop (bridge_env.go).
	envAllow map[string]struct{}
	// contentCollection is StartOpts.ContentCollection, immutable after Start. contentCollectionMu spans a resolve and
	// its send, so KAS always ends on the newest resolution.
	contentCollection func(context.Context) bool
	cliPath           string
	modelID           marotte.ModelID
	workDir           string
	sessionID         marotte.SessionID
	currentMode       string
	sessionTitle      string
	// effortLevel is the tier the session last reported (the `effortLevel` option's currentValue), not the requested
	// one, so applyInitialEffort repairs. Empty means unknown and must assert. ObserveEffort feeds it too.
	effortLevel string
	// thinking is the session's last reported thinking value ("on", "off", or "" when untoggleable or unreported);
	// SetModel clears it.
	thinking string
	// extraArgs are this spawn's filtered operator launch flags (StartOpts.ExtraArgs). Immutable after Start.
	extraArgs []string
	// extraEnv is appended to the kiro-cli process's environment so the active install directory leads PATH and
	// `kiro-cli` resolves within the same verified install. Empty leaves the screened inherited env.
	extraEnv []string
	// presets are the KAS policy-preset ids this session opens with, immutable after Start. KAS does not persist them,
	// so both session doors must send the same set or a resumed chat silently changes posture.
	presets []string
	// memory is the session-door memory preference; its reflection half is re-asserted after session/load. Immutable
	// after Start.
	memory marotte.MemoryPreference
	// features are the Agent-capabilities settings this spawn sends; immutable after Start.
	features marotte.AgentFeatures
	// deliveredSeq counts notifications readLoop pushed onto notifCh and stamps each. readLoop's goroutine only.
	deliveredSeq uint64
	// loadSeq is the read-loop position the session/load response arrived at (SessionLoadSeq). Guarded by b.mu: written
	// by the loading goroutine, read wherever a decision is ordered against the replay.
	loadSeq uint64
	// summarizationPct is the summarization threshold the session reported; 0 until a session/load result carries one.
	summarizationPct float64
	nextID           atomic.Int64
	stopOnce         sync.Once
	mu               sync.Mutex
	writeMu          sync.Mutex
	pendingMu        sync.Mutex
	// readMu decides whether a read loop or Stop closes notifCh (claimNotifClose).
	readMu              sync.Mutex
	contentCollectionMu sync.Mutex
	notifClaimed        bool
	enableHooks         bool
	// secretStorage gates the `_meta.kiro.secretStorage` declaration in initialize.
	secretStorage bool
	// toolSearch is "Load MCP tools on demand", driving kascap's KIRO_FEATURE_TOOL_LOAD_ENABLED row. knowledge gates the
	// two knowledge rows. Both immutable after Start: KAS freezes them at session creation.
	toolSearch bool
	knowledge  bool
	// disableTelemetry drives kascap's telemetryEnabled row; immutable after Start.
	disableTelemetry bool
	// supervised records that the session accepted `autopilot: off`, not that the chat asked; the request lives on the
	// chat record. False covers refused and unasked, so the coordinator reads it with the chat's request.
	supervised bool
	// disableSessionTitles writes KIRO_DISABLE_SESSION_TITLE_LLM=true (run bridges).
	disableSessionTitles bool
	// disableAutoCompaction is the value this bridge sent on the session door, immutable after Start. KAS froze it, so
	// it, not the current setting, says whether KAS still compacts this chat.
	disableAutoCompaction bool
	// sessionTitleSetByUser is the session result's titleSetByUser latch.
	sessionTitleSetByUser bool
}

// AutoCompactionDisabled reports whether this bridge's session opened with KAS compaction off.
func (b *Bridge) AutoCompactionDisabled() bool { return b.disableAutoCompaction }

// Option configures a Bridge at construction time.
type Option func(*Bridge)

// WithEnv appends environment variables to the kiro-cli process, to put the active install directory first on PATH.
func WithEnv(env []string) Option {
	return func(b *Bridge) { b.extraEnv = env }
}

// WithEnvAllow re-permits credential-shaped names the inherit screen would drop (ParseEnvAllowlist, EnvAllowVar).
func WithEnvAllow(allowed map[string]struct{}) Option {
	return func(b *Bridge) { b.envAllow = allowed }
}

// New returns a bridge that runs the kiro-cli binary at cliPath; call Start first. The caller resolves cliPath per
// bridge, so a version switch reaches the next chat.
func New(cliPath, workDir string, opts ...Option) *Bridge {
	b := &Bridge{
		cliPath: cliPath,
		workDir: workDir,
		pending: make(map[int64]chan pendingReply),
		notifCh: make(chan marotte.Notification, 256),
		done:    make(chan struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// SessionID returns the bridge's ACP session id. Safe from any goroutine.
func (b *Bridge) SessionID() marotte.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

// SessionLoadSeq returns the read-loop position of the session/load response, which a consumer must have folded to
// before treating the replay as complete. Only session/load sets it; 0 is also a legal position, so pair it with
// the load having returned.
func (b *Bridge) SessionLoadSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadSeq
}

// ModelID returns the currently-selected model id. Safe from any goroutine.
func (b *Bridge) ModelID() marotte.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelID
}

// CurrentMode returns the currently-selected session mode. Safe from any goroutine.
func (b *Bridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// SupervisedApplied reports whether this bridge's session accepted `autopilot: off`. Whether the chat asked lives on
// the chat record, and a caller must read both to report a refused assert
// (BridgeCoordinator.reportSupervisedNotApplied).
func (b *Bridge) SupervisedApplied() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.supervised
}

// SessionTitle returns KAS's title for the live session (flat `_meta.title`): "New Session" on creation, the stored
// title on load. Not authoritative: the caller adopts it only for a default-named chat, and focus_update outranks it.
func (b *Bridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

// SessionTitleSetByUser reports whether KAS holds the title as the user's (flat `_meta.titleSetByUser`).
func (b *Bridge) SessionTitleSetByUser() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitleSetByUser
}

// SummarizationThreshold returns the percentage at which the session summarizes its context, from session/load's
// flat `_meta.contextUsage`; 0 when none carried it.
func (b *Bridge) SummarizationThreshold() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.summarizationPct
}

// AgentKiroCapabilities returns the backend's initialize advertisement.
func (b *Bridge) AgentKiroCapabilities() AgentKiroCapabilities {
	p := b.agentKiro.Load()
	if p == nil {
		return AgentKiroCapabilities{}
	}
	raw := make(map[string]json.RawMessage, len(p.Raw))
	for key, value := range p.Raw {
		raw[key] = bytes.Clone(value)
	}
	return AgentKiroCapabilities{
		Raw:              raw,
		Logging:          p.Logging,
		ExtensionMethods: slices.Clone(p.ExtensionMethods),
		ReplayMarking:    p.ReplayMarking,
	}
}

// KASLogDir returns the log directory the backend advertised at initialize, or "".
func (b *Bridge) KASLogDir() string {
	if p := b.agentKiro.Load(); p != nil {
		return p.Logging.LogDir
	}
	return ""
}

// Modes returns the session modes from session/new or session/load. Frozen; callers must not mutate it.
func (b *Bridge) Modes() []marotte.SessionMode {
	if p := b.modes.Load(); p != nil {
		return *p
	}
	return nil
}

// Catalog returns the session result's unfiltered catalog. Frozen; callers must not mutate it.
func (b *Bridge) Catalog() []marotte.SessionModel {
	if p := b.catalog.Load(); p != nil {
		return *p
	}
	return nil
}

// Models returns Catalog without [Deprecated] / [Legacy] entries (modeltext.Hidden).
func (b *Bridge) Models() []marotte.SessionModel {
	catalog := b.Catalog()
	if catalog == nil {
		return nil
	}
	models := make([]marotte.SessionModel, 0, len(catalog))
	for _, model := range catalog {
		if !modeltext.Hidden(model.Description) {
			models = append(models, model)
		}
	}
	return models
}

// NotifCh returns incoming ACP notifications, each carrying the read loop's sequence.
func (b *Bridge) NotifCh() <-chan marotte.Notification { return b.notifCh }

// SetModel swaps the model in-session via session/set_config_option (configId "model"), v3's replacement for
// session/set_model. On failure the model id is unchanged.
func (b *Bridge) SetModel(ctx context.Context, modelID string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	_, err := b.Call(ctx, marotte.MethodSetConfigOption, map[string]any{
		marotte.KeySessionID: sessionID,
		keyConfigID:          marotte.ConfigOptionModel,
		keyConfigValue:       modelID,
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.modelID = marotte.ModelID(modelID)
	// A swap can reset effort invisibly: KAS reconciles against the new model's tiers (2.19.1: a swap to `auto` drops
	// it). Clearing makes the next call assert.
	b.effortLevel = ""
	b.thinking = ""
	b.mu.Unlock()
	return nil
}

// EnsureEffort makes the live session run at level via session/set_config_option (configId "effortLevel"), the one
// spelling of that call. It sends only when the reported level differs (SetModel clears the cache), drops an invalid
// level, and caches the reply: KAS silently ignores a level the model lacks.
func (b *Bridge) EnsureEffort(ctx context.Context, level string) error {
	if level == "" || !marotte.EffortLevel(level).Valid() {
		return nil
	}
	b.mu.Lock()
	sessionID := b.sessionID
	current := b.effortLevel
	b.mu.Unlock()
	if level == current {
		return nil
	}
	resp, err := b.Call(ctx, marotte.MethodSetConfigOption, map[string]any{
		marotte.KeySessionID: sessionID,
		keyConfigID:          marotte.ConfigOptionEffort,
		keyConfigValue:       level,
	})
	if err != nil {
		return err
	}
	// Cache what the session reports: on 2.19.1 a level set on `auto` returns ok with no effortLevel option.
	var out struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	if resp != nil && len(resp.Result) > 0 && json.Unmarshal(resp.Result, &out) == nil {
		b.mu.Lock()
		b.applyEffortConfigOptionLocked(out.ConfigOptions)
		b.mu.Unlock()
	}
	return nil
}

// EnsureThinking makes the session's thinking match choice ("on" or "off") via set_config_option (configId
// "thinking"). A string: KAS ignores a boolean. Sent only when the session reported a different value; unreported
// means untoggleable or unknown. The reply refreshes thinking and effort, since thinking off caps a high tier.
func (b *Bridge) EnsureThinking(ctx context.Context, choice string) error {
	if choice != marotte.ThinkingOn && choice != marotte.ThinkingOff {
		return nil
	}
	b.mu.Lock()
	sessionID := b.sessionID
	current := b.thinking
	b.mu.Unlock()
	if current == "" || current == choice {
		return nil
	}
	resp, err := b.Call(ctx, marotte.MethodSetConfigOption, map[string]any{
		marotte.KeySessionID: sessionID,
		keyConfigID:          marotte.ConfigOptionThinking,
		keyConfigValue:       choice,
	})
	if err != nil {
		return err
	}
	var out struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	b.mu.Lock()
	b.thinking = choice
	if resp != nil && len(resp.Result) > 0 && json.Unmarshal(resp.Result, &out) == nil {
		b.applyThinkingConfigOptionLocked(out.ConfigOptions)
		b.applyEffortConfigOptionLocked(out.ConfigOptions)
	}
	b.mu.Unlock()
	return nil
}

// ObserveThinking records a thinking value from the config_option_update channel this bridge forwards unread.
// Empty is ignored.
func (b *Bridge) ObserveThinking(value string) {
	if value != marotte.ThinkingOn && value != marotte.ThinkingOff {
		return
	}
	b.mu.Lock()
	b.thinking = value
	b.mu.Unlock()
}

// ObserveEffort records a level the session reported on config_option_update, which this bridge forwards unread.
// KAS moves the level itself (first-prompt pin, an IDE or TUI swap), and a stale cache would skip the repair. Empty
// is ignored.
func (b *Bridge) ObserveEffort(level string) {
	if level == "" {
		return
	}
	b.mu.Lock()
	b.effortLevel = level
	b.mu.Unlock()
}

// spawn packages this spawn's facts for kascap's gated rows, which gate by presence, value and emptiness. Immutable
// after Start, so read without the mutex; one method because both doors must describe the same spawn.
func (b *Bridge) spawn() *kascap.Spawn {
	return &kascap.Spawn{
		SecretStorage:            b.secretStorage,
		Hooks:                    b.enableHooks,
		Presets:                  b.presets,
		Knowledge:                b.knowledge,
		MemoryMode:               b.memory.Mode,
		MemoryReflection:         b.memory.Reflection,
		ToolLoad:                 b.toolSearch,
		DisableSessionTitles:     b.disableSessionTitles,
		DisableAutoCompaction:    b.disableAutoCompaction,
		SpecPlan:                 b.features.SpecPlan,
		SpecAskClarification:     b.features.SpecAskClarification,
		WorkValidation:           b.features.WorkValidation,
		InfraSafetyMonitor:       b.features.InfraSafetyMonitor,
		TerminalCommandTimeoutMs: b.features.TerminalCommandTimeoutMs,
		InlineAgents:             b.features.InlineAgents,
		SteeringReminders:        b.features.SteeringReminders,
		Workflows:                b.features.Workflows,
		DisableTelemetry:         b.disableTelemetry,
	}
}

func decodeAgentKiroCapabilities(result json.RawMessage) (AgentKiroCapabilities, error) {
	var envelope struct {
		AgentCapabilities struct {
			Meta struct {
				Kiro json.RawMessage `json:"kiro"`
			} `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return AgentKiroCapabilities{}, err
	}
	kiro := envelope.AgentCapabilities.Meta.Kiro
	if len(kiro) == 0 || string(kiro) == "null" {
		return AgentKiroCapabilities{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(kiro, &raw); err != nil {
		return AgentKiroCapabilities{}, err
	}
	var typed struct {
		ExtensionMethods []string `json:"extensionMethods"`
		ReplayMarking    bool     `json:"replayMarking"`
	}
	if err := json.Unmarshal(kiro, &typed); err != nil {
		return AgentKiroCapabilities{}, err
	}
	// Diagnostic only: a malformed block must not fail the handshake.
	var logging KASLogging
	if block, ok := raw["logging"]; ok && json.Unmarshal(block, &logging) != nil {
		logging = KASLogging{}
	}
	return AgentKiroCapabilities{
		Raw:              raw,
		Logging:          logging,
		ExtensionMethods: typed.ExtensionMethods,
		ReplayMarking:    typed.ReplayMarking,
	}, nil
}

func (b *Bridge) initialize(ctx context.Context) error {
	initStart := time.Now()
	// Declared in internal/kascap. This is the connection door; withSessionMeta builds the session door from the same
	// table, and each key goes where KAS reads it.
	kiroMeta := kascap.Capabilities(b.spawn())

	// fs and terminal route file access and command execution through marotte. elicitation makes kiro-cli forward an
	// MCP server's elicitation/create; without it the tool call stalls.
	resp, err := b.Call(ctx, methodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
				// Claiming KAS's stat, readDirectory and delete confines rather than grants: otherwise KAS uses its own filesystem
				// with no marotte path check. readFile/writeFile stay absent: claiming writeFile brings range edits in UTF-16 offsets
				// marotte would have to splice.
				"_meta": map[string]any{metaKeyKiro: map[string]any{
					"stat":          true,
					"readDirectory": true,
					"delete":        true,
				}},
			},
			// terminal routes agent shell commands through marotte's terminal/* handlers, which own the pid, argv and output.
			// Registering a client tool in KAS's CORE_IO_TOOL_IDS flips hasClientIOTools and silently unbounds ExecuteBash.
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
			"_meta":       map[string]any{metaKeyKiro: kiroMeta},
		},
		"clientInfo": map[string]any{
			"name": "marotte", "title": "Marotte for Kiro", "version": version.Build,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	caps, err := decodeAgentKiroCapabilities(resp.Result)
	if err != nil {
		return fmt.Errorf("initialize result: %w", err)
	}
	b.agentKiro.Store(&caps)
	// Start()'s "bridge started" is the authoritative line; elapsed_ms isolates the initialize round trip.
	slog.Debug("ACP initialize RPC completed",
		"version", version.Build,
		"elapsed_ms", time.Since(initStart).Milliseconds(),
		"extension_methods", len(caps.ExtensionMethods),
		"replay_marking", caps.ReplayMarking,
	)
	return nil
}
