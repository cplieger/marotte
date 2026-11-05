package settings

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
)

// TestLoggingReaders_StayQuietWhenTheirCtxEnded pins that a reader whose ctx ended logs nothing: the
// error is the caller's stop, not the document's. The live-ctx case over an unparseable document
// proves each reader still logs a real read failure. Serial: slog's default is process-global.
func TestLoggingReaders_StayQuietWhenTheirCtxEnded(t *testing.T) {
	readers := []struct {
		name string
		read func(ctx context.Context, dir string)
	}{
		{"Field", func(ctx context.Context, dir string) { Field[string](ctx, dir, KeyMemoryMode) }},
		{"fieldOr", func(ctx context.Context, dir string) { fieldOr(ctx, dir, KeyMemoryMode, "") }},
		{"FieldInto", func(ctx context.Context, dir string) {
			var out string
			FieldInto(ctx, dir, KeyMemoryMode, &out)
		}},
		{"RetentionEnabled", func(ctx context.Context, dir string) { RetentionEnabled(ctx, dir) }},
	}
	ctxs := []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		wantLog int
	}{
		{"a cancelled ctx", func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx
		}, 0},
		{"an expired ctx", func(t *testing.T) context.Context {
			ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
			t.Cleanup(cancel)
			return ctx
		}, 0},
		{"a live ctx", func(t *testing.T) context.Context { return t.Context() }, 1},
	}
	for _, r := range readers {
		for _, c := range ctxs {
			t.Run(r.name+" with "+c.name, func(t *testing.T) {
				dir := t.TempDir()
				resetCache(t, dir)
				writeSettings(t, dir, []byte(`{`))

				rec := capture.Default(t)
				r.read(c.ctx(t), dir)

				if n := rec.Len(); n != c.wantLog {
					t.Errorf("%s over an unparseable config.json with %s logged %d lines, want %d; messages: %v",
						r.name, c.name, n, c.wantLog, rec.Messages())
				}
			})
		}
	}
}
