package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestHandleMemoryCollection_ListsTheStoreWithoutASession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	br.callResults = map[string]json.RawMessage{methodKiroMemoryList: json.RawMessage(`{"memories":[` +
		`{"id":"m1","memoryType":"USER_PREFERENCE","scopes":["Global"],"title":"Tabs","summary":"use tabs","createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-02T00:00:00Z"},` +
		`{"id":"m2","memoryType":"SEMANTIC","title":"Build","summary":"make"}]}`)}

	rec := httptest.NewRecorder()
	h.config.handleMemoryCollection(rec, httptest.NewRequest(http.MethodGet, "/api/memory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/memory = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body memoryListResponse
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
	t.Cleanup(h.stopUtilityBridge)
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
	t.Cleanup(h.stopUtilityBridge)
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
			"a transport fault is generic", errors.New("bridge exited: /config/home/secret path"),
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
