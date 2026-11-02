package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// errRunWorkUnadmitted refuses, unwritten, a run-work request whose context carries no admission.
var errRunWorkUnadmitted = errors.New("run work was sent without an admission")

// runWorkVerbs make a run's step do model work, whichever lane carries them.
var runWorkVerbs = map[string]bool{
	methodKiroWorkflowInvoke: true,
	methodKiroWorkflowResume: true,
	methodKiroWorkflowRetry:  true,
	methodKiroWorkflowUpdate: true,
}

// isRunWork reports whether a request written on lane's session ("" for the engine lane) is run work: a run-work
// verb, or a prompt or steer addressed to a session other than lane's own, which is a step's, since a chat prompts
// and steers its own session. A prompt or steer naming no session counts as run work.
func isRunWork(lane marotte.SessionID, method string, params any) bool {
	if runWorkVerbs[method] {
		return true
	}
	if method != marotte.MethodPrompt && method != marotte.MethodSessionSteer {
		return false
	}
	target, ok := paramsSessionID(params)
	return !ok || target != lane
}

func paramsSessionID(params any) (marotte.SessionID, bool) {
	if m, ok := params.(map[string]any); ok {
		switch v := m[marotte.KeySessionID].(type) {
		case string:
			return marotte.SessionID(v), v != ""
		case marotte.SessionID:
			return v, v != ""
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", false
	}
	var p struct {
		SessionID marotte.SessionID `json:"sessionId"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return "", false
	}
	return p.SessionID, p.SessionID != ""
}

type admissionKey struct{}

// bind is ctx carrying a, the proof guardRunWork asks of a run-work write; an unadmitted a binds nothing.
func (a runAdmission) bind(ctx context.Context) context.Context {
	if !a.admitted() {
		return ctx
	}
	return context.WithValue(ctx, admissionKey{}, a)
}

// guardRunWork refuses run work whose ctx no admitRunWork admission was bound to (runAdmission.bind).
func guardRunWork(ctx context.Context, lane marotte.SessionID, method string, params any) error {
	if !isRunWork(lane, method, params) {
		return nil
	}
	if a, _ := ctx.Value(admissionKey{}).(runAdmission); a.admitted() {
		return nil
	}
	slog.Error("run work was sent without an admission; refused unwritten", "method", method, "lane", lane)
	return errRunWorkUnadmitted
}

// guardedEngine is every engine as the owner holds it (engineOwner.spawn). Only the writers that can carry run work,
// Call, CallAt and Notify on it and on each session it opens and a session's PromptAt, pass guardRunWork first; every
// other method reaches the embedded value unguarded, so TestRunWork_EveryEngineMethodIsGuardedOrClassified fails on
// a new method until it is guarded or listed as one that sends no run work.
type guardedEngine struct{ engineHandle }

func (g guardedEngine) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if err := guardRunWork(ctx, "", method, params); err != nil {
		return nil, err
	}
	return g.engineHandle.Call(ctx, method, params)
}

func (g guardedEngine) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if err := guardRunWork(ctx, "", method, params); err != nil {
		return nil, 0, err
	}
	return g.engineHandle.CallAt(ctx, method, params)
}

func (g guardedEngine) Notify(ctx context.Context, method string, params any) error {
	if err := guardRunWork(ctx, "", method, params); err != nil {
		return err
	}
	return g.engineHandle.Notify(ctx, method, params)
}

func (g guardedEngine) NewSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	s, err := g.engineHandle.NewSession(ctx, o, guardAttach(attach))
	return guardSession(s), err
}

func (g guardedEngine) LoadSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	s, err := g.engineHandle.LoadSession(ctx, o, guardAttach(attach))
	return guardSession(s), err
}

// guardedSession is a value, so two guards of one session compare equal, as the sessions they guard do.
type guardedSession struct{ engineSession }

func guardSession(s engineSession) engineSession {
	if s == nil {
		return nil
	}
	return guardedSession{s}
}

func guardAttach(attach func(engineSession)) func(engineSession) {
	if attach == nil {
		return nil
	}
	return func(s engineSession) { attach(guardSession(s)) }
}

func (g guardedSession) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if err := guardRunWork(ctx, g.SessionID(), method, params); err != nil {
		return nil, err
	}
	return g.engineSession.Call(ctx, method, params)
}

func (g guardedSession) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if err := guardRunWork(ctx, g.SessionID(), method, params); err != nil {
		return nil, 0, err
	}
	return g.engineSession.CallAt(ctx, method, params)
}

func (g guardedSession) PromptAt(ctx context.Context, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	if err := guardRunWork(ctx, g.SessionID(), marotte.MethodPrompt, params); err != nil {
		return nil, 0, err
	}
	return g.engineSession.PromptAt(ctx, params, commit)
}

func (g guardedSession) Notify(ctx context.Context, method string, params any) error {
	if err := guardRunWork(ctx, g.SessionID(), method, params); err != nil {
		return err
	}
	return g.engineSession.Notify(ctx, method, params)
}
