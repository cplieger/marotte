package translate

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// infoKindModelRouted is KAS's Auto routing notice, live and replayed. Its only field is a flat
// message (legacyFields returns {} for it), so it dispatches on the kind STRING.
const infoKindModelRouted = "model_routed"

// routeNoticeDefault is KAS's default notice (nM.clientNotificationMessage), worded as the switch
// happens; the transcript row is read after it, so only this exact text gets the past tense and a
// tuned message passes through unchanged.
const (
	routeNoticeDefault = "Found a better model for this task. Switching to it now."
	routeNoticePast    = "Found a better model for this task. Switched to it."
)

// modelRoutedPayload is the decode door for a model_routed message, shared by the live and replay
// paths so the two payloads are byte-identical.
func modelRoutedPayload(message string) marotte.EntryModelRouted {
	if message == routeNoticeDefault {
		message = routeNoticePast
	}
	return marotte.EntryModelRouted{Message: displayText(message)}
}

func (t *Translator) handleModelRouted(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	p := modelRoutedPayload(k.Message)
	t.appendLaneless(ctx, chatID, marotte.EntryKindModelRouted, "", p,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.ModelRouted(ctx, p)
		})
}
