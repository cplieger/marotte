package marotte

import "context"

type noEngineWaitKey struct{}

// WithoutEngineWait marks ctx as a read that answers at once, wrapping ErrBridgeExited, while no agent engine is
// ready, instead of waiting for one. An HTTP GET carries it: a read held open until its budget ends fills the
// browser's per-origin connection pool and stalls every other request behind it.
func WithoutEngineWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, noEngineWaitKey{}, true)
}

// WithEngineWait lifts WithoutEngineWait: work detached from a request outlives it, so it waits for the engine.
func WithEngineWait(ctx context.Context) context.Context {
	if !EngineWaitRefused(ctx) {
		return ctx
	}
	return context.WithValue(ctx, noEngineWaitKey{}, false)
}

// EngineWaitRefused reports whether ctx was marked by WithoutEngineWait.
func EngineWaitRefused(ctx context.Context) bool {
	refused, _ := ctx.Value(noEngineWaitKey{}).(bool)
	return refused
}
