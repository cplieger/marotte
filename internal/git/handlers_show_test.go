package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func showReq(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleShow(rec, httptest.NewRequest(http.MethodGet, "/api/git/show?path="+path, nil))
	return rec
}

// A revision over the cap, a binary one and a malformed one are each
// refused with their code and no content, before any of it becomes a lossy JSON string.
func TestHandleShow_RefusesWhatTheDiffCannotShow(t *testing.T) {
	cases := map[string]struct {
		content  string
		wantCode int
		wantKind ShowRefusalCode
	}{
		"over the cap": {content: strings.Repeat("x", 3<<20), wantCode: http.StatusRequestEntityTooLarge, wantKind: ShowTooLarge},
		"binary":       {content: "PNG\x00\x01\x02", wantCode: http.StatusUnsupportedMediaType, wantKind: ShowBinary},
		"not utf-8":    {content: "caf\xe9\n", wantCode: http.StatusUnsupportedMediaType, wantKind: ShowNotUTF8},
	}
	for name, tc := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			skipNoGit(t)
			dir := t.TempDir()
			initFixtureRepo(t, dir)
			writeCommit(t, dir, "blob.dat", tc.content, "add blob")
			rec := showReq(t, NewHandler(dir, WithShowMax(2<<20)), "blob.dat")
			var got struct {
				Content *string         `json:"content"`
				Error   string          `json:"error"`
				Code    ShowRefusalCode `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if rec.Code != tc.wantCode || got.Code != tc.wantKind || got.Error == "" || got.Content != nil {
				t.Errorf("show %s = %d code %q error %q content %v, want %d code %q, a sentence and no content",
					name, rec.Code, got.Code, got.Error, got.Content != nil, tc.wantCode, tc.wantKind)
			}
		})
	}
}

// A blob exactly at the cap is served whole.
func TestHandleShow_ServesABlobAtTheCap(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	writeCommit(t, dir, "full.txt", strings.Repeat("y", 4096), "add")
	rec := showReq(t, NewHandler(dir, WithShowMax(4096)), "full.txt")
	var got ShowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(got.Content) != 4096 {
		t.Errorf("show at the cap = %d (%d bytes), want 200 with all 4096", rec.Code, len(got.Content))
	}
}
