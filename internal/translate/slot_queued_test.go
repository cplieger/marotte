package translate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const blockingSession = "sess_fe657706-3b4c-4a4f-8452-7123574bac1c"

func queuedFrame(t *testing.T, active string) []byte {
	t.Helper()
	return steerFrame(t, "queued", map[string]any{"activeSessionId": active})
}

func wireTurnStart(t *testing.T) []byte {
	t.Helper()
	return steerFrame(t, "turn_start", map[string]any{"turnStart": true})
}

func eventsOf(events []marotte.ServerEvent, typ marotte.EventType) []marotte.ServerEvent {
	var out []marotte.ServerEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// The recorded shape of turn n=833: KAS queued the prompt behind another chat's turn, which held its one autonomous
// slot for 31 minutes. The chat says what it waits for, naming that chat, and turn_start ends the wait.
func TestSessionInfo_AQueuedPromptNamesTheChatHoldingTheSlot(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	if _, err := store.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Overnight autonomous build"
		return true
	}); err != nil {
		t.Fatalf("seed c2: %v", err)
	}
	deps.sessionChats = map[string]marotte.ChatID{blockingSession: "c2"}
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", queuedFrame(t, blockingSession), FrameAttribution{})

	labels := eventsOf(*events, marotte.EventWorkingLabel)
	if len(labels) != 1 || labels[0].ChatID != "c1" {
		t.Fatalf("working_label events = %+v, want one for c1", labels)
	}
	if got := labels[0].Payload.(marotte.WorkingLabelPayload).Label; got != `Waiting for "Overnight autonomous build"` {
		t.Errorf("working label = %q, want the blocking chat named", got)
	}
	notices := eventsOf(*events, marotte.EventSystemNotice)
	if len(notices) != 1 || notices[0].ChatID != "c1" {
		t.Fatalf("system_notice events = %+v, want one for c1", notices)
	}
	n := notices[0].Payload.(marotte.SystemNoticePayload)
	if n.Level != marotte.NoticeInfo || n.ChatName != "A" ||
		n.Message != `Waiting for "Overnight autonomous build" to finish: kiro-cli runs one agent turn at a time, so this turn starts when that one ends.` {
		t.Errorf("notice = %+v, want an info notice on c1 naming the blocking chat", n)
	}

	*events = nil
	tr.HandleSessionInfoUpdate(t.Context(), "c1", wireTurnStart(t), FrameAttribution{})
	if labels := eventsOf(*events, marotte.EventWorkingLabel); len(labels) != 1 ||
		labels[0].Payload.(marotte.WorkingLabelPayload).Label != marotte.WorkingLabelThinking {
		t.Errorf("working_label after turn_start = %+v, want the wait replaced by %q", labels, marotte.WorkingLabelThinking)
	}
	*events = nil
	tr.HandleSessionInfoUpdate(t.Context(), "c1", wireTurnStart(t), FrameAttribution{})
	if labels := eventsOf(*events, marotte.EventWorkingLabel); len(labels) != 0 {
		t.Errorf("working_label after a second turn_start = %+v, want none: no wait was shown", labels)
	}
}

// A slot held by a session no chat owns (a workflow step, a run's host) is named generically.
func TestSessionInfo_AQueuedPromptBehindAnUnknownSession(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", queuedFrame(t, "sess_unknown"), FrameAttribution{})

	labels := eventsOf(*events, marotte.EventWorkingLabel)
	if len(labels) != 1 || labels[0].Payload.(marotte.WorkingLabelPayload).Label != "Waiting for another kiro-cli session" {
		t.Errorf("working_label events = %+v, want the wait named generically", labels)
	}
}

// A step's own queued frame is the run's, never the launching chat's.
func TestSessionInfo_AStepsQueuedFrameReachesNoChat(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.sessionChats = map[string]marotte.ChatID{blockingSession: "c1"}
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", queuedFrame(t, blockingSession),
		FrameAttribution{Step: true, SessionID: testStep, RunID: testWfRun, NodePath: "x"})

	if got := len(eventsOf(*events, marotte.EventWorkingLabel)) + len(eventsOf(*events, marotte.EventSystemNotice)); got != 0 {
		t.Errorf("events = %+v, want none for a step's frame", *events)
	}
}

// KAS 2.32.0 never queues a vibe chat's execution (it runs as chat-agent), so a queued frame on one is logged as the
// sign KAS routes vibe as default-v2; a default-v2 chat queuing is the expected case and logs nothing.
func TestSessionInfo_AQueuedVibeChatIsLogged(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		warn       bool
	}{
		{name: "vibe", mode: "vibe", warn: true},
		{name: "default-v2", mode: "default-v2", warn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			t.Cleanup(captureSlog(&buf))
			deps, _, store := depsWithStore(t, "c1")
			if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.CurrentModeID = tc.mode
				return true
			}); err != nil {
				t.Fatalf("seed c1: %v", err)
			}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", queuedFrame(t, blockingSession), FrameAttribution{})

			got := buf.String()
			logged := strings.Contains(got, "level=WARN") && strings.Contains(got, "chat in mode vibe was queued for the autonomous slot")
			if logged != tc.warn {
				t.Errorf("WARN logged = %v, want %v:\n%s", logged, tc.warn, got)
			}
			if tc.warn && (!strings.Contains(got, "chat_id=c1") || !strings.Contains(got, "mode=vibe") ||
				!strings.Contains(got, "active_session_id="+blockingSession)) {
				t.Errorf("WARN = %s, want the chat id, its mode and the blocking session", got)
			}
		})
	}
}
