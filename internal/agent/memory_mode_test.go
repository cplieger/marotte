package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// deviceMemoryRow is one composed memoryMode or memoryReflection row as KAS lists it in a view and answers a set with.
func deviceMemoryRow(setting, value string) string {
	return `{"krn":"krn:::setting/` + setting + `","value":"` + value + `","compose":"replace","assertions":[],"contributions":[]}`
}

// memoryView is a connection view holding rows.
func memoryView(rows ...string) json.RawMessage {
	return json.RawMessage(`{"view":{"observer":"connection"},"layers":[{"id":"user-local","name":"This device"}],` +
		`"settings":[` + strings.Join(rows, ",") + `]}`)
}

func putMemoryMode(h *Runtime, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.config.handleMemoryMode(rec, httptest.NewRequest(http.MethodPut, "/api/memory/mode", strings.NewReader(body)))
	return rec
}

type memorySet struct{ krn, value, layer string }

// recordMemorySets answers each _kiro/configuration/set with the row it wrote, recording the writes in order.
func recordMemorySets(br *fakeBridge) func() []memorySet {
	var (
		mu   sync.Mutex
		sets []memorySet
	)
	br.setOnCall(func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroConfigurationSet {
			return nil, nil, false
		}
		krn, _ := p["krn"].(string)
		value, _ := p["value"].(string)
		layer, _ := p["layer"].(string)
		mu.Lock()
		sets = append(sets, memorySet{krn, value, layer})
		mu.Unlock()
		setting := strings.TrimPrefix(krn, "krn:::setting/")
		return json.RawMessage(`{"setting":` + deviceMemoryRow(setting, value) + `}`), nil, true
	})
	return func() []memorySet {
		mu.Lock()
		defer mu.Unlock()
		return append([]memorySet(nil), sets...)
	}
}

// KAS grants a client memoryMode and memoryReflection at user-local only; kiro-cli's terminal UI writes the mode
// before the reflection, and only a member whose composed value differs.
func TestMemoryModePut_WritesTheDeviceLayer(t *testing.T) {
	for _, tc := range []struct {
		name string
		view json.RawMessage
		body string
		want []memorySet
		mode string
	}{
		{
			name: "learn from nothing stated", view: memoryView(), body: `{"mode":"learn"}`, mode: "learn",
			want: []memorySet{
				{"krn:::setting/memoryMode", "read_write", "user-local"},
				{"krn:::setting/memoryReflection", "on", "user-local"},
			},
		},
		{
			name: "learn from read and write", body: `{"mode":"learn"}`, mode: "learn",
			view: memoryView(deviceMemoryRow("memoryMode", "read_write"), deviceMemoryRow("memoryReflection", "off")),
			want: []memorySet{{"krn:::setting/memoryReflection", "on", "user-local"}},
		},
		{
			name: "off from learn", body: `{"mode":"off"}`, mode: "off",
			view: memoryView(deviceMemoryRow("memoryMode", "read_write"), deviceMemoryRow("memoryReflection", "on")),
			want: []memorySet{
				{"krn:::setting/memoryMode", "disabled", "user-local"},
				{"krn:::setting/memoryReflection", "off", "user-local"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			h.config.cacheConfigurationState(tc.view)
			sets := recordMemorySets(br)
			before := len(eventsOfType(h, marotte.EventSettingsUpdated))

			rec := putMemoryMode(h, tc.body)

			wantBody := `{"lock":null,"mode":"` + tc.mode + `"}`
			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != wantBody {
				t.Fatalf("PUT = %d %s, want 200 %s", rec.Code, got, wantBody)
			}
			if got := sets(); len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) ||
				(len(got) > 1 && got[1] != tc.want[1]) {
				t.Errorf("writes = %+v, want %+v", got, tc.want)
			}
			if got := h.config.DeviceMemory(); got == nil || got.Mode != tc.mode {
				t.Errorf("DeviceMemory() after the write = %+v, want mode %s so GET /api/settings answers it", got, tc.mode)
			}
			awaitEventCount(t, h, marotte.EventSettingsUpdated, before+1)
		})
	}
}

func TestMemoryModePut_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		err      *marotte.RPCError
		wantCode int
		wantKey  string
	}{
		"locked": {
			err:      &marotte.RPCError{Code: -32602, Message: "Invalid params: the setting is locked", Data: json.RawMessage(`{"reason":"locked","layer":"organization"}`)},
			wantCode: http.StatusConflict, wantKey: `"code":"locked"`,
		},
		"unavailable": {
			err:      &marotte.RPCError{Code: -32000, Message: "The setting cannot be written now", Data: json.RawMessage(`{"reason":"unavailable","layer":"user-local"}`)},
			wantCode: http.StatusConflict, wantKey: `"code":"unavailable"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallRPCErr(methodKiroConfigurationSet, tc.err)

			rec := putMemoryMode(h, `{"mode":"read_only"}`)

			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantKey) {
				t.Errorf("PUT = %d %s, want %d carrying %s", rec.Code, rec.Body.String(), tc.wantCode, tc.wantKey)
			}
		})
	}
	for name, body := range map[string]string{
		"unknown mode":   `{"mode":"always"}`,
		"KAS's own enum": `{"mode":"disabled"}`,
		"no mode":        `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			if rec := putMemoryMode(h, body); rec.Code != http.StatusBadRequest {
				t.Errorf("PUT %s = %d %s, want 400", body, rec.Code, rec.Body.String())
			}
			if br.called(methodKiroConfigurationSet) {
				t.Errorf("PUT %s called %s, want no write", body, methodKiroConfigurationSet)
			}
		})
	}
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	rec := httptest.NewRecorder()
	h.config.handleMemoryMode(rec, httptest.NewRequest(http.MethodGet, "/api/memory/mode", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec.Code)
	}
}

func TestCacheConfigurationState_AnnouncesADeviceMemoryMove(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	if got := h.config.DeviceMemory(); got != nil {
		t.Errorf("DeviceMemory() before any view = %+v, want nil: nothing is known yet", got)
	}
	h.config.cacheConfigurationState(memoryView())
	if got := h.config.DeviceMemory(); got == nil || got.Mode != marotte.DeviceMemoryOff {
		t.Errorf("DeviceMemory() over a view stating neither row = %+v, want off", got)
	}
	before := settingsUpdatedCount(t, h)

	h.config.cacheConfigurationState(memoryView(deviceMemoryRow("memoryMode", "read_only")))
	if got := h.config.DeviceMemory(); got == nil || got.Mode != marotte.DeviceMemoryReadOnly {
		t.Errorf("DeviceMemory() after the move = %+v, want read_only", got)
	}
	if n := settingsUpdatedCount(t, h); n != before+1 {
		t.Errorf("settings_updated broadcasts after a memory move = %d, want %d", n, before+1)
	}
	h.config.cacheConfigurationState(memoryView(deviceMemoryRow("memoryMode", "read_only")))
	if n := settingsUpdatedCount(t, h); n != before+1 {
		t.Errorf("settings_updated broadcasts after an unchanged view = %d, want still %d", n, before+1)
	}
}
