// Package bridge runs the one kiro-cli ACP engine process and the ACP sessions on it. The Engine owns the process,
// its pipe, its read loop and its write lock; a Session is one ACP session's handle and has no way to stop the
// process. Frames reach per-consumer lanes: a session's own by sessionId, the rest through a Router, else the engine
// lane.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/version"
)

// scannerLineCap is the per-frame cap on the engine's stdout: a full fsWriteCap (4 MiB) payload plus worst-case
// escaping. A larger frame is drained and dropped (bridge_frame.go).
const scannerLineCap = 16 << 20

// stdoutBufSize is the ReadSlice window, not the frame cap: a larger frame is assembled across ErrBufferFull reads.
const stdoutBufSize = 64 * 1024

// stderrLineCap bounds one forwarded kiro-cli stderr line; a longer one is marked truncated and drained through its
// newline so later diagnostics still reach the log.
const stderrLineCap = 64 * 1024

// Aliases, so errors.Is holds at the call sites.
var (
	errBridgeExited     = marotte.ErrBridgeExited
	errBridgeNotStarted = marotte.ErrBridgeNotStarted
)

// ACP RPC method names, re-exported for package-local use; marotte/methods.go is canonical.
const (
	methodInitialize   = marotte.MethodInitialize
	methodSessionNew   = marotte.MethodSessionNew
	methodSessionLoad  = marotte.MethodSessionLoad
	methodSessionClose = marotte.MethodSessionClose
)

// KAS ignores, rather than rejects, a block one level off.
const metaKeyKiro = "kiro"

// agentKiroCapabilities is the part of the backend's initialize advertisement marotte reads.
type agentKiroCapabilities struct {
	Logging          kasLogging
	ExtensionMethods []string
	ReplayMarking    bool
}

// kasLogging is initialize's logging block. Only the log directory is read.
type kasLogging struct {
	LogDir string `json:"logDir"`
}

// stdinPipe wraps the process's write end so Engine.stdin is one atomically published word.
type stdinPipe struct{ w io.WriteCloser }

// exitReason is the engine-scoped evidence that ended an engine. Only these end it; a session's errors never do.
type exitReason string

const (
	// ExitEnded means stdout reached EOF without marotte asking, so the process ended itself.
	ExitEnded exitReason = "ended"
	// exitReadError means reading stdout failed.
	exitReadError exitReason = "read_error"
	// ExitStdinStalled means a write chunk made no progress within writeStall.
	ExitStdinStalled exitReason = "stdin_stalled"
	// exitWriteFailed means a write to stdin failed or was short, so the frames after it cannot be read.
	exitWriteFailed exitReason = "write_failed"
	// ExitParseBreaker means too many consecutive frames failed to parse.
	ExitParseBreaker exitReason = "parse_breaker"
	// exitDrainExhausted means one frame never terminated within the drain budget.
	exitDrainExhausted exitReason = "drain_exhausted"
	// ExitLaneOverflow means the engine lane's notification backlog reached its hard cap.
	ExitLaneOverflow exitReason = "lane_overflow"
	// ExitStopped means marotte called Stop.
	ExitStopped exitReason = "stopped"
)

// ExitInfo describes an engine's end. OnExit receives it before the process is reaped, so SidecarExit is filled in
// only by Exit, after Done.
type ExitInfo struct {
	Reason exitReason
	// SidecarExit is the kiro-cli-chat process's own status ("exit_code=N" or "signal=NAME"); its node child's status
	// never reaches marotte, so it names no cause.
	SidecarExit string
	Uptime      time.Duration
	// JSHeapOOM: the engine's stderr carried Node's "JavaScript heap out of memory" line.
	JSHeapOOM bool
	// RelayWriteTimeout: the engine's stderr carried the relay's 2-second write timeout line.
	RelayWriteTimeout bool
}

// EndedItself reports whether the engine ended without marotte stopping it.
func (i *ExitInfo) EndedItself() bool { return i.Reason != ExitStopped }

// DetachedSession is a session lane the engine cut loose because its consumer stopped reading: Requests are the
// requests it held, oldest first, each still owed exactly one answer; DroppedNotifications counts what was dropped.
type DetachedSession struct {
	SessionID            marotte.SessionID
	Requests             []*marotte.RPCResponse
	DroppedNotifications int
}

// Engine is the one kiro-cli ACP process. Safe for concurrent use; every write serializes on writeMu.
type Engine struct {
	startedAt time.Time
	// lifecycleCtx bounds the process: EngineOpts.Lifetime, required by Start. Never a request or turn context.
	lifecycleCtx context.Context
	router       Router
	cmd          *exec.Cmd
	pending      map[int64]*pendingEntry
	reapSession  func(marotte.SessionID)
	// contentCollection is EngineOpts.ContentCollection, immutable after Start. contentCollectionMu spans a resolve and
	// its send, so KAS always ends on the newest resolution.
	contentCollection func(context.Context) (enabled, readable bool)
	// stdin is the process's write end, published atomically: startProcess assigns it, writeFrame writes, teardown and
	// cmd.Cancel close it, on different goroutines.
	stdin      atomic.Pointer[stdinPipe]
	agentKiro  atomic.Pointer[agentKiroCapabilities]
	stdout     *frameReader
	onDetach   func(DetachedSession)
	engineLane *lane
	background func(func()) bool
	sessions   map[marotte.SessionID]*Session
	// releasing holds an id whose abandoned load is being closed with no handle: a door for that id waits for the
	// close to settle, so the close never lands on the newer session. Guarded by lanesMu.
	releasing map[marotte.SessionID]chan struct{}
	// envAllow re-permits names the credential screen would drop (bridge_env.go).
	envAllow map[string]struct{}
	// done closes once the engine has ended: lanes closed, pending failed, process reaped. stopping closes at the
	// first teardown; exited closes when the exit path finished.
	done        chan struct{}
	stopping    chan struct{}
	exited      chan struct{}
	onExit      func(ExitInfo)
	sidecarExit string
	// exitReason is the first engine-scoped evidence recorded. Guarded by mu.
	exitReason exitReason
	workDir    string
	cliPath    string
	extraArgs  []string
	authArgs   []string
	// extraEnv is appended to the engine's environment so the active install directory leads PATH. Empty leaves the
	// screened inherited env.
	extraEnv []string
	// startIgnoreFiles and startTerminalTimeoutMs are what Start left KAS holding (StartLive). Guarded by mu.
	startIgnoreFiles []string
	// engineGroups are the process groups below kiro-cli's own (noteEngineGroups). Guarded by mu.
	engineGroups []procgroup.Group
	spawn        kascap.EngineSpawn
	// startTerminalTimeoutMs: see startIgnoreFiles.
	startTerminalTimeoutMs int
	nextID                 atomic.Int64
	heapMiB                int
	generation             uint64
	stopOnce               sync.Once
	exitOnce               sync.Once
	lanesMu                sync.Mutex
	writeMu                sync.Mutex
	pendingMu              sync.Mutex
	mu                     sync.Mutex
	contentCollectionMu    sync.Mutex
	// readerExited is the exit path's fence, taking no lock: once set, writeFrame (which loads it under writeMu)
	// writes nothing.
	readerExited      atomic.Bool
	readLoopStarted   atomic.Bool
	jsHeapOOM         atomic.Bool
	relayWriteTimeout atomic.Bool
	// contentCollectionApplied is the value KAS last confirmed; contentCollectionKnown is false until an assert lands
	// and again after one fails, since a failed send may or may not have reached KAS. Guarded by mu.
	contentCollectionApplied bool
	contentCollectionKnown   bool
}

// Option configures an Engine at construction time.
type Option func(*Engine)

// WithEnv appends environment variables to the engine's process, to put the active install directory first on PATH.
func WithEnv(env []string) Option { return func(e *Engine) { e.extraEnv = env } }

// WithEnvAllow re-permits credential-shaped names the inherit screen would drop (ParseEnvAllowlist, EnvAllowVar).
func WithEnvAllow(allowed map[string]struct{}) Option {
	return func(e *Engine) { e.envAllow = allowed }
}

// WithRouter places the frames no session lane claims. Without one they all reach the engine lane.
func WithRouter(r Router) Option { return func(e *Engine) { e.router = r } }

// WithExitHook runs fn synchronously in the exit path, before any pending request fails or any lane closes, so a
// snapshot of what was running sees nothing the end changed. fn must not block.
func WithExitHook(fn func(ExitInfo)) Option { return func(e *Engine) { e.onExit = fn } }

// WithDetachHandler receives each session lane cut loose for overflow; the handler answers its requests once and
// closes the session.
func WithDetachHandler(fn func(DetachedSession)) Option { return func(e *Engine) { e.onDetach = fn } }

// WithGeneration labels this engine's log lines with the owner's spawn count.
func WithGeneration(gen uint64) Option { return func(e *Engine) { e.generation = gen } }

// NewEngine returns an engine that runs the kiro-cli-chat sidecar at cliPath; call Start next. Its engine lane exists
// from here on, so a consumer can attach before Start writes initialize.
func NewEngine(cliPath, workDir string, opts ...Option) *Engine {
	e := &Engine{
		cliPath:    cliPath,
		workDir:    workDir,
		pending:    make(map[int64]*pendingEntry),
		sessions:   make(map[marotte.SessionID]*Session),
		releasing:  make(map[marotte.SessionID]chan struct{}),
		engineLane: newLane(),
		done:       make(chan struct{}),
		stopping:   make(chan struct{}),
		exited:     make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// EngineNotifCh yields the frames no session lane took, each stamped with its engine-lane position. It closes when
// the engine ends.
func (e *Engine) EngineNotifCh() <-chan marotte.Notification { return e.engineLane.out }

// Done closes once the engine has ended and its process is reaped.
func (e *Engine) Done() <-chan struct{} { return e.done }

// Exit describes the engine's end; complete once Done is closed.
func (e *Engine) Exit() ExitInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exitInfoLocked()
}

func (e *Engine) exitInfoLocked() ExitInfo {
	info := ExitInfo{
		Reason:            e.exitReason,
		SidecarExit:       e.sidecarExit,
		JSHeapOOM:         e.jsHeapOOM.Load(),
		RelayWriteTimeout: e.relayWriteTimeout.Load(),
	}
	if !e.startedAt.IsZero() {
		info.Uptime = time.Since(e.startedAt)
	}
	return info
}

// StartLive reports what Start left KAS holding of the two connection-scoped live settings: the ignore list its send
// delivered, nil when none was sent or the send failed, and the shell timeout initialize carried, or the one sent
// after it once that send landed.
func (e *Engine) StartLive() (ignoreFiles []string, terminalTimeoutMs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startIgnoreFiles, e.startTerminalTimeoutMs
}

// KASLogDir returns the log directory the backend advertised at initialize, or "".
func (e *Engine) KASLogDir() string {
	if p := e.agentKiro.Load(); p != nil {
		return p.Logging.LogDir
	}
	return ""
}

// goBackground runs fn on the runtime's tracked goroutines when it has them; false means it drains and fn did not run.
func (e *Engine) goBackground(fn func()) bool {
	if e.background != nil {
		return e.background(fn)
	}
	go fn()
	return true
}

// A turn may run for hours, a handshake may not. A backstop, so not a setting; a var for tests. Sized for the first
// start after a version change, when KAS unpacks its runtime.
var handshakeBudget = 120 * time.Second

// replayBudget bounds a session/load: KAS streams the whole transcript before the load response, a length nothing
// here bounds. If a transcript outgrows it, add an activity-reset deadline in the frame reader, not a timer on the
// replay settle (load_projection.go).
var replayBudget = 300 * time.Second

// handshakeTimeout rewrites an expired budget into an actionable error and returns others unchanged. The raw
// `session/new: context deadline exceeded` hides that sending again is the whole recovery; errors.Is(err,
// context.DeadlineExceeded) still holds.
func handshakeTimeout(err error, phase string, budget time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s did not finish within %s. kiro-cli stopped answering. Send again to retry, and check /api/health if it keeps happening: %w",
		phase, budget, err)
}

// Start launches the engine process and initializes the connection; no session verb runs here. ctx bounds the
// handshake, capped by handshakeBudget; o.Lifetime bounds the process and is required. On an error the caller owns
// the teardown: Stop, then wait on Done.
func (e *Engine) Start(ctx context.Context, o *marotte.EngineOpts) error {
	if o.Lifetime == nil {
		return errors.New("bridge: EngineOpts.Lifetime is required: it bounds the kiro-cli process, and every default for it is a process nothing can cancel")
	}
	e.lifecycleCtx = o.Lifetime
	e.background = o.Background
	e.reapSession = o.ReapSession
	e.contentCollection = o.ContentCollection
	e.extraArgs = o.ExtraArgs
	e.authArgs = o.AuthArgs
	e.heapMiB = o.HeapMiB
	e.spawn = kascap.EngineSpawn{
		InfraSafetyMonitor:       o.Features.InfraSafetyMonitor,
		AutoRouting:              o.Features.AutoRouting,
		AutoDelegation:           o.Features.AutoDelegation,
		TerminalCommandTimeoutMs: o.Features.TerminalCommandTimeoutMs,
		SecretStorage:            o.SecretStorage,
		Knowledge:                o.Knowledge,
		SteeringReminders:        o.Features.SteeringReminders,
		DisableTelemetry:         o.DisableTelemetry,
		ModelFallback:            o.Features.ModelFallback,
	}
	if err := e.startProcess(); err != nil {
		return err
	}
	start := time.Now()
	hctx, cancel := context.WithTimeout(ctx, handshakeBudget)
	defer cancel()
	e.resolveTerminalTimeout(hctx, o.TerminalTimeout)
	if err := e.initialize(hctx); err != nil {
		return handshakeTimeout(err, "agent engine start", handshakeBudget)
	}
	// Between initialize and the first session: KAS scopes the ignore list to the connection and pushes it into every
	// session, so the first turn already enforces it.
	ignoreFiles := e.applyIgnoreFiles(hctx, o.IgnoreFiles)
	terminalTimeoutMs := e.reapplyTerminalTimeout(hctx, o.TerminalTimeout)
	e.mu.Lock()
	e.startIgnoreFiles, e.startTerminalTimeoutMs = ignoreFiles, terminalTimeoutMs
	e.mu.Unlock()
	slog.Info("engine started",
		"generation", e.generation,
		"work_dir", e.workDir,
		"kas_log_dir", logsafe.Field(e.KASLogDir()),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

func decodeAgentKiroCapabilities(result json.RawMessage) (agentKiroCapabilities, error) {
	var envelope struct {
		AgentCapabilities struct {
			Meta struct {
				Kiro json.RawMessage `json:"kiro"`
			} `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return agentKiroCapabilities{}, err
	}
	kiro := envelope.AgentCapabilities.Meta.Kiro
	if len(kiro) == 0 || string(kiro) == "null" {
		return agentKiroCapabilities{}, nil
	}
	var typed struct {
		Logging          json.RawMessage `json:"logging"`
		ExtensionMethods []string        `json:"extensionMethods"`
		ReplayMarking    bool            `json:"replayMarking"`
	}
	if err := json.Unmarshal(kiro, &typed); err != nil {
		return agentKiroCapabilities{}, err
	}
	// Diagnostic only: a malformed block must not fail the handshake.
	var logging kasLogging
	if len(typed.Logging) > 0 && json.Unmarshal(typed.Logging, &logging) != nil {
		logging = kasLogging{}
	}
	return agentKiroCapabilities{
		Logging:          logging,
		ExtensionMethods: typed.ExtensionMethods,
		ReplayMarking:    typed.ReplayMarking,
	}, nil
}

func (e *Engine) initialize(ctx context.Context) error {
	initStart := time.Now()
	// Declared in internal/kascap. This is the connection door; the session door is built from the same table.
	kiroMeta := kascap.Capabilities(&e.spawn)

	// fs and terminal route file access and command execution through marotte. elicitation makes kiro-cli forward an
	// MCP server's elicitation/create; without it the tool call stalls.
	resp, err := e.Call(ctx, methodInitialize, map[string]any{
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
	e.agentKiro.Store(&caps)
	// The engine answered, so it exists, and the relay still links it.
	e.noteEngineGroups()
	slog.Debug("ACP initialize RPC completed",
		"version", version.Build,
		"elapsed_ms", time.Since(initStart).Milliseconds(),
		"extension_methods", len(caps.ExtensionMethods),
		"replay_marking", caps.ReplayMarking,
	)
	return nil
}

// applyIgnoreFiles tells KAS which ignore files to enforce, once per engine before the first session: the value is
// connection-scoped. Resolved here, since a settings write during the start reaches this engine. Nil or empty sends
// nothing: `{files: []}` clears the list. Best-effort at Warn. Returns the list sent, nil when none was.
func (e *Engine) applyIgnoreFiles(ctx context.Context, resolve func(context.Context) []string) []string {
	if resolve == nil {
		return nil
	}
	names := resolve(ctx)
	if len(names) == 0 {
		return nil
	}
	if err := e.Notify(ctx, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{
		marotte.ParamIgnoreFiles: names,
	}); err != nil {
		slog.Warn("apply agent ignore files", "files", names, "error", err)
		return nil
	}
	return names
}

// Nil keeps the captured value.
func (e *Engine) resolveTerminalTimeout(ctx context.Context, resolve func(context.Context) int) {
	if resolve != nil {
		e.spawn.TerminalCommandTimeoutMs = resolve(ctx)
	}
}

// reapplyTerminalTimeout re-reads the timeout after initialize and sends a changed one: a save landing during the
// start has no other channel. Best-effort at Warn. Returns the timeout KAS holds.
func (e *Engine) reapplyTerminalTimeout(ctx context.Context, resolve func(context.Context) int) int {
	held := e.spawn.TerminalCommandTimeoutMs
	if resolve == nil {
		return held
	}
	ms := resolve(ctx)
	if ms == held {
		return held
	}
	if err := e.Notify(ctx, marotte.MethodTerminalSettingsChanged, marotte.TerminalSettingsParams(ms)); err != nil {
		slog.Warn("apply terminal settings", "command_timeout_ms", ms, "error", err)
		return held
	}
	e.spawn.TerminalCommandTimeoutMs = ms
	return ms
}

// openSessionIDs returns the ids of the sessions in state open.
func (e *Engine) openSessionIDs() []marotte.SessionID {
	e.lanesMu.Lock()
	defer e.lanesMu.Unlock()
	ids := make([]marotte.SessionID, 0, len(e.sessions))
	for id, s := range e.sessions {
		if s.Open() {
			ids = append(ids, id)
		}
	}
	return ids
}

// assertContentCollection resolves the content-collection value and sets it on sessionID, serialized with every
// other assert so a stale resolution never lands last. It reports the resolved value; no resolver sends nothing.
func (e *Engine) assertContentCollection(ctx context.Context, sessionID marotte.SessionID) (bool, error) {
	if e.contentCollection == nil {
		return false, nil
	}
	e.contentCollectionMu.Lock()
	defer e.contentCollectionMu.Unlock()
	enabled, _ := e.contentCollection(ctx)
	return enabled, e.sendContentCollection(ctx, sessionID, enabled)
}

// RefreshContentCollection sends the resolved content-collection value when KAS has not confirmed it. KAS holds one
// value per process but takes it only through a session, so it rides any open session; with none open, the next
// session door asserts it. Over unreadable settings it keeps a confirmed value.
func (e *Engine) RefreshContentCollection(ctx context.Context) (bool, error) {
	if e.contentCollection == nil {
		return false, nil
	}
	e.contentCollectionMu.Lock()
	defer e.contentCollectionMu.Unlock()
	enabled, readable := e.contentCollection(ctx)
	e.mu.Lock()
	applied, known := e.contentCollectionApplied, e.contentCollectionKnown
	e.mu.Unlock()
	if known && (!readable || applied == enabled) {
		return enabled, nil
	}
	ids := e.openSessionIDs()
	if len(ids) == 0 {
		return enabled, nil
	}
	return enabled, e.sendContentCollection(ctx, ids[0], enabled)
}

// sendContentCollection runs under contentCollectionMu and checks the reply, which carries the option KAS now holds.
func (e *Engine) sendContentCollection(ctx context.Context, sessionID marotte.SessionID, enabled bool) error {
	err := e.setContentCollection(ctx, sessionID, enabled)
	e.mu.Lock()
	e.contentCollectionApplied, e.contentCollectionKnown = enabled, err == nil
	e.mu.Unlock()
	return err
}

func (e *Engine) setContentCollection(ctx context.Context, sessionID marotte.SessionID, enabled bool) error {
	if sessionID == "" {
		return errors.New("no session")
	}
	resp, err := e.Call(ctx, marotte.MethodSetConfigOption, marotte.ContentCollectionParams(string(sessionID), enabled))
	if err != nil {
		return err
	}
	if got, ok := marotte.ContentCollectionReported(resp.Result); ok && got != enabled {
		return fmt.Errorf("kiro-cli reports content collection enabled=%v", got)
	}
	return nil
}
