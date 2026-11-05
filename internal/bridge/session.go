package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
)

// sessionState is a Session's place in its life (design: opening, open, closing, closed).
type sessionState int

const (
	// stateOpening: the lane is registered and the door is running; only the door calls.
	stateOpening sessionState = iota
	stateOpen
	// stateClosing: session/close is written or about to be, unconfirmed. Calls are refused; Respond still answers
	// KAS's requests during its quiesce.
	stateClosing
	stateClosed
)

// closeBudget bounds one session/close write and reply, under the engine's lifetime, never the caller's ctx. A var
// for tests.
var closeBudget = 30 * time.Second

// Session is one ACP session on the engine. It has no way to stop the engine: its own errors, timeouts and replies
// stay its own. Safe for concurrent use.
type Session struct {
	engine    *Engine
	lane      *lane
	closeDone chan struct{}
	// held closes when a released session is declared held (declareHeld), which fails a door waiting on its id.
	held chan struct{}
	// gaveUp closes when the door abandons its call; a late answer to that call acts only after it.
	gaveUp    chan struct{}
	heldTimer *time.Timer
	modes     atomic.Pointer[[]marotte.SessionMode]
	// catalog is the unfiltered advertised set: Models and ApplyServedModels derive from it, so a deprecated model the
	// account holds still counts for entitlement.
	catalog atomic.Pointer[[]marotte.SessionModel]
	// abandoned is the last prompt abandoned after its write, until AwaitAbandonedPrompt spends it.
	abandoned *abandonedPrompt
	modelID   marotte.ModelID
	// thinking is the session's last reported thinking value ("on", "off", or "" when untoggleable or unreported).
	thinking    string
	role        marotte.SessionRole
	id          marotte.SessionID
	currentMode string
	// sessionTitle is KAS's title for the session (flat `_meta.title`).
	sessionTitle string
	// effortLevel is the tier the session last reported, not the requested one, so the door repairs. Empty means
	// unknown and must assert. ObserveEffort feeds it too.
	effortLevel string
	// lastErr is custody's latest miss, for the held report.
	lastErr string
	// workflowLaunchWant is the door's wanted workflowLaunch value, immutable after construction; empty asserts
	// nothing. workflowLaunch is the value the session last reported, meaningful only while workflowLaunchOffered.
	workflowLaunchWant string
	workflowLaunch     string
	// policyErrors are the rules KAS skipped at the door (flat `_meta.policyErrors`).
	policyErrors []marotte.PolicyErrorItem
	// spawn is this session's door input, immutable after the door.
	spawn kascap.SessionSpawn
	// loadSeq is the lane position the session/load response arrived at (SessionLoadSeq).
	loadSeq uint64
	// heldGen identifies the armed held clock, so a stopped clock's callback does nothing.
	heldGen        uint64
	state          sessionState
	mu             sync.Mutex
	titleSetByUser bool
	// supervised records that the session accepted `autopilot: off`, not that the chat asked.
	supervised bool
	// closeAsked is a close that arrived during the door; the door releases what KAS created instead of opening it.
	closeAsked bool
	// released: no caller holds the session any more; its lane is closed and only custody settles it (Release).
	released bool
	// reported: the held report for this handle was logged, by the custody's clock or an abandoned door's.
	reported bool
	// reapOnSettle reaps the session's files once its release settles: a session/new its door abandoned.
	reapOnSettle bool
	// workflowLaunchRefused latches once a set_config_option did not confirm workflowLaunchWant, so a KAS that
	// keeps reporting another value is asked once per session, not on every frame. workflowLaunchOffered is
	// whether the session's last report carried the option at all.
	workflowLaunchOffered bool
	workflowLaunchRefused bool
}

// SessionID returns the session's ACP id.
func (s *Session) SessionID() marotte.SessionID { return s.id }

// Open reports whether calls may use the session: its door succeeded, no close is under way, and its engine lives.
func (s *Session) Open() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateOpen
}

// NotifCh yields the frames routed to this session, each stamped with its lane position. It closes when the session
// closes or the engine ends.
func (s *Session) NotifCh() <-chan marotte.Notification { return s.lane.out }

func (s *Session) callable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case stateOpen:
		return nil
	case stateOpening:
		return marotte.ErrSessionOpening
	case stateClosing:
		return marotte.ErrSessionClosing
	case stateClosed:
		if s.engine.readerExited.Load() {
			return &marotte.TransportError{Err: fmt.Errorf("%w: %w", errBridgeExited, marotte.ErrNotWritten), Retryable: true}
		}
		return marotte.ErrSessionClosed
	}
	return marotte.ErrSessionClosed
}

// Call sends a request on the session, positioned on its lane. Refused unless the session is open.
func (s *Session) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, _, err := s.CallAt(ctx, method, params)
	return resp, err
}

// CallAt is Call plus the lane position the response arrived at, for callers ordering a decision against frames still
// in flight. A cancelled ctx abandons the request and returns position zero. session/prompt is refused: PromptAt
// writes it.
func (s *Session) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if err := s.callable(); err != nil {
		return nil, 0, err
	}
	return s.engine.callAt(ctx, s.lane, s.id, method, params, nil)
}

// PromptAt writes the session's session/prompt and waits for its outcome, as CallAt. commit, when non-nil, runs under
// the engine's write lock immediately before the frame: its error refuses the write and is the answer, with nothing
// written, and a nil lets the frame out ahead of every frame written after commit returned.
func (s *Session) PromptAt(ctx context.Context, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	if err := s.callable(); err != nil {
		return nil, 0, err
	}
	p := &abandonedPrompt{done: make(chan struct{})}
	resp, seq, abandoned, err := s.engine.request(ctx, s.lane, s.id, marotte.MethodPrompt, params, p.answer, commit)
	if abandoned {
		s.mu.Lock()
		s.abandoned = p
		s.mu.Unlock()
	}
	return resp, seq, err
}

// abandonedPrompt is a written session/prompt whose caller gave up: KAS keeps streaming its execution's frames, and
// its answer, at lane position seq, is the last of them.
type abandonedPrompt struct {
	done chan struct{}
	seq  uint64
}

// answer is the prompt's late hook; the engine calls it at most once.
func (p *abandonedPrompt) answer(_ *marotte.RPCResponse, seq uint64) {
	p.seq = seq
	close(p.done)
}

// AwaitAbandonedPrompt waits for the answer to the session's last session/prompt abandoned after its write, returning
// the lane position it arrived at. It returns false at once when none is outstanding, and when ctx ends or the session
// closes first (its engine's exit closes it, and drops the abandoned entry unanswered). Either way the record is
// spent, so the next call waits for nothing.
func (s *Session) AwaitAbandonedPrompt(ctx context.Context) (uint64, bool) {
	s.mu.Lock()
	p := s.abandoned
	s.abandoned = nil
	s.mu.Unlock()
	if p == nil {
		return 0, false
	}
	select {
	case <-p.done:
		return p.seq, true
	case <-ctx.Done():
	case <-s.closeDone:
	}
	return 0, false
}

// doorSetConfig is a session/set_config_option the door itself makes while the session is opening.
func (s *Session) doorSetConfig(ctx context.Context, params any) (*marotte.RPCResponse, error) {
	resp, _, err := s.engine.callAt(ctx, s.lane, s.id, marotte.MethodSetConfigOption, params, nil)
	return resp, err
}

// Notify sends a notification on the session's behalf. Refused unless the session is open.
func (s *Session) Notify(ctx context.Context, method string, params any) error {
	if err := s.callable(); err != nil {
		return err
	}
	return s.engine.Notify(ctx, method, params)
}

// Respond answers a request KAS sent on this session. Allowed while closing: KAS's quiesce may ask.
func (s *Session) Respond(ctx context.Context, id int64, result any, err error) error {
	return s.engine.Respond(ctx, id, result, err)
}

// SessionLoadSeq returns the lane position of the session/load response, which a consumer must have folded to before
// treating the replay as complete. Only a load sets it; 0 is also a legal position, so pair it with the load having
// returned.
func (s *Session) SessionLoadSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSeq
}

// ModelID returns the currently-selected model id.
func (s *Session) ModelID() marotte.ModelID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelID
}

// CurrentMode returns the currently-selected session mode.
func (s *Session) CurrentMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentMode
}

// SupervisedApplied reports whether the session accepted `autopilot: off`. Whether the chat asked lives on the chat
// record, and a caller must read both to report a refused assert.
func (s *Session) SupervisedApplied() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supervised
}

// SessionTitle returns KAS's title for the live session: "New Session" on creation, the stored title on load. Not
// authoritative: the caller adopts it only for a default-named chat, and focus_update outranks it.
func (s *Session) SessionTitle() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionTitle
}

// SessionTitleSetByUser reports whether KAS holds the title as the user's (flat `_meta.titleSetByUser`).
func (s *Session) SessionTitleSetByUser() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.titleSetByUser
}

// PolicyErrors returns a copy of the rules KAS reported skipping or failing to load when the
// session opened; nil when it reported none.
func (s *Session) PolicyErrors() []marotte.PolicyErrorItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.policyErrors)
}

// Modes returns the session modes from the door. Frozen; callers must not mutate it.
func (s *Session) Modes() []marotte.SessionMode {
	if p := s.modes.Load(); p != nil {
		return *p
	}
	return nil
}

// Catalog returns the door result's unfiltered catalog. Frozen; callers must not mutate it.
func (s *Session) Catalog() []marotte.SessionModel {
	if p := s.catalog.Load(); p != nil {
		return *p
	}
	return nil
}

// Models returns Catalog without [Deprecated] / [Legacy] entries (modeltext.Hidden).
func (s *Session) Models() []marotte.SessionModel {
	catalog := s.Catalog()
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

// SetModel swaps the model in-session via session/set_config_option (configId "model"). On failure the model id is
// unchanged.
func (s *Session) SetModel(ctx context.Context, modelID string) error {
	if err := s.callable(); err != nil {
		return err
	}
	return s.setModel(ctx, modelID)
}

func (s *Session) setModel(ctx context.Context, modelID string) error {
	if _, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionModel,
		keyConfigValue:       modelID,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.modelID = marotte.ModelID(modelID)
	// A swap can reset effort invisibly: KAS reconciles against the new model's tiers. Clearing makes the next call
	// assert.
	s.effortLevel = ""
	s.thinking = ""
	s.mu.Unlock()
	return nil
}

// EnsureEffort makes the session run at level via session/set_config_option (configId "effortLevel"). It sends only
// when the reported level differs (SetModel clears the cache), drops an invalid level, and caches the reply: KAS
// silently ignores a level the model lacks.
func (s *Session) EnsureEffort(ctx context.Context, level string) error {
	if err := s.callable(); err != nil {
		return err
	}
	return s.ensureEffort(ctx, level)
}

func (s *Session) ensureEffort(ctx context.Context, level string) error {
	if level == "" || !marotte.EffortLevel(level).Valid() {
		return nil
	}
	s.mu.Lock()
	current := s.effortLevel
	s.mu.Unlock()
	if level == current {
		return nil
	}
	resp, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionEffort,
		keyConfigValue:       level,
	})
	if err != nil {
		return err
	}
	// Cache what the session reports: a level set on `auto` returns ok with no effortLevel option.
	var out struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	if resp != nil && len(resp.Result) > 0 && json.Unmarshal(resp.Result, &out) == nil {
		s.mu.Lock()
		s.applyEffortConfigOptionLocked(out.ConfigOptions)
		s.mu.Unlock()
	}
	return nil
}

// EnsureThinking makes the session's thinking match choice ("on" or "off") via set_config_option (configId
// "thinking"). A string: KAS ignores a boolean. Sent only when the session reported a different value. The reply
// refreshes thinking and effort, since thinking off caps a high tier.
func (s *Session) EnsureThinking(ctx context.Context, choice string) error {
	if err := s.callable(); err != nil {
		return err
	}
	return s.ensureThinking(ctx, choice)
}

func (s *Session) ensureThinking(ctx context.Context, choice string) error {
	if choice != marotte.ThinkingOn && choice != marotte.ThinkingOff {
		return nil
	}
	s.mu.Lock()
	current := s.thinking
	s.mu.Unlock()
	if current == "" || current == choice {
		return nil
	}
	resp, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionThinking,
		keyConfigValue:       choice,
	})
	if err != nil {
		return err
	}
	var out struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	s.mu.Lock()
	s.thinking = choice
	if resp != nil && len(resp.Result) > 0 && json.Unmarshal(resp.Result, &out) == nil {
		s.applyThinkingConfigOptionLocked(out.ConfigOptions)
		s.applyEffortConfigOptionLocked(out.ConfigOptions)
	}
	s.mu.Unlock()
	return nil
}

// HealWorkflowLaunch records the workflowLaunch value a config_option_update offered and asserts the door's wanted
// value when it differs. After one failed or unconfirmed assert the session sends no more, and returns nil.
func (s *Session) HealWorkflowLaunch(ctx context.Context, reported string) error {
	if err := s.callable(); err != nil {
		return err
	}
	s.mu.Lock()
	s.workflowLaunch, s.workflowLaunchOffered = reported, true
	s.mu.Unlock()
	return s.ensureWorkflowLaunch(ctx)
}

// recordWorkflowLaunch records the workflowLaunch option a door result carries, or that it carries none.
func (s *Session) recordWorkflowLaunch(result json.RawMessage) {
	value, offered := marotte.WorkflowLaunchReported(result)
	s.mu.Lock()
	s.workflowLaunch, s.workflowLaunchOffered = value, offered
	s.mu.Unlock()
}

func (s *Session) ensureWorkflowLaunch(ctx context.Context) error {
	s.mu.Lock()
	want, reported := s.workflowLaunchWant, s.workflowLaunch
	skip := want == "" || !s.workflowLaunchOffered || reported == want || s.workflowLaunchRefused
	s.mu.Unlock()
	if skip {
		return nil
	}
	resp, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionWorkflowLaunch,
		keyConfigValue:       want,
	})
	var confirmed string
	if err == nil && resp != nil {
		confirmed, _ = marotte.WorkflowLaunchReported(resp.Result)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if confirmed == want {
		s.workflowLaunch = want
		return nil
	}
	s.workflowLaunchRefused = true
	if err == nil {
		err = fmt.Errorf("%w: reply reports %q", errWorkflowLaunchUnconfirmed, confirmed)
	}
	return err
}

// errWorkflowLaunchUnconfirmed is a set_config_option reply that does not report the workflowLaunch value sent.
var errWorkflowLaunchUnconfirmed = errors.New("bridge: kiro-cli did not confirm the workflow use value")

// ObserveThinking records a thinking value from the config_option_update channel the session forwards unread. Empty
// is ignored.
func (s *Session) ObserveThinking(value string) {
	if value != marotte.ThinkingOn && value != marotte.ThinkingOff {
		return
	}
	s.mu.Lock()
	s.thinking = value
	s.mu.Unlock()
}

// ObserveEffort records a level the session reported on config_option_update. KAS moves the level itself (first
// prompt pin, an IDE or TUI swap), and a stale cache would skip the repair. Empty is ignored.
func (s *Session) ObserveEffort(level string) {
	if level == "" {
		return
	}
	s.mu.Lock()
	s.effortLevel = level
	s.mu.Unlock()
}

// Close sends session/close for a caller that keeps the session if kiro-cli refuses: KAS quiesces it and disposes it
// in memory, keeping its persisted state. The write and the reply run under the engine's lifetime bounded by
// closeBudget. Outcomes: a result closes it (KAS answers {} for a session it holds, one it closed and one it never
// had, so a re-send is idempotent); an in-budget error reply reopens it and returns the error; a spent budget or the
// caller's ctx ending lets go of it as Release does, returning ErrSessionClosing or the ctx error. On a session still
// opening nothing is written until its door answers, and Close waits for the door's release of what KAS created.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case stateClosed:
		s.mu.Unlock()
		return nil
	case stateClosing:
		s.mu.Unlock()
		return s.awaitClosed(ctx)
	case stateOpening:
		s.closeAsked = true
		s.mu.Unlock()
		return s.awaitClosed(ctx)
	}
	s.state = stateClosing
	s.mu.Unlock()
	result := make(chan error, 1)
	if !s.engine.goBackground(func() { result <- s.sendClose() }) {
		result <- s.sendClose()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		s.Release()
		return ctx.Err()
	}
}

// awaitClosed waits for a close under way to settle, bounded by ctx.
func (s *Session) awaitClosed(ctx context.Context) error {
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		return marotte.ErrSessionClosing
	}
}

// sendClose writes a holder's session/close once and settles the state from its reply. Once the session is released,
// by the holder giving up or a Release meanwhile, a refusal or a silence hands it to custody instead of reopening it.
func (s *Session) sendClose() error {
	cctx, cancel := context.WithTimeout(s.engine.lifecycleCtx, closeBudget)
	defer cancel()
	resp, _, err := s.engine.callAt(cctx, s.lane, s.id, methodSessionClose,
		map[string]any{marotte.KeySessionID: s.id}, s.lateRelease)
	switch {
	case err == nil:
		s.settle()
		return nil
	case marotte.ReplyUnread(err):
		// KAS may have closed it, so this is no refusal: custody re-sends, and a re-sent close is answered {} either way.
		slog.Warn("a session's close answer could not be read; the close is re-sent",
			"session_id", s.id, "role", s.role, "error", err)
		s.Release()
		s.noteMiss(err)
		s.startCustody(true)
		return marotte.ErrSessionClosing
	case resp != nil && resp.Error != nil:
		// Any error reply is a refusal, whatever its code: KAS may still hold the session live.
		if s.reopen() {
			return err
		}
		s.noteMiss(err)
		s.startCustody(true)
		return err
	case errors.Is(err, errBridgeExited), errors.Is(err, errWriteFenced):
		s.closed()
		return err
	case errors.Is(err, context.DeadlineExceeded):
		s.Release()
		s.noteMiss(err)
		s.startCustody(true)
		return marotte.ErrSessionClosing
	case errors.Is(err, context.Canceled):
		// The lifetime ended: marotte is draining and the engine goes with it.
		return marotte.ErrSessionClosing
	}
	s.closed()
	return err
}

// reopen returns a holder's refused close to open, unless the session was released meanwhile.
func (s *Session) reopen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateClosing || s.released {
		return false
	}
	s.state = stateOpen
	return true
}

// closed moves the session to closed: it leaves the lane table, so every later frame naming it reaches the engine
// lane, and its lane delivers what it queued, then ends. False means it was closed already.
func (s *Session) closed() bool {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return false
	}
	s.state = stateClosed
	s.stopHeldClockLocked()
	s.mu.Unlock()
	e := s.engine
	e.lanesMu.Lock()
	if e.sessions[s.id] == s {
		delete(e.sessions, s.id)
	}
	e.lanesMu.Unlock()
	s.lane.close()
	close(s.closeDone)
	return true
}

// engineEnded closes the session with its engine; the exit path already took it off the lane table.
func (s *Session) engineEnded() {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return
	}
	s.state = stateClosed
	s.stopHeldClockLocked()
	reported := s.reported
	s.mu.Unlock()
	s.lane.close()
	close(s.closeDone)
	if reported {
		slog.Info(releasedMsgText, "session_id", s.id, "role", s.role)
	}
}

// detached closes a session whose lane overflowed; the lane is already cut loose.
func (s *Session) detached() {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return
	}
	s.state = stateClosed
	s.mu.Unlock()
	close(s.closeDone)
}
