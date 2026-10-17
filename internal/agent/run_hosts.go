package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// errRunHostStart marks a run whose launching session could not be loaded: a server fault, answered 500 without
// echoing an internal path.
var errRunHostStart = errors.New("the run's launching session could not be loaded")

// Driving it from another session would drop the profile's presets from its steps. Answered 409.
var errLaunchSessionUnavailable = errors.New("the run's launching session cannot be opened here")

const launchSessionUnavailableText = "This run's launching session cannot be opened on this server. " +
	"Cancel and delete still work."

// errRunNotListed means KAS's inventory does not list the run. Answered 404.
var errRunNotListed = errors.New("run not found")

// errRuntimeDraining settles a host load or scheduled launch refused because marotte is shutting down.
var errRuntimeDraining = errors.New("marotte is shutting down")

// drivenHost is the resident session a driving verb writes on: the run's launching session, which holds the profile's
// presets its steps take. chatID is its lane's chat key (the chat's id, or the run's own run:<id>).
type drivenHost struct {
	sb     *sharedBridge
	chatID marotte.ChatID
}

// ensureResident makes the run's launching session resident, one load per run at a time: a chat-parented run's chat
// session through the chat's own open, a parentless run's host session loaded under run:<id> with the presets its
// lease recorded at launch. A load runs to completion whatever ctx does, since a cancelled door would detach the
// session; ctx bounds only this caller's waits, for the run's lock and for the load.
func (rs *Runs) ensureResident(ctx context.Context, workflowID string) (*drivenHost, error) {
	unlock, err := rs.hosts.acquire(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if sb := rs.residentRunHost(workflowID); sb != nil {
		return &drivenHost{sb: sb, chatID: runChatID(workflowID)}, nil
	}
	o := rs.originFromLease(ctx, workflowID)
	if o.err != nil {
		return nil, o.err
	}
	if o.parent.chat != "" {
		sb, hErr := rs.hostOnChat(ctx, workflowID, o.parent)
		if hErr != nil {
			return nil, hErr
		}
		rs.router.registerOwner(workflowID, marotte.SessionID(o.parent.session))
		return &drivenHost{sb: sb, chatID: o.parent.chat}, nil
	}
	l, held := rs.lease(workflowID)
	if !held {
		return nil, fmt.Errorf("%w: a run marotte did not launch has no recorded security profile",
			errLaunchSessionUnavailable)
	}
	presets, err := l.HostPresets()
	if err != nil {
		slog.Warn("a run's launching session was not loaded, because its lease recorded no security profile",
			"workflow_id", workflowID)
		return nil, fmt.Errorf("%w: %w", errLaunchSessionUnavailable, err)
	}
	sb, err := rs.loadHost(ctx, workflowID, o.parent.session, presets)
	if err != nil {
		return nil, err
	}
	return &drivenHost{sb: sb, chatID: runChatID(workflowID)}, nil
}

// hostOnChat reaches a chat-parented run through the chat's own session, never a second one: that session must be
// the run's launching session, since openBridge can fall back to a new one.
func (rs *Runs) hostOnChat(ctx context.Context, workflowID string, parent runParent) (*sharedBridge, error) {
	held, err := rs.coord.openBridge(ctx, parent.chat, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
	}
	if got := string(held.SessionID()); got != parent.session {
		slog.Warn("a run's launching chat is open on another session, so the run was not driven",
			"workflow_id", workflowID, "chat_id", parent.chat,
			"launch_session", parent.session, "chat_session", got)
		return nil, errLaunchSessionUnavailable
	}
	return held, nil
}

// loadHost loads a parentless run's host session under run:<id>, its forward attached before the door, and makes the
// router route the run's frames to it. The door runs under the runtime's lifetime, bounded by launchTimeout; ctx
// bounds only this caller's wait, and a later caller waits on a load still under way rather than starting another.
func (rs *Runs) loadHost(ctx context.Context, workflowID, session string, presets []string) (*sharedBridge, error) {
	chatID := runChatID(workflowID)
	sb, existed := rs.bridges.orInsert(chatID)
	// pending first: a load that settles after the caller's residency read is resident by the second test.
	if existed && (sb.settled.pending() || rs.residentRunHost(workflowID) == sb) {
		return awaitHost(ctx, sb)
	}
	if existed {
		rs.bridges.removeIfSame(chatID, sb)
		if sb, existed = rs.bridges.orInsert(chatID); existed {
			return nil, fmt.Errorf("%w: another load of this run's session is under way", errRunHostStart)
		}
	}
	if !rs.lifecycle.goUnlessDraining(func() { rs.openHost(workflowID, session, presets, sb) }) {
		rs.bridges.removeIfSame(chatID, sb)
		sb.settled.settle(errRuntimeDraining)
	}
	return awaitHost(ctx, sb)
}

func awaitHost(ctx context.Context, sb *sharedBridge) (*sharedBridge, error) {
	if err := sb.settled.wait(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
	}
	return sb, nil
}

func (rs *Runs) openHost(workflowID, session string, presets []string, sb *sharedBridge) {
	chatID := runChatID(workflowID)
	lctx, cancel := context.WithTimeout(rs.lifecycle.shutdownCtx, launchTimeout)
	defer cancel()
	err := rs.engine.loadSession(lctx, &marotte.SessionOpts{
		SessionID: session, Role: marotte.SessionRoleHost, Presets: presets,
	}, func(s engineSession) {
		sb.attach(s)
		rs.coord.goForward(chatID, s)
	})
	if err != nil {
		rs.bridges.removeIfSame(chatID, sb)
		sb.settled.settle(err)
		return
	}
	sb.setIdle()
	rs.router.registerOwner(workflowID, marotte.SessionID(session))
	sb.settled.settle(nil)
	slog.Info("loaded a run's launching session", "workflow_id", workflowID, "session_id", session)
}

// residentRunHost is the run's own resident host session under run:<id>, or nil (also while loading).
func (rs *Runs) residentRunHost(workflowID string) *sharedBridge {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil && sb.startedPastOpen() && sb.current().Open() {
		return sb
	}
	return nil
}

// call writes one driving verb on the host session and records it in the run-mutation registry.
func (h *drivenHost) call(ctx context.Context, rs *Runs, workflowID, method string, params map[string]any) (*marotte.RPCResponse, error) {
	return rs.drive(ctx, h.sb, workflowID, method, params, mutationDrive)
}

// drive writes method for workflowID on sb's session, the lane the run's frames reach, entered in the run-mutation
// registry immediately before the write: a restart or an engine end before its effect is observed keeps the run in
// the interrupted set.
func (rs *Runs) drive(
	ctx context.Context, sb *sharedBridge, workflowID, method string, params map[string]any, kind mutationKind,
) (*marotte.RPCResponse, error) {
	_, gen := rs.engine.current()
	m := rs.mutations.enter(workflowID, kind, gen)
	resp, seq, err := sb.CallAt(ctx, method, params)
	if errors.Is(err, marotte.ErrSessionClosed) || errors.Is(err, marotte.ErrSessionClosing) {
		// Refused before the write: the session was not open.
		m.unwritten()
		return nil, err
	}
	m.replied(resp, err, laneRef{session: sb.current()}, seq)
	return resp, runCallErr(resp, err)
}

// stopVerb writes a stop verb (pause, cancel) for workflowID positioned on the lane its lifecycle frames reach: the
// resident owner session's, or the engine lane's for a run whose owner is not resident.
func (rs *Runs) stopVerb(ctx context.Context, workflowID, method string, params map[string]any) error {
	if owner, ok := rs.residentOwner(workflowID); ok {
		_, err := rs.drive(ctx, owner.sb, workflowID, method, params, mutationStop)
		return err
	}
	h, gen, release, err := rs.engine.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	m := rs.mutations.enter(workflowID, mutationStop, gen)
	resp, seq, err := h.CallAt(ctx, method, params)
	m.replied(resp, err, laneRef{gen: gen}, seq)
	return runCallErr(resp, err)
}

// residentOwner is the resident session the run's frames route to, with no load.
func (rs *Runs) residentOwner(workflowID string) (*drivenHost, bool) {
	if sb := rs.residentRunHost(workflowID); sb != nil {
		return &drivenHost{sb: sb, chatID: runChatID(workflowID)}, true
	}
	l, held := rs.lease(workflowID)
	if !held || l.ChatID == "" || l.ParentSessionID == "" {
		return nil, false
	}
	chatID := marotte.ChatID(l.ChatID)
	sb := rs.bridges.get(chatID)
	if sb == nil || !sb.startedPastOpen() || !sb.current().Open() || string(sb.SessionID()) != l.ParentSessionID {
		return nil, false
	}
	return &drivenHost{sb: sb, chatID: chatID}, true
}

// hostSecurityOf is a parentless launch's recorded presets, nil for a chat-parented one.
func hostSecurityOf(presets []string) *runlease.HostSecurity {
	if presets == nil {
		presets = []string{}
	}
	return &runlease.HostSecurity{Presets: presets}
}

// hostRelay holds a new host session's frames until its run's id names its lane: KAS's requests during the door are
// answered at once, and every notification is delivered to the forward once it starts, in lane order.
type hostRelay struct {
	engineSession
	out   chan marotte.Notification
	start chan struct{}
}

// newHostRelay relays s's frames; early answers the requests that arrive before start.
func newHostRelay(s engineSession, early func(msg *marotte.RPCResponse)) *hostRelay {
	r := &hostRelay{engineSession: s, out: make(chan marotte.Notification), start: make(chan struct{})}
	go r.run(early)
	return r
}

func (r *hostRelay) NotifCh() <-chan marotte.Notification { return r.out }

// begin lets the forward read: everything held is delivered first.
func (r *hostRelay) begin() { close(r.start) }

func (r *hostRelay) run(early func(msg *marotte.RPCResponse)) {
	defer close(r.out)
	src := r.engineSession.NotifCh()
	var held []marotte.Notification
	for {
		select {
		case n, ok := <-src:
			if !ok {
				return
			}
			if n.Msg != nil && n.Msg.ID != nil && n.Msg.Method != "" {
				early(n.Msg)
				continue
			}
			held = append(held, n)
			continue
		case <-r.start:
		}
		break
	}
	for _, n := range held {
		r.out <- n
	}
	for n := range src {
		r.out <- n
	}
}
