package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
)

// EngineSource is where the agent engine's binary and environment come from, as the composition root resolves them.
type EngineSource interface {
	// ChatPath is the kiro-cli-chat sidecar to spawn, "" while none is available.
	ChatPath() string
	// Installed closes once a managed install is active; nil means an unmanaged runtime that starts at once.
	Installed() <-chan struct{}
	// Env is appended to the engine's environment, so the active install directory leads PATH.
	Env() []string
	// EnvAllow re-permits credential-shaped names the engine's inherit screen would drop.
	EnvAllow() map[string]struct{}
}

// noEngine is the source of a runtime with no kiro-cli: no path and no install to wait for, so the owner never starts.
type noEngine struct{}

func (noEngine) ChatPath() string              { return "" }
func (noEngine) Installed() <-chan struct{}    { return nil }
func (noEngine) Env() []string                 { return nil }
func (noEngine) EnvAllow() map[string]struct{} { return nil }

// engineSpawner builds the engine for one generation. An EngineSource that implements it replaces the kiro-cli
// process, which is how the package's tests run a fake engine.
type engineSpawner interface {
	spawnEngine(gen uint64, path string, opts ...bridge.Option) engineHandle
}

// engineHandle is the one engine process, as the owner drives it (*bridge.Engine through bridgeEngine).
type engineHandle interface {
	Start(ctx context.Context, o *marotte.EngineOpts) error
	Stop()
	Done() <-chan struct{}
	Exit() bridge.ExitInfo
	EngineNotifCh() <-chan marotte.Notification
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
	Notify(ctx context.Context, method string, params any) error
	Respond(ctx context.Context, id int64, result any, err error) error
	// NewSession and LoadSession run attach once the session's lane exists and before the door writes, so a
	// consumer reads every frame KAS sends for it.
	NewSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error)
	LoadSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error)
	StartLive() (ignoreFiles []string, terminalTimeoutMs int)
	RefreshContentCollection(ctx context.Context) (bool, error)
	KASLogDir() string
}

// bridgeEngine adapts *bridge.Engine, whose doors hand back the concrete *bridge.Session.
type bridgeEngine struct{ *bridge.Engine }

var (
	_ engineHandle  = bridgeEngine{}
	_ engineSession = (*bridge.Session)(nil)
)

func (b bridgeEngine) NewSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	s, err := b.Engine.NewSession(ctx, o, sessionAttach(attach))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (b bridgeEngine) LoadSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	s, err := b.Engine.LoadSession(ctx, o, sessionAttach(attach))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func sessionAttach(attach func(engineSession)) func(*bridge.Session) {
	if attach == nil {
		return nil
	}
	return func(s *bridge.Session) { attach(s) }
}
