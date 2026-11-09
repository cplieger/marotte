package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route/routetest"
)

func TestHandleMemoryCollection_ListsTheStoreWithoutASession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroMemoryList: json.RawMessage(`{"memories":[` +
		`{"id":"m1","memoryType":"USER_PREFERENCE","scopes":["Global"],"title":"Tabs","summary":"use tabs","createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-02T00:00:00Z"},` +
		`{"id":"m2","memoryType":"SEMANTIC","title":"Build","summary":"make"}]}`)}

	rec := httptest.NewRecorder()
	h.config.handleMemoryCollection(rec, httptest.NewRequest(http.MethodGet, "/api/memory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/memory = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body MemoryList
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Cap != 1000 || len(body.Memories) != 2 {
		t.Fatalf("body = %+v, want cap 1000 and two rows", body)
	}
	if got := body.Memories[0]; got.ID != "m1" || got.MemoryType != "USER_PREFERENCE" || got.Summary != "use tabs" {
		t.Errorf("row 0 = %+v", got)
	}
	// A record with no scopes is KAS's Global scope.
	if got := body.Memories[1].Scopes; len(got) != 1 || got[0] != "Global" {
		t.Errorf("row 1 scopes = %v, want [Global]", got)
	}
	params := br.paramsFor(methodKiroMemoryList)
	if _, has := params["sessionId"]; has {
		t.Error("memory list carries a sessionId; without one KAS answers from entitlement alone")
	}
	if params["limit"] != float64(1000) && params["limit"] != 1000 {
		t.Errorf("limit = %v, want 1000 (the store cap, so one page holds it all)", params["limit"])
	}
}

func TestHandleMemoryOne_AddressesByIDAndMapsTheEdit(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroMemoryUpdate: json.RawMessage(
		`{"memory":{"id":"m1","memoryType":"SEMANTIC","title":"New","summary":"s","content":"c"}}`,
	)}

	req := httptest.NewRequest(http.MethodPatch, "/api/memory/m1", strings.NewReader(`{"title":"New","content":"c"}`))
	req.SetPathValue("id", "m1")
	rec := httptest.NewRecorder()
	h.config.handleMemoryOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroMemoryUpdate)
	if params["id"] != "m1" || params["newTitle"] != "New" || params["content"] != "c" {
		t.Errorf("update params = %+v, want id m1, newTitle New, content c", params)
	}
	if _, has := params["summary"]; has {
		t.Errorf("update params = %+v; an absent field must not be sent", params)
	}
	if _, has := params["title"]; has {
		t.Error("update addressed by title; titles repeat across scopes, so only id may address a record")
	}
}

func TestHandleMemoryOne_DeleteByID(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroMemoryDelete: json.RawMessage(`{"id":"m9","title":"x"}`)}
	req := httptest.NewRequest(http.MethodDelete, "/api/memory/m9", nil)
	req.SetPathValue("id", "m9")
	rec := httptest.NewRecorder()
	h.config.handleMemoryOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := br.paramsFor(methodKiroMemoryDelete)["id"]; got != "m9" {
		t.Errorf("delete id = %v, want m9", got)
	}
}

func TestHandleMemoryOne_DeleteOfAGoneIDIsDone(t *testing.T) {
	cases := []struct {
		name     string
		refusal  *marotte.RPCError
		wantCode int
	}{
		{
			// The wire shape KAS's ACP layer gives a thrown InvalidMemoryCommandError.
			"an id KAS no longer holds", &marotte.RPCError{
				Code: -32603, Message: "Internal error",
				Data: json.RawMessage(`{"details":"no memory with id \"m9\" to delete."}`),
			}, http.StatusOK,
		},
		{
			"any other refusal still fails", &marotte.RPCError{
				Code: -32603, Message: "Internal error",
				Data: json.RawMessage(`{"details":"'id' or 'title' is required for delete."}`),
			}, http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.callRPCErrs = map[string]*marotte.RPCError{methodKiroMemoryDelete: tc.refusal}
			req := httptest.NewRequest(http.MethodDelete, "/api/memory/m9", nil)
			req.SetPathValue("id", "m9")
			rec := httptest.NewRecorder()
			h.config.handleMemoryOne(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("DELETE = %d, want %d (%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestWriteMemoryErr_ClassifiesTheFailure(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantText string
	}{
		{
			"an ineligible account", &marotte.RPCError{Code: -32602, Message: "Invalid params: Memory is not enabled."},
			http.StatusConflict, memoryNotEnabledCode,
		},
		{
			"a KAS refusal keeps KAS's words", &marotte.RPCError{Code: -32602, Message: `no memory with id "m1" to delete.`},
			http.StatusBadRequest, "no memory with id",
		},
		{
			"a parse error KAS itself sends keeps KAS's words", &marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"},
			http.StatusBadRequest, "Parse error",
		},
		{
			"a transport fault is generic", errors.New("bridge exited: /config/home/secret path"),
			http.StatusBadGateway, "memory request failed",
		},
		{
			"an undecoded answer is not a refusal", undecodedReply(),
			http.StatusBadGateway, "memory request failed",
		},
		{
			"an oversize answer is not a refusal", &marotte.TransportError{Err: marotte.ErrFrameTooLarge},
			http.StatusBadGateway, "memory request failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeMemoryErr(rec, tc.err)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantText) {
				t.Errorf("writeMemoryErr(%v) = %d %s, want %d containing %q",
					tc.err, rec.Code, rec.Body.String(), tc.wantCode, tc.wantText)
			}
		})
	}
}

// TestHandleMemoryOne_AStalledEngineAnswersStillApplying pins that an edit or delete the engine has not answered within
// the browser's budget is reported as still applying, never as failed, and that the late answer refreshes the list.
func TestHandleMemoryOne_AStalledEngineAnswersStillApplying(t *testing.T) {
	for _, tc := range []struct {
		method, kasMethod, body, reply string
	}{
		{http.MethodPatch, methodKiroMemoryUpdate, `{"title":"New"}`, `{"memory":{"id":"m1","memoryType":"SEMANTIC","title":"New","summary":"s"}}`},
		{http.MethodDelete, methodKiroMemoryDelete, ``, `{"id":"m1","title":"x"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			answerBudgetOf(t, 50*time.Millisecond)
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallResult(tc.kasMethod, json.RawMessage(tc.reply))
			release := holdCall(t, br, tc.kasMethod)
			before := len(eventsOfType(h, marotte.EventMemoriesChanged))

			req := httptest.NewRequest(tc.method, "/api/memory/m1", strings.NewReader(tc.body))
			req.SetPathValue("id", "m1")
			rec := answeredWithin(t, func(w http.ResponseWriter) { h.config.handleMemoryOne(w, req) })

			assertStillApplying(t, rec)
			release()
			awaitEventCount(t, h, marotte.EventMemoriesChanged, before+1)
		})
	}
}

// KAS tells the client after any change to the store, the agent's own writes included, so an open Memories tab
// refetches without a marotte write.
func TestMemoryChangedNotification_BroadcastsRefetch(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := len(eventsOfType(h, marotte.EventMemoriesChanged))

	h.engineNotification(&marotte.RPCResponse{Method: methodKiroMemoryChanged, Params: json.RawMessage(
		`{"total":2,"counts":{"byType":{"SEMANTIC":2},"byScope":{"Global":2}}}`,
	)})

	awaitEventCount(t, h, marotte.EventMemoriesChanged, before+1)
}

// A record the cloud has not taken yet carries KAS's pending mark to the tab.
func TestMemoryListCarriesCloudState(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroMemoryList, json.RawMessage(`{"memories":[`+
		`{"id":"m1","memoryType":"SEMANTIC","title":"a","summary":"s","_meta":{"kiro":{"cloudState":"pending"}}},`+
		`{"id":"m2","memoryType":"SEMANTIC","title":"b","summary":"s"}]}`))
	rec := httptest.NewRecorder()
	h.config.handleMemoryCollection(rec, httptest.NewRequest(http.MethodGet, "/api/memory", nil))

	var got MemoryList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Memories) != 2 {
		t.Fatalf("GET = %d %s, want two rows", rec.Code, rec.Body.String())
	}
	if got.Memories[0].CloudState != "pending" || got.Memories[1].CloudState != "" {
		t.Errorf("cloud states = %q, %q, want pending then none", got.Memories[0].CloudState, got.Memories[1].CloudState)
	}
	if strings.Contains(rec.Body.String(), `"_meta"`) {
		t.Errorf("body %s passes KAS's _meta through", rec.Body.String())
	}
}

// A HEAD on a path whose GET reads the engine and whose other methods write it takes the read arm: the engine sees
// the read's calls only, though the body would make a write arm succeed.
func TestKnowledgeAndMemoryRoutes_HEADOnlyReads(t *testing.T) {
	tests := map[string]struct {
		path, body string
		want       []string
	}{
		"knowledge":   {"/api/knowledge", `{"path":"/w/docs"}`, []string{methodKiroKnowledge}},
		"memory sync": {"/api/memory/sync", `{"scope":"device","on":true}`, []string{methodKiroMemoryGetSyncStatus, methodKiroConfigurationGet}},
		"one memory":  {"/api/memory/m1", `{"title":"New"}`, []string{methodKiroMemoryGet}},
	}
	const record = `{"memory":{"id":"m1","memoryType":"SEMANTIC","title":"Old","summary":"s","content":"c"}}`
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			for method, res := range map[string]string{
				methodKiroKnowledge:           `{"success":true,"message":"done","entries":[{"name":"docs","id":"a1","item_count":1,"path":"/w/docs"}]}`,
				methodKiroMemoryGet:           record,
				methodKiroMemoryUpdate:        record,
				methodKiroMemoryDelete:        `{"id":"m1","title":"Old"}`,
				methodKiroMemoryGetSyncStatus: `{"state":"synced","lastSyncAt":"2026-11-06T10:00:00.000Z","pending":0}`,
				methodKiroConfigurationGet:    string(memorySyncView("on", "replace", `[]`, `[]`)),
				methodKiroConfigurationSet:    `{"setting":{"krn":"krn:::setting/memorySync","value":"on","compose":"replace","assertions":[],"contributions":[]}}`,
			} {
				br.setCallResult(method, json.RawMessage(res))
			}
			mux := http.NewServeMux()
			h.config.registerKnowledgeRoutes(routetest.On(mux))
			h.config.registerMemoryRoutes(routetest.On(mux))
			br.forgetCalls()

			req := httptest.NewRequest(http.MethodHead, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("HEAD %s = %d %q, want 200 application/json", tc.path, rec.Code, rec.Header().Get("Content-Type"))
			}
			if got := br.callLog(); !slices.Equal(got, tc.want) {
				t.Errorf("HEAD %s called %v, want only the read's %v", tc.path, got, tc.want)
			}
			if p := br.paramsFor(methodKiroKnowledge); p != nil && p[keySubcommand] != "show" {
				t.Errorf("HEAD %s sent knowledge %v, want show", tc.path, p[keySubcommand])
			}
		})
	}
}
