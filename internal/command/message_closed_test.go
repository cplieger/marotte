package command

// A message from outside marotte's page into a chat the user closed: refused 410 session_closed, nothing placed,
// prompted or reopened. The same chat with its tab open takes the message.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

var grelotCaller = &marotte.Caller{Label: "grelot"}

// chatTabOf is the open chat tab for ref, if any.
func chatTabOf(st *tabs.Store, ref marotte.ChatID) (marotte.TabSubject, bool) {
	open, _ := st.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindChat && s.Ref == string(ref) {
			return s, true
		}
	}
	return marotte.TabSubject{}, false
}

// messageFixture is the caller fixture over a real coordinator and tab store, with c1 closed and listed after c2.
type messageFixture struct {
	*callerFixture
	mem *Membership
	st  *tabs.Store
	bus *tabBus
}

func newMessageFixture(t *testing.T) *messageFixture {
	t.Helper()
	f := newCallerFixture(t)
	mem, st, bus := newTabbedMembership(t, f.store)
	seedNamed(t, f.store, "c2", "Other")
	closeListed(t, mem, "c1")
	closeListed(t, mem, "c2")
	f.door.inner = mem
	return &messageFixture{callerFixture: f, mem: mem, st: st, bus: bus}
}

// tabState is what a refused message must leave as it was.
type tabState struct {
	recent  []string
	version uint64
	frames  int
}

func (m *messageFixture) tabState(t *testing.T) tabState {
	t.Helper()
	_, version := m.st.List()
	return tabState{recent: refsOf(m.st.Recent()), version: version, frames: len(m.bus.frames(t))}
}

// leftClosed fails t unless c1 is still closed with the tabs as before held them, and nothing reached its run order.
func (m *messageFixture) leftClosed(t *testing.T, before tabState) {
	t.Helper()
	if n := len(m.opens.prompts()); n != 0 {
		t.Errorf("turn opens = %d, want none for a closed chat", n)
	}
	if n := len(m.queue.queued()); n != 0 {
		t.Errorf("rows queued = %d, want none for a closed chat", n)
	}
	if n := len(m.steers.froms); n != 0 {
		t.Errorf("steers = %d, want none for a closed chat", n)
	}
	if _, ok := chatTabOf(m.st, "c1"); ok {
		t.Error("c1 has a tab after the message, want it left closed")
	}
	if after := m.tabState(t); after.version != before.version || after.frames != before.frames ||
		!slices.Equal(after.recent, before.recent) {
		t.Errorf("tabs after = %+v, want %+v unchanged", after, before)
	}
}

func TestNotify_IntoAChatTheUserClosedIsSessionClosedAndChangesNothing(t *testing.T) {
	m := newMessageFixture(t)
	before := m.tabState(t)

	_, err := m.d.Notify(t.Context(), "c1", notifyText, grelotCaller, NotifyOptions{})
	m.join.join()

	if statusOf(err) != http.StatusGone || reasonOf(err) != reasonSessionClosed {
		t.Errorf("notify = %d %q (%v), want 410 %s", statusOf(err), reasonOf(err), err, reasonSessionClosed)
	}
	if got := ClassifyNotifyError(err); got != NotifySessionClosed {
		t.Errorf("class = %v, want NotifySessionClosed", got)
	}
	m.leftClosed(t, before)
}

func TestNotify_IntoAChatWithItsTabOpenPrompts(t *testing.T) {
	m := newMessageFixture(t)
	openChatTab(t, m.mem, "c1", "")

	notifyInto(t, m.callerFixture)

	if n := len(m.opens.prompts()); n != 1 {
		t.Errorf("turn opens = %d, want the message delivered", n)
	}
}

func TestNotify_IntoAGoneChatIsUnknownAndOpensNothing(t *testing.T) {
	m := newMessageFixture(t)
	m.opens.admitFirst = AdmissionChatGone
	mark := len(m.bus.frames(t))

	_, err := m.d.Notify(t.Context(), "c-gone", notifyText, grelotCaller, NotifyOptions{})

	if got := ClassifyNotifyError(err); got != NotifyUnknownChat {
		t.Errorf("class = %v (%v), want NotifyUnknownChat", got, err)
	}
	if n := len(m.bus.frames(t)) - mark; n != 0 {
		t.Errorf("emitted %d frames, want 0", n)
	}
}

// An API caller's prompt, steer or queued prompt through /api/command meets the notify route's refusal.
func TestCommand_FromAnAPICallerIntoAChatTheUserClosedIsSessionClosed(t *testing.T) {
	for _, cmdType := range []marotte.CommandType{marotte.CmdPrompt, marotte.CmdSteer, marotte.CmdQueuePrompt} {
		t.Run(string(cmdType), func(t *testing.T) {
			m := newMessageFixture(t)
			before := m.tabState(t)

			w := m.send(t, cmdType, "Agent Mail", nil, map[string]string{"text": "1 new message", "message_id": "m-1"})

			var body errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("answer %d %q is not the error envelope: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != http.StatusGone || body.Reason != reasonSessionClosed || body.Error != errSessionClosed.Error() {
				t.Errorf("answer = %d %+v, want 410 reason %s", w.Code, body, reasonSessionClosed)
			}
			m.leftClosed(t, before)
		})
	}
}

// The user's own send from the page into a closed regular chat is not a message from outside: it is taken.
func TestPrompt_FromThePageIntoAClosedChatIsTaken(t *testing.T) {
	m := newMessageFixture(t)

	m.post(t, marotte.CmdPrompt, "", pageHeaders)

	if n := len(m.opens.prompts()); n != 1 {
		t.Errorf("turn opens = %d, want the page's prompt delivered", n)
	}
}
