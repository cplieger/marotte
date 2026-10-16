package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// closeRetries are the delays before the two re-sends of a session/close that got no reply.
var closeRetries = []time.Duration{30 * time.Second, 120 * time.Second}

// Session is one ACP session on the engine. It has no way to stop the engine: its own errors, timeouts and replies
// stay its own. Safe for concurrent use.
type Session struct {
	engine    *Engine
	lane      *lane
	closeDone chan struct{}
	modes     atomic.Pointer[[]marotte.SessionMode]
	// catalog is the unfiltered advertised set: Models and ApplyServedModels derive from it, so a deprecated model the
	// account holds still counts for entitlement.
	catalog atomic.Pointer[[]marotte.SessionModel]
	modelID marotte.ModelID
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
	// spawn is this session's door input, immutable after the door.
	spawn kascap.SessionSpawn
	// loadSeq is the lane position the session/load response arrived at (SessionLoadSeq).
	loadSeq uint64
	// summarizationPct is the summarization threshold the session reported; 0 until a load result carries one.
	summarizationPct float64
	state            sessionState
	mu               sync.Mutex
	titleSetByUser   bool
	// supervised records that the session accepted `autopilot: off`, not that the chat asked.
	supervised bool
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
	case stateClosing:
		return marotte.ErrSessionClosing
	case stateClosed:
		if s.engine.readerExited.Load() {
			return &marotte.TransportError{Err: errBridgeExited, Retryable: true}
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
// in flight (the prompt paths). A cancelled ctx abandons the request and returns position zero.
func (s *Session) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if err := s.callable(); err != nil {
		return nil, 0, err
	}
	return s.engine.callAt(ctx, s.lane, s.id, method, params, nil)
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

// AutoCompactionDisabled reports whether this session opened with KAS compaction off: KAS froze the door's value.
func (s *Session) AutoCompactionDisabled() bool { return s.spawn.DisableAutoCompaction }

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

// SummarizationThreshold returns the percentage at which the session summarizes its context, from session/load's
// flat `_meta.contextUsage`; 0 when none carried it.
func (s *Session) SummarizationThreshold() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summarizationPct
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

// Close sends session/close and ends the session: KAS quiesces it and disposes it in memory, keeping its persisted
// state. The write and the reply run under the engine's lifetime bounded by closeBudget; ctx only bounds how long the
// caller waits, so a caller giving up never abandons the close. Outcomes: a result closes it (KAS answers {} for a
// session it holds, one it closed and one it never had, so a retry is idempotent); an error reply reopens it and
// returns the error; no reply leaves it closing, retried twice in the background, and returns ErrSessionClosing.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case stateClosed:
		s.mu.Unlock()
		return nil
	case stateClosing:
		s.mu.Unlock()
		return s.awaitClosed(ctx)
	}
	s.state = stateClosing
	s.mu.Unlock()
	budget, retries := closeBudget, closeRetries
	result := make(chan error, 1)
	if !s.engine.goBackground(func() { result <- s.sendClose(budget, retries) }) {
		result <- s.sendClose(budget, retries)
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
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

// sendClose writes session/close once and settles the state from its reply.
func (s *Session) sendClose(budget time.Duration, retries []time.Duration) error {
	cctx, cancel := context.WithTimeout(s.engine.lifecycleCtx, budget)
	defer cancel()
	resp, _, err := s.engine.callAt(cctx, s.lane, s.id, methodSessionClose,
		map[string]any{marotte.KeySessionID: s.id}, s.lateClose)
	switch {
	case err == nil:
		s.closed()
		return nil
	case resp != nil && resp.Error != nil:
		// Any error reply is a refusal, whatever its code: KAS may still hold the session live.
		s.reopen()
		return err
	case errors.Is(err, errBridgeExited), errors.Is(err, errWriteFenced):
		s.closed()
		return err
	case errors.Is(err, context.DeadlineExceeded):
		slog.Warn("a session did not confirm its close; it stays until it does or the engine restarts",
			"session_id", s.id, "role", s.role)
		s.retryClose(budget, retries)
		return marotte.ErrSessionClosing
	case errors.Is(err, context.Canceled):
		// The lifetime ended: marotte is draining and the engine goes with it.
		return marotte.ErrSessionClosing
	}
	s.closed()
	return err
}

// lateClose finalizes a close whose reply came after its budget.
func (s *Session) lateClose(resp *marotte.RPCResponse) {
	if resp.Error != nil {
		s.reopen()
		return
	}
	s.closed()
}

// retryClose re-sends an unconfirmed close at each closeRetries delay, under the lifetime, until a reply settles it.
func (s *Session) retryClose(budget time.Duration, retries []time.Duration) {
	s.engine.goBackground(func() {
		for _, delay := range retries {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-s.closeDone:
				timer.Stop()
				return
			case <-s.engine.lifecycleCtx.Done():
				timer.Stop()
				return
			}
			s.mu.Lock()
			closing := s.state == stateClosing
			s.mu.Unlock()
			if !closing {
				return
			}
			cctx, cancel := context.WithTimeout(s.engine.lifecycleCtx, budget)
			resp, _, err := s.engine.callAt(cctx, s.lane, s.id, methodSessionClose,
				map[string]any{marotte.KeySessionID: s.id}, s.lateClose)
			cancel()
			switch {
			case err == nil:
				s.closed()
				return
			case resp != nil && resp.Error != nil:
				s.reopen()
				return
			case errors.Is(err, errBridgeExited), errors.Is(err, errWriteFenced):
				return
			}
		}
	})
}

func (s *Session) reopen() {
	s.mu.Lock()
	if s.state == stateClosing {
		s.state = stateOpen
	}
	s.mu.Unlock()
}

// closed moves the session to closed: it leaves the lane table, so every later frame naming it reaches the engine
// lane, and its lane delivers what it queued, then ends.
func (s *Session) closed() {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return
	}
	s.state = stateClosed
	s.mu.Unlock()
	e := s.engine
	e.lanesMu.Lock()
	if e.sessions[s.id] == s {
		delete(e.sessions, s.id)
	}
	e.lanesMu.Unlock()
	s.lane.close()
	close(s.closeDone)
}

// engineEnded closes the session with its engine; the exit path already took it off the lane table.
func (s *Session) engineEnded() {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return
	}
	s.state = stateClosed
	s.mu.Unlock()
	s.lane.close()
	close(s.closeDone)
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
