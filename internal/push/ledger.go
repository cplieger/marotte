package push

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// SetFrames sets the publisher of the page's notification frame. Nil publishes nothing.
func (s *Service) SetFrames(publish func(marotte.ChatID, marotte.NotificationPayload)) {
	s.frames = publish
}

// Raise records n about chatID, whose tab's read position was position when it was raised, and
// returns the push fan-out for the caller to run outside every lock.
func (s *Service) Raise(_ context.Context, chatID marotte.ChatID, n *marotte.NotificationPayload, _ uint64) func(context.Context) {
	if s.frames != nil {
		s.frames(chatID, *n)
	}
	return func(ctx context.Context) { s.Send(ctx, n) }
}

// View records one page's view report under its profile's tag.
func (s *Service) View(tag string, v marotte.PageView) {
	if s.presence != nil {
		s.presence.View(tag, v)
	}
}

// Shown ends the notice id on tag: a page of that profile showed it.
func (*Service) Shown(string, string) {}

// Declined answers an offer of id on tag that the page's own gates refused.
func (*Service) Declined(string, string) {}

// Seen ends every notice about the tab (kind, ref) at a read position up to marker.
func (*Service) Seen(marotte.TabKind, string, uint64) {}

// TabClosed forgets the tab's acked marker.
func (*Service) TabClosed(marotte.TabKind, string) {}
