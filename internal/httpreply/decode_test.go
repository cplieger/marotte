package httpreply

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeJSON_ReportsWhetherTheHandlerMayProceed pins the return value every handler branches
// on, in both directions: true only when v was populated, false only with a response written.
func TestDecodeJSON_ReportsWhetherTheHandlerMayProceed(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("a well-formed body decodes and leaves the response untouched", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"name":"marotte"}`))
		req.Header.Set("Content-Type", MIMETypeJSON)
		rec := httptest.NewRecorder()

		var got payload
		if !DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(valid body) = false, want true (body %q)", `{"name":"marotte"}`)
		}
		if got.Name != "marotte" {
			t.Errorf("decoded name = %q, want %q", got.Name, "marotte")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("DecodeJSON wrote %q on the success path, want nothing", rec.Body.String())
		}
	})

	t.Run("a malformed body is refused with 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{not json`))
		req.Header.Set("Content-Type", MIMETypeJSON)
		rec := httptest.NewRecorder()

		var got payload
		if DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(%q) = true, want false", `{not json`)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if !strings.Contains(rec.Body.String(), "invalid json") {
			t.Errorf("body = %q, want it to name the invalid json", rec.Body.String())
		}
	})

	t.Run("a non-JSON content type is refused before the body is read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"name":"marotte"}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()

		var got payload
		if DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(text/plain) = true, want false")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if got.Name != "" {
			t.Errorf("decoded name = %q, want the destination untouched", got.Name)
		}
	})
}
