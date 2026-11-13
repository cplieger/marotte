package agent

import "github.com/cplieger/marotte/internal/marotte"

// pushStub answers every pushService method a test's fake does not observe; a fake embeds it and overrides the rest.
type pushStub struct{}

func (pushStub) HasSubscribers() bool                                        { return true }
func (pushStub) SetPreferences(map[marotte.PushKind]bool)                    {}
func (pushStub) Preferences() map[marotte.PushKind]bool                      { return nil }
func (pushStub) Close()                                                      {}
func (pushStub) SetFrames(func(marotte.ChatID, marotte.NotificationPayload)) {}
func (pushStub) View(string, marotte.PageView)                               {}
func (pushStub) Shown(string, string)                                        {}
func (pushStub) Declined(string, string)                                     {}
func (pushStub) Seen(marotte.TabKind, string, uint64)                        {}
func (pushStub) TabClosed(marotte.TabKind, string)                           {}
func (pushStub) Settled(marotte.PushSubject, string)                         {}
