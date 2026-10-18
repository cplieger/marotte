package marotte

import "context"

type writeCommitKey struct{}

// WithWriteCommit returns ctx carrying commit for the one request a caller makes with it, as net/http/httptrace
// carries its hooks. The engine runs commit under its write lock immediately before the request's frame: an error
// refuses the write and is the call's answer, and a nil lets the frame out ahead of every frame written after commit
// returned.
func WithWriteCommit(ctx context.Context, commit func() error) context.Context {
	return context.WithValue(ctx, writeCommitKey{}, commit)
}

// WriteCommit returns the commit ctx carries, nil for none.
func WriteCommit(ctx context.Context) func() error {
	commit, _ := ctx.Value(writeCommitKey{}).(func() error)
	return commit
}
