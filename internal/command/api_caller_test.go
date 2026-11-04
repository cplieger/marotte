package command

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// openRecorder is scriptedAdmission with every TurnOpen it was handed kept.
type openRecorder struct {
	*scriptedAdmission
	opens []TurnOpen
	mu    sync.Mutex
}

func (o *openRecorder) OpenTurn(ctx context.Context, chatID marotte.ChatID, open TurnOpen) (string, error) {
	o.mu.Lock()
	o.opens = append(o.opens, open)
	o.mu.Unlock()
	return o.scriptedAdmission.OpenTurn(ctx, chatID, open)
}

func (o *openRecorder) prompts() []*marotte.EntryPrompt {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*marotte.EntryPrompt, 0, len(o.opens))
	for i := range o.opens {
		out = append(out, o.opens[i].Prompt)
	}
	return out
}

// callerFixture serves prompt, steer and queue_prompt for chat c1, whose composer holds a draft and a staged file,
// through the real dispatcher. The prompt's session open fails at once, so no turn outlives the test.
type callerFixture struct {
	d      *Dispatcher
	opens  *openRecorder
	store  *testsupport.InMemoryChatStore
	steers *stubSteerQueue
	queue  *fakeQueue
	join   *promptJoin
}

func newCallerFixture(t *testing.T) *callerFixture {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Release notes"
		c.Draft = "half a thought"
		c.Attachments = []string{"notes.md"}
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := &callRecorder{}
	opens := &openRecorder{scriptedAdmission: &scriptedAdmission{rec: rec}}
	host := &admissionHost{
		hostDouble: newTestHost(t, store),
		admission:  opens.scriptedAdmission,
		openErr:    context.Canceled,
		rec:        rec,
	}
	steers := newStubSteerQueue()
	roles := steerRolesOf(newBenchDeps(), NewSteerLedger(), steers)
	roles.chats = host
	roles.bridges = host
	roles.bus = host
	roles.admission = opens
	roles.turnOutcome = opens
	join := &promptJoin{}
	roles.lifecycle = join
	queue := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}

	d := New()
	d.status = &fakeChatStatus{}
	d.Register(marotte.CmdPrompt, func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return cmdPrompt(ctx, roles, cmd)
	})
	// benchDeps holds a prompt turn for the steer to join.
	steerRoles := steerRolesOf(newBenchDeps(), NewSteerLedger(), steers)
	d.Register(marotte.CmdSteer, func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return cmdSteer(ctx, steerRoles, cmd)
	})
	d.Register(marotte.CmdQueuePrompt, func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return cmdQueuePrompt(ctx, queue, cmd)
	})
	return &callerFixture{d: d, opens: opens, store: store, steers: steers, queue: queue, join: join}
}

// post sends a command the way the poller does (no fetch metadata, no Origin), with headers adding a page's.
func (f *callerFixture) post(t *testing.T, cmdType marotte.CommandType, label string, headers map[string]string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"text": "Agent Mail: 1 new message", "message_id": "m-1"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"type": cmdType, "chat_id": "c1", "caller": label, "payload": json.RawMessage(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.d.ServeHTTP(w, req)
	f.join.join()
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", cmdType, w.Code, w.Body.String())
	}
}

var pageHeaders = map[string]string{"Sec-Fetch-Site": "same-origin"}

func TestPrompt_FromAnAPICallerIsAttributedAndLeavesTheComposer(t *testing.T) {
	f := newCallerFixture(t)
	f.post(t, marotte.CmdPrompt, "Agent Mail", nil)

	prompts := f.opens.prompts()
	if len(prompts) != 1 || prompts[0] == nil {
		t.Fatalf("turn opens = %v, want one prompt", prompts)
	}
	if got := prompts[0].Caller; got == nil || got.Label != "Agent Mail" {
		t.Errorf("turn_open prompt caller = %+v, want Agent Mail", got)
	}
	c, _ := f.store.Get(t.Context(), "c1")
	if c.Draft != "half a thought" || !slices.Equal(c.Attachments, []string{"notes.md"}) {
		t.Errorf("composer after an API prompt = draft %q, files %v; want the user's draft and staged file kept",
			c.Draft, c.Attachments)
	}
}

// A page's own prompt is the user's: its label is ignored, so a page cannot word its prompt as another program's,
// and the composer settles as the sent draft.
func TestPrompt_FromThePageIsTheUsersWhateverLabelItSends(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"https page":      pageHeaders,
		"plain-HTTP page": {"Origin": "http://nas.lan:9847"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCallerFixture(t)
			f.post(t, marotte.CmdPrompt, "Agent Mail", headers)

			prompts := f.opens.prompts()
			if len(prompts) != 1 || prompts[0] == nil {
				t.Fatalf("turn opens = %v, want one prompt", prompts)
			}
			if got := prompts[0].Caller; got != nil {
				t.Errorf("turn_open prompt caller = %+v, want none on a page's prompt", got)
			}
			c, _ := f.store.Get(t.Context(), "c1")
			if c.Draft != "" || len(c.Attachments) != 0 {
				t.Errorf("composer after the user's prompt = draft %q, files %v; want it cleared", c.Draft, c.Attachments)
			}
		})
	}
}

func TestSteer_FromAnAPICallerIsAttributed(t *testing.T) {
	f := newCallerFixture(t)
	f.post(t, marotte.CmdSteer, "Agent Mail", nil)
	f.post(t, marotte.CmdSteer, "Agent Mail", pageHeaders)

	if len(f.steers.froms) != 2 {
		t.Fatalf("routed %d steers, want 2", len(f.steers.froms))
	}
	if got := f.steers.froms[0]; got == nil || got.Label != "Agent Mail" {
		t.Errorf("API steer caller = %+v, want Agent Mail", got)
	}
	if got := f.steers.froms[1]; got != nil {
		t.Errorf("page steer caller = %+v, want none", got)
	}
}

func TestQueuePrompt_FromAnAPICallerIsAttributedThroughItsDrain(t *testing.T) {
	f := newCallerFixture(t)
	f.post(t, marotte.CmdQueuePrompt, "", nil)

	rows := f.queue.chat.QueuedPrompts
	if len(rows) != 1 {
		t.Fatalf("queued %d rows, want 1", len(rows))
	}
	if got := rows[0].Caller; got == nil || got.Label != "" {
		t.Fatalf("queued row caller = %+v, want an API caller with no label", got)
	}

	roles := promptRolesOf(newBenchDeps())
	opens := &openRecorder{scriptedAdmission: &scriptedAdmission{rec: &callRecorder{}}}
	roles.turnOutcome = opens
	roles.followups = f.queue
	roles.lifecycle = f.join
	roles.bridges = &admissionHost{hostDouble: newBenchDeps(), openErr: context.Canceled, rec: &callRecorder{}}
	sendNextUserRow(t.Context(), roles, testReservation("c1", true), TurnFence{}, nil)
	f.join.join()

	prompts := opens.prompts()
	if len(prompts) != 1 || prompts[0] == nil || prompts[0].Caller == nil {
		t.Errorf("drained turn opens = %v, want the queued row's prompt attributed to its API caller", prompts)
	}
}

// The label is untrusted text drawn on one line of the turn header.
func TestCommandCaller_LabelIsOneShortSafeLine(t *testing.T) {
	cases := []struct {
		name, label, want string
	}{
		{name: "a plain name is kept", label: "Agent Mail", want: "Agent Mail"},
		{name: "no label is an unnamed caller", label: "", want: ""},
		{name: "edge spaces go", label: "  Agent Mail  ", want: "Agent Mail"},
		{name: "a line break cannot start a second line", label: "Agent\nMail\r", want: "Agent Mail"},
		{name: "a terminal escape is blanked", label: "Agent\x1b[31mMail", want: "Agent [31mMail"},
		{name: "a bidi override is blanked", label: "Agent\u202eliaM", want: "Agent liaM"},
		{name: "blanked breaks leave one space", label: "Agent\n\nMail", want: "Agent Mail"},
		{
			name:  "leading blanks do not spend the cap",
			label: strings.Repeat(" ", 64) + "Agent Mail",
			want:  "Agent Mail",
		},
		{
			name:  "the label is cut at 64 runes",
			label: strings.Repeat("é", 70),
			want:  strings.Repeat("é", 64),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/command", nil)
			got := commandCaller(r, tc.label)
			if got == nil || got.Label != tc.want {
				t.Errorf("commandCaller(%q) = %+v, want label %q", tc.label, got, tc.want)
			}
		})
	}
}
