package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// modelFallbackCapture is one exchange of internal/translate/testdata/configuration_model_fallback.json, captured on
// kiro-cli 3.2.1.
type modelFallbackCapture struct {
	Params map[string]json.RawMessage `json:"params"`
	Result json.RawMessage            `json:"result"`
	Error  *marotte.RPCError          `json:"error"`
}

func modelFallbackExchange(t *testing.T, name string) modelFallbackCapture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "translate", "testdata", "configuration_model_fallback.json"))
	if err != nil {
		t.Fatalf("read the configuration_model_fallback fixture: %v", err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse the configuration_model_fallback fixture: %v", err)
	}
	var ex modelFallbackCapture
	if err := json.Unmarshal(fx[name], &ex); err != nil {
		t.Fatalf("parse the fixture's %s exchange: %v", name, err)
	}
	return ex
}

func putModelFallback(h *Runtime, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.config.handleModelFallback(rec, httptest.NewRequest(http.MethodPut, "/api/model-fallback", strings.NewReader(body)))
	return rec
}

// KAS grants a client modelFallback at user-synced only, and takes a one-model chain or an empty one, which clears
// the account's choice back to Kiro's.
func TestModelFallbackPutWritesTheAccountLayer(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantValue, wantBody string
	}{
		{
			name: "set", body: `{"model":"model-a","target":"model-b"}`, wantValue: `["model-b"]`,
			wantBody: `{"model":"model-a","configured":"model-b","vendor_default":"model-k","read_only_reason":""}`,
		},
		{
			name: "clear", body: `{"model":"model-a","target":""}`, wantValue: `[]`,
			wantBody: `{"model":"model-a","configured":"","vendor_default":"model-k","read_only_reason":""}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallResult(methodKiroConfigurationSet, modelFallbackExchange(t, tc.name).Result)
			before := settingsUpdatedCount(t, h)

			rec := putModelFallback(h, tc.body)

			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != tc.wantBody {
				t.Fatalf("PUT = %d %s, want 200 %s", rec.Code, got, tc.wantBody)
			}
			p := br.paramsFor(methodKiroConfigurationSet)
			value, _ := json.Marshal(p["value"])
			if p["krn"] != "krn:::setting/modelFallback/model-a" || string(value) != tc.wantValue || p["layer"] != "user-synced" {
				t.Errorf("set params = %v (value %s), want modelFallback/model-a = %s at user-synced", p, value, tc.wantValue)
			}
			var want marotte.ModelFallback
			_ = json.Unmarshal([]byte(tc.wantBody), &want)
			if got := h.config.ModelFallbacks(); len(got) != 1 || got[0] != want {
				t.Errorf("ModelFallbacks() after the write = %+v, want [%+v] so GET /api/settings answers it", got, want)
			}
			if n := settingsUpdatedCount(t, h); n != before+1 {
				t.Errorf("settings_updated broadcasts = %d, want %d so every tab repaints", n, before+1)
			}
		})
	}
}

func TestModelFallbackPutRejectsABadRequest(t *testing.T) {
	for name, body := range map[string]string{
		"no model":  `{"target":"model-b"}`,
		"blank":     `{"model":"  ","target":"model-b"}`,
		"two picks": `{"model":"model-a","target":["model-b","model-c"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })

			if rec := putModelFallback(h, body); rec.Code != http.StatusBadRequest {
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
	h.config.handleModelFallback(rec, httptest.NewRequest(http.MethodGet, "/api/model-fallback", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPut {
		t.Errorf("GET = %d Allow %q, want 405 Allow PUT", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestModelFallbackPutRefusals(t *testing.T) {
	identityChanged := &marotte.RPCError{Code: -32000, Message: "Fallback preference was not stored; the account changed before it could be written. Read the settings again for the current account."}
	cases := map[string]struct {
		err      *marotte.RPCError
		wantCode int
		wantKey  string
	}{
		"locked": {
			err:      &marotte.RPCError{Code: -32602, Message: "Invalid params: the setting is locked", Data: json.RawMessage(`{"errorType":"ConfigurationSetRefusedError","reason":"locked","layer":"organization"}`)},
			wantCode: http.StatusConflict, wantKey: `Model fallback is locked by the organization layer.`,
		},
		"unavailable": {
			err:      &marotte.RPCError{Code: -32000, Message: "The setting cannot be written now", Data: json.RawMessage(`{"reason":"unavailable","layer":"user-synced"}`)},
			wantCode: http.StatusConflict, wantKey: `"code":"unavailable"`,
		},
		"its own fallback": {
			err:      modelFallbackExchange(t, "refused_self").Error,
			wantCode: http.StatusBadRequest, wantKey: `'model-a' cannot be its own fallback`,
		},
		"two targets": {
			err:      modelFallbackExchange(t, "refused_two_targets").Error,
			wantCode: http.StatusBadRequest, wantKey: `does not accept`,
		},
		"a model with no fallback": {
			err:      modelFallbackExchange(t, "refused_auto").Error,
			wantCode: http.StatusConflict, wantKey: `not eligible for model fallback`,
		},
		"the account changed": {
			err:      identityChanged,
			wantCode: http.StatusConflict, wantKey: `the account changed before it could be written`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallRPCErr(methodKiroConfigurationSet, tc.err)
			before := settingsUpdatedCount(t, h)

			rec := putModelFallback(h, `{"model":"model-a","target":"model-b"}`)

			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantKey) {
				t.Errorf("PUT = %d %s, want %d carrying %s", rec.Code, rec.Body.String(), tc.wantCode, tc.wantKey)
			}
			if n := settingsUpdatedCount(t, h); n != before {
				t.Errorf("settings_updated broadcasts after a refusal = %d, want %d", n, before)
			}
		})
	}
}
