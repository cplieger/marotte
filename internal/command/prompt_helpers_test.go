package command

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// promptViaCallAt is a fake's PromptAt over its own scripted CallAt, the commit run first as the engine runs it.
func promptViaCallAt(ctx context.Context, b bridgeCaller, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	if commit != nil {
		if err := commit(); err != nil {
			return nil, 0, err
		}
	}
	return b.CallAt(ctx, marotte.MethodPrompt, params)
}
