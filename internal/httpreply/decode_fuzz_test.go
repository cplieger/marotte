package httpreply

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzDecodeJSONContentType targets the DecodeJSON Content-Type gate: no crafted Content-Type
// header may pass the prefix check without being application/json.
func FuzzDecodeJSONContentType(f *testing.F) {
	f.Add("application/json", `{"key":"value"}`)
	f.Add("application/json; charset=utf-8", `{"key":"value"}`)
	f.Add("text/plain", `{"key":"value"}`)
	f.Add("", `{"key":"value"}`)
	f.Add("application/json\x00extra", `{"key":"value"}`)
	f.Add("APPLICATION/JSON", `{"key":"value"}`)
	f.Add("application/jsonl", `{"key":"value"}`)
	f.Add("application/json-patch+json", `{"key":"value"}`)
	f.Add("application/xml", `{"key":"value"}`)
	f.Add("text/html", `{"key":"value"}`)
	f.Add("multipart/form-data", `{"key":"value"}`)

	f.Fuzz(func(t *testing.T, contentType, body string) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()

		var dst map[string]any
		ok := DecodeJSON(rec, req, &dst)

		if contentType != "" && !strings.HasPrefix(contentType, MIMETypeJSON) {
			if ok {
				t.Fatalf("DecodeJSON accepted non-JSON content-type %q", contentType)
			}
			return
		}

		if ok && dst == nil {
			t.Fatalf("DecodeJSON returned true but dst is nil for body %q", body)
		}

		if !ok && rec.Code >= 500 {
			t.Fatalf("DecodeJSON wrote 5xx status %d for input ct=%q body=%q",
				rec.Code, contentType, body)
		}
	})
}

func FuzzDecodeJSON(f *testing.F) {
	f.Add("application/json", []byte(`{"key":"value"}`))
	f.Add("application/json", []byte(`{}`))
	f.Add("application/json", []byte(`{"nested":{"a":1}}`))
	f.Add("application/json", []byte(`{not json`))
	f.Add("application/json", []byte(``))
	f.Add("application/json", []byte(`null`))
	f.Add("application/json", []byte(`[1,2,3]`))
	f.Add("text/plain", []byte(`{"key":"value"}`))
	f.Add("", []byte(`{"key":"value"}`))
	f.Add("application/xml", []byte(`<xml/>`))
	f.Add("application/json", []byte{0x00, 0xff, 0xfe})

	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		var dest map[string]any
		ok := DecodeJSON(rec, req, &dest)

		if !ok {
			if rec.Code == http.StatusOK || rec.Code == 0 {
				t.Errorf("DecodeJSON returned false but status = %d (expected error status)", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("DecodeJSON error response Content-Type = %q, want application/json", ct)
			}
		}
	})
}
