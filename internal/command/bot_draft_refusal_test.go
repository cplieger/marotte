package command

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// countingRun is a run order that counts every submission and answers each started.
type countingRun struct {
	runQueue
	n  int
	mu sync.Mutex
}

func (q *countingRun) Submit(context.Context, marotte.ChatID, uint64, *Submission) (SubmitAnswer, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.n++
	return SubmitAnswer{As: SubmitStarted}, nil
}

func (q *countingRun) submitted() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// draftRoles is the prompt roles over a store holding c-bot as a draft and c-started as a started Bot, with the run
// order counting what reaches it.
func draftRoles(t *testing.T) (*promptRoles, *countingRun, chatStore) {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	seedBot(t, store, "c-started", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	roles := promptRolesOf(newTestHost(t, store))
	run := &countingRun{}
	roles.runq = run
	return roles, run, store
}

func wantBotDraft(t *testing.T, err error, run *countingRun) {
	t.Helper()
	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonBotDraft {
		t.Errorf("status %d reason %q, want 409 %s", statusOf(err), reasonOf(err), reasonBotDraft)
	}
	if got := run.submitted(); got != 0 {
		t.Errorf("%d submission(s) reached the run order, want none for a draft", got)
	}
}

func TestDraftBot_APromptIsRefused(t *testing.T) {
	roles, run, _ := draftRoles(t)

	_, err := cmdPrompt(t.Context(), roles, promptReq(t, "c-bot", "hello"))

	wantBotDraft(t, err, run)
}

func TestDraftBot_ASteerIsRefused(t *testing.T) {
	roles, run, _ := draftRoles(t)

	_, err := cmdSteer(t.Context(), roles, steerReq(t, "c-bot", "and this", "m-steer"))

	wantBotDraft(t, err, run)
}

func TestDraftBot_AQueuedPromptIsRefused(t *testing.T) {
	roles, run, store := draftRoles(t)
	req := queueReq(t, "later", "m-q")
	req.ChatID = "c-bot"

	_, err := cmdQueuePrompt(t.Context(), run, roles.admission, nil, store, req)

	wantBotDraft(t, err, run)
}

func TestDraftBot_ANotifyIsRefused(t *testing.T) {
	roles, run, _ := draftRoles(t)
	d := New()
	d.prompts = roles

	err := d.Notify(t.Context(), "c-bot", "1 new message", &marotte.Caller{Label: "grelot"})

	wantBotDraft(t, err, run)
	if got := ClassifyNotifyError(err); got != NotifyRetryLater {
		t.Errorf("notify refusal class = %v, want NotifyRetryLater as for any other 409", got)
	}
}

func TestDraftBot_AStartedBotTakesAPrompt(t *testing.T) {
	roles, run, _ := draftRoles(t)

	if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c-started", "hello")); err != nil {
		t.Fatalf("prompt into a started Bot = %v", err)
	}

	if got := run.submitted(); got != 1 {
		t.Errorf("submissions = %d, want 1", got)
	}
}

// unreadableLookups answers every Lookup as a record that exists but cannot be read.
type unreadableLookups struct{ *testsupport.InMemoryChatStore }

func (unreadableLookups) Lookup(context.Context, marotte.ChatID) (*marotte.Chat, bool, error) {
	return nil, false, errors.New("chat.json: unexpected end of JSON input")
}

func TestDraftBot_APromptIntoAnUnreadableRecordIsRefusedAsUnavailable(t *testing.T) {
	store := unreadableLookups{testsupport.NewInMemoryChatStore()}
	seedBot(t, store.InMemoryChatStore, "c-bot", "New Bot", &marotte.Bot{})
	roles := promptRolesOf(newTestHost(t, store))
	run := &countingRun{}
	roles.runq = run

	_, err := cmdPrompt(t.Context(), roles, promptReq(t, "c-bot", "hello"))

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("status %d (%v), want 503", statusOf(err), err)
	}
	if got := run.submitted(); got != 0 {
		t.Errorf("%d submission(s) reached the run order, want none", got)
	}
}

func TestDraftBot_AForkIsRefusedBeforeAnySession(t *testing.T) {
	cases := []struct {
		name    string
		message string
	}{
		{name: "a head fork", message: ""},
		{name: "a branch fork", message: "m-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
			br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
			host := newForkHost(store, br, "c-bot")
			mem, st, _ := newTabbedMembership(t, store)
			req := forkReqAt(t, "c-bot", tc.message)

			_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), mem, req)

			if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonBotDraft {
				t.Errorf("status %d reason %q, want 409 %s", statusOf(err), reasonOf(err), reasonBotDraft)
			}
			if len(host.openChatIDs) != 0 || br.callCount != 0 {
				t.Errorf("opens %v and %d session call(s), want none", host.openChatIDs, br.callCount)
			}
			if got := storedIDs(t, store); len(got) != 1 {
				t.Errorf("the store holds %v, want only the draft", got)
			}
			if open, _ := st.List(); len(open) != 0 {
				t.Errorf("the set holds %d tabs, want none opened", len(open))
			}
		})
	}
}
