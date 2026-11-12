package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// memorySyncView is a connection getState answer whose memorySync row has value and the contributions given.
func memorySyncView(value, compose, contributions, assertions string) json.RawMessage {
	return json.RawMessage(`{"view":{"observer":"connection"},"layers":[{"id":"organization","name":"Your organization"},` +
		`{"id":"user-synced","name":"Account"},{"id":"user-local","name":"This device"}],"settings":[` +
		`{"krn":"krn:::setting/other","value":"on","compose":"replace","assertions":[],"contributions":[]},` +
		`{"krn":"krn:::setting/memorySync","value":"` + value + `","compose":"` + compose + `","assertions":` + assertions +
		`,"contributions":` + contributions + `}]}`)
}

func TestMemorySyncChoiceDecode(t *testing.T) {
	const none = `[{"layer":"replace-default","compose":"replace"}]`
	cases := map[string]struct {
		view json.RawMessage
		want memorySyncChoice
	}{
		"unset": {
			view: memorySyncView("off", "replace", `[{"layer":"kiro-agent-defaults","stated":{"value":"off","used":true}}]`, none),
			want: memorySyncChoice{},
		},
		"account on": {
			view: memorySyncView("on", "replace", `[{"layer":"user-synced","stated":{"value":"on","used":true}}]`, none),
			want: memorySyncChoice{Enabled: true, Account: true},
		},
		"account on under another layer's off": {
			view: memorySyncView("off", "replace",
				`[{"layer":"user-synced","stated":{"value":"on","used":false}},{"layer":"user-local","stated":{"value":"off","used":true}}]`, none),
			want: memorySyncChoice{Account: true},
		},
		"on at a layer other than the account's": {
			view: memorySyncView("on", "replace", `[{"layer":"user-local","stated":{"value":"on","used":true}}]`, none),
			want: memorySyncChoice{Enabled: true},
		},
		"locked by the organization": {
			view: memorySyncView("off", "locked", `[{"layer":"organization","stated":{"value":"off","used":true}},{"layer":"user-synced"}]`,
				`[{"layer":"organization","compose":"locked"}]`),
			want: memorySyncChoice{Lock: &memorySyncLock{Layer: "organization", Name: "Your organization"}},
		},
		"locked by a layer the view does not name": {
			view: memorySyncView("on", "locked", `[]`, `[{"layer":"replace-default","compose":"replace"},{"layer":"managed","compose":"locked"}]`),
			want: memorySyncChoice{Enabled: true, Lock: &memorySyncLock{Layer: "managed", Name: "managed"}},
		},
		"no row": {
			view: json.RawMessage(`{"layers":[],"settings":[]}`),
			want: memorySyncChoice{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := decodeMemorySyncChoice(tc.view)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Enabled != tc.want.Enabled || got.Account != tc.want.Account ||
				(got.Lock == nil) != (tc.want.Lock == nil) || (got.Lock != nil && *got.Lock != *tc.want.Lock) {
				t.Errorf("choice = %+v (lock %+v), want %+v (lock %+v)", got, got.Lock, tc.want, tc.want.Lock)
			}
		})
	}
}

func TestMemorySyncStatusDecode(t *testing.T) {
	two := 2
	cases := map[string]struct {
		raw  string
		want memorySyncStatus
	}{
		"not started":          {`{"state":"notStarted"}`, memorySyncStatus{State: "notStarted"}},
		"off":                  {`{"state":"off"}`, memorySyncStatus{State: "off"}},
		"signed out":           {`{"state":"unavailable","reason":"notSignedIn"}`, memorySyncStatus{State: "unavailable", Reason: "notSignedIn"}},
		"suspended":            {`{"state":"suspended","reason":"forbidden","lastSyncAt":"t1"}`, memorySyncStatus{State: "suspended", Reason: "forbidden", LastSyncAt: "t1"}},
		"syncing":              {`{"state":"syncing"}`, memorySyncStatus{State: "syncing"}},
		"synced":               {`{"state":"synced","lastSyncAt":"t1","pending":0}`, memorySyncStatus{State: "synced", LastSyncAt: "t1"}},
		"failed with pending":  {`{"state":"failed","reason":"sync","pending":2}`, memorySyncStatus{State: "failed", Reason: "sync", Pending: &two}},
		"an unknown state":     {`{"state":"paused"}`, memorySyncStatus{State: "unknown"}},
		"an unknown reason":    {`{"state":"unavailable","reason":"later"}`, memorySyncStatus{State: "unknown"}},
		"an unreadable answer": {`[]`, memorySyncStatus{State: "unknown"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := decodeMemorySyncStatus(json.RawMessage(tc.raw))
			if got.State != tc.want.State || got.Reason != tc.want.Reason || got.LastSyncAt != tc.want.LastSyncAt ||
				(tc.want.Pending != nil && (got.Pending == nil || *got.Pending != *tc.want.Pending)) {
				t.Errorf("status = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMemorySyncGet_AnswersStatusAndChoice(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroMemoryGetSyncStatus, json.RawMessage(`{"state":"synced","lastSyncAt":"2026-11-06T10:00:00.000Z","pending":0}`))
	br.setCallResult(methodKiroConfigurationGet, memorySyncView("on", "replace", `[{"layer":"user-synced","stated":{"value":"on","used":true}}]`, `[]`))

	rec := httptest.NewRecorder()
	h.config.handleMemorySync(rec, httptest.NewRequest(http.MethodGet, "/api/memory/sync", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if p := br.paramsFor(methodKiroConfigurationGet); p["observer"] != "connection" {
		t.Errorf("getState params = %v, want the connection's view", p)
	}
	const want = `{"status":{"pending":0,"state":"synced","last_sync_at":"2026-11-06T10:00:00.000Z"},"choice":{"enabled":true,"account":true}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func putMemorySync(h *Runtime, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.config.handleMemorySync(rec, httptest.NewRequest(http.MethodPut, "/api/memory/sync", strings.NewReader(body)))
	return rec
}

// kiro-cli 3.2.1 grants a client memorySync at user-synced only, so every PUT writes there, a body still naming the
// device scope of earlier builds included.
func TestMemorySyncPutWritesTheAccountLayer(t *testing.T) {
	for _, tc := range []struct{ body, value string }{
		{`{"on":true}`, "on"},
		{`{"on":false}`, "off"},
		{`{"scope":"device","on":true}`, "on"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallResult(methodKiroConfigurationSet, json.RawMessage(
				`{"setting":{"krn":"krn:::setting/memorySync","value":"`+tc.value+`","compose":"replace","assertions":[],"contributions":[]}}`,
			))
			before := len(eventsOfType(h, marotte.EventMemorySyncChanged))

			if rec := putMemorySync(h, tc.body); rec.Code != http.StatusOK {
				t.Fatalf("PUT = %d %s, want 200", rec.Code, rec.Body.String())
			}
			p := br.paramsFor(methodKiroConfigurationSet)
			if p["krn"] != memorySyncKRN || p["value"] != tc.value || p["layer"] != "user-synced" {
				t.Errorf("set params = %v, want %s at user-synced", p, tc.value)
			}
			awaitEventCount(t, h, marotte.EventMemorySyncChanged, before+1)
		})
	}
}

// A PUT answering a locked choice names the lock from the connection's view, as the GET does.
func TestMemorySyncPutNamesLock(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroConfigurationSet, json.RawMessage(
		`{"setting":{"krn":"krn:::setting/memorySync","value":"off","compose":"locked","assertions":[{"layer":"organization"}],"contributions":[]}}`,
	))
	br.setCallResult(methodKiroConfigurationGet, memorySyncView("off", "locked", `[]`, `[{"layer":"organization","compose":"locked"}]`))

	rec := putMemorySync(h, `{"on":false}`)

	const want = `{"lock":{"layer":"organization","name":"Your organization"},"enabled":false,"account":false}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
		t.Errorf("PUT = %d %s, want 200 %s", rec.Code, got, want)
	}
}

func TestMemorySyncPutRefusals(t *testing.T) {
	cases := map[string]struct {
		err      *marotte.RPCError
		body     string
		wantCode int
		wantKey  string
	}{
		"locked": {
			err:      &marotte.RPCError{Code: -32602, Message: "Invalid params: the setting is locked", Data: json.RawMessage(`{"errorType":"ConfigurationSetRefusedError","reason":"locked","layer":"organization"}`)},
			body:     `{"on":true}`,
			wantCode: http.StatusConflict, wantKey: `"code":"locked"`,
		},
		"unavailable": {
			err:      &marotte.RPCError{Code: -32000, Message: "The setting cannot be written now", Data: json.RawMessage(`{"reason":"unavailable","layer":"user-synced"}`)},
			body:     `{"on":true}`,
			wantCode: http.StatusConflict, wantKey: `"code":"unavailable"`,
		},
		"not granted": {
			err:      &marotte.RPCError{Code: -32602, Message: "Invalid params: a client may not set this setting at this layer", Data: json.RawMessage(`{"reason":"notGranted"}`)},
			body:     `{"on":true}`,
			wantCode: http.StatusBadRequest, wantKey: "may not set",
		},
		"locked by a layer id carrying a control character": {
			err:      &marotte.RPCError{Code: -32602, Message: "locked", Data: json.RawMessage(`{"reason":"locked","layer":"org\u001b[31m"}`)},
			body:     `{"on":true}`,
			wantCode: http.StatusConflict, wantKey: `"code":"locked"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallRPCErr(methodKiroConfigurationSet, tc.err)

			rec := putMemorySync(h, tc.body)

			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantKey) {
				t.Errorf("PUT = %d %s, want %d carrying %s", rec.Code, rec.Body.String(), tc.wantCode, tc.wantKey)
			}
			if strings.Contains(rec.Body.String(), `\u001b`) {
				t.Errorf("PUT body %s relays a control character", rec.Body.String())
			}
		})
	}
}

// KAS pushes the sync state whenever it moves, so an open tab refetches it.
func TestSyncStatusNotificationPublishes(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := len(eventsOfType(h, marotte.EventMemorySyncChanged))

	h.engineNotification(&marotte.RPCResponse{Method: methodKiroMemorySyncStatus, Params: json.RawMessage(`{"state":"syncing"}`)})

	awaitEventCount(t, h, marotte.EventMemorySyncChanged, before+1)
}
