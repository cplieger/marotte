package agent

// History lists a Bot with no session KAS lists by a row of its own record, so a closed Bot stays reachable however
// long ago it closed.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// seedClosedBot writes a started, closed Bot named name whose chain holds sessions.
func seedClosedBot(t *testing.T, cs *testChatStore, id marotte.ChatID, name string, sessions ...string) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser, c.CurrentModeID, c.Model = name, true, marotte.BotRoleID, "model-a"
		c.Bot = &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1_700_000_000_000, ClosedAt: 1_700_000_100_000}
		for _, sid := range sessions {
			c.RecordSession(sid)
		}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot %s: %v", id, err)
	}
}

func headerOf(t *testing.T, cs *testChatStore, id string) marotte.ChatHeader {
	t.Helper()
	for _, h := range cs.List(t.Context()) {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("no header for %s", id)
	return marotte.ChatHeader{}
}

// botHistory seeds a Bot whose kickoff was refused (no session), a Bot whose session KAS no longer lists, a Bot with
// a listed session, a regular chat with no session and a regular chat with a listed one.
func botHistory(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	seedClosedBot(t, cs, "c-bot-empty", "No kickoff")
	seedClosedBot(t, cs, "c-bot-unlisted", "Forgotten by KAS", "sess_gone")
	seedClosedBot(t, cs, "c-bot-listed", "Listed", "sess_bot")
	seedChain(t, cs, "c-plain-empty")
	seedChain(t, cs, "c-plain", "sess_plain")
	return h, cs, br
}

func listSessions(t *testing.T, h *Runtime) marotte.SessionListResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleSessionList(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	var got marotte.SessionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode reply %q: %v", rec.Body.String(), err)
	}
	return got
}

func rowsByChat(rows []marotte.ResumableSession) map[string][]marotte.ResumableSession {
	out := map[string][]marotte.ResumableSession{}
	for _, r := range rows {
		out[r.ChatID] = append(out[r.ChatID], r)
	}
	return out
}

// recordRow is the row History owes a Bot with no listed session, built from its stored header.
func recordRow(h *marotte.ChatHeader) marotte.ResumableSession {
	return marotte.ResumableSession{ChatID: h.ID, Title: h.Name, UpdatedAt: h.UpdatedAt, CreatedAt: h.CreatedAt, Model: h.Model, Bot: true}
}

func wantNewestFirst(t *testing.T, rows []marotte.ResumableSession) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		if rows[i].UpdatedAt > rows[i-1].UpdatedAt {
			t.Errorf("rows %d and %d are out of order (%d before %d), want newest first", i-1, i, rows[i-1].UpdatedAt, rows[i].UpdatedAt)
		}
	}
}

func TestHandleSessionList_ListsEachBotWithNoListedSessionByItsRecord(t *testing.T) {
	t.Parallel()
	h, cs, br := botHistory(t)
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionList: json.RawMessage(`{"sessions":[
			{"sessionId":"sess_plain","title":"Old chat","updatedAt":"2020-01-01T00:00:00.000Z","_meta":{"kiro":{"status":"idle"}}},
			{"sessionId":"sess_bot","title":"Listed","updatedAt":"2099-01-01T00:00:00.000Z","_meta":{"kiro":{"status":"idle"}}}]}`),
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
	}

	got := listSessions(t, h)

	if got.SessionsState != marotte.ReadReady {
		t.Errorf("sessions_state = %q, want ready", got.SessionsState)
	}
	by := rowsByChat(got.Sessions)
	for _, id := range []string{"c-bot-empty", "c-bot-unlisted"} {
		hdr := headerOf(t, cs, id)
		if rows := by[id]; len(rows) != 1 || rows[0] != recordRow(&hdr) {
			t.Errorf("%s rows = %+v, want its one record row %+v", id, rows, recordRow(&hdr))
		}
	}
	if rows := by["c-bot-listed"]; len(rows) != 1 || rows[0].SessionID != "sess_bot" || !rows[0].Bot {
		t.Errorf("c-bot-listed rows = %+v, want its KAS row alone, labelled a Bot", rows)
	}
	if rows := by["c-plain-empty"]; len(rows) != 0 {
		t.Errorf("a regular chat with no session got rows %+v, want none", rows)
	}
	if rows := by["c-plain"]; len(rows) != 1 || rows[0].SessionID != "sess_plain" || rows[0].Bot {
		t.Errorf("c-plain rows = %+v, want its KAS row, unlabelled", rows)
	}
	if len(got.Sessions) != 4 {
		t.Errorf("rows = %+v, want 4", got.Sessions)
	}
	wantNewestFirst(t, got.Sessions)
}

// The rows come from the records alone, read beside no tab set and no Recently closed list, so a Bot whose entry
// expired or was pushed out by later closes is still listed.
func TestHandleSessionList_ListsTheBotRowsWhileKASCannotList(t *testing.T) {
	t.Parallel()
	h, cs, br := botHistory(t)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`)}
	br.callErrs = map[string]error{marotte.MethodSessionList: errors.New("kas gone")}

	got := listSessions(t, h)

	if got.SessionsState != marotte.ReadUnavailable {
		t.Errorf("sessions_state = %q, want unavailable", got.SessionsState)
	}
	by := rowsByChat(got.Sessions)
	for _, id := range []string{"c-bot-empty", "c-bot-unlisted", "c-bot-listed"} {
		hdr := headerOf(t, cs, id)
		if rows := by[id]; len(rows) != 1 || rows[0] != recordRow(&hdr) {
			t.Errorf("%s rows = %+v, want its record row %+v", id, rows, recordRow(&hdr))
		}
	}
	if len(got.Sessions) != 3 {
		t.Errorf("rows = %+v, want the three Bots' record rows", got.Sessions)
	}
	wantNewestFirst(t, got.Sessions)
}
