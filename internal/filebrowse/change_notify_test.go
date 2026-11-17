package filebrowse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// changeLog records what WithChangeNotify reported, in order.
type changeLog struct {
	got []Change
	mu  sync.Mutex
}

func (c *changeLog) notify(_ context.Context, ch Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ch)
}

func (c *changeLog) take() []Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got = nil
	return out
}

func notifyingDir(t *testing.T) (h *Handler, log *changeLog, dir, prefix string) {
	t.Helper()
	dir = t.TempDir()
	log = &changeLog{}
	h, err := New(Sensitive{}, []string{dir}, WithChangeNotify(log.notify))
	if err != nil {
		t.Fatal(err)
	}
	return h, log, dir, strings.TrimPrefix(dir, "/")
}

// Each action reports the path it changed in its cleaned absolute spelling; a rename also names both ends, and a
// refused action reports nothing.
func TestFileActions_NotifyTheChangeTheyMade(t *testing.T) {
	h, log, dir, prefix := notifyingDir(t)
	writeTestFile(t, filepath.Join(dir, "a.txt"), "A")
	writeTestFile(t, filepath.Join(dir, "taken.txt"), "T")
	cases := []struct {
		desc, body string
		want       Change
	}{
		{
			"rename", `{"action":"rename","path":"` + prefix + `/a.txt","name":"b.txt"}`,
			Change{Paths: []string{dir + "/a.txt", dir + "/b.txt"}, From: dir + "/a.txt", To: dir + "/b.txt"},
		},
		{"mkdir", `{"action":"mkdir","path":"` + prefix + `/sub/"}`, Change{Paths: []string{dir + "/sub"}}},
		{"touch", `{"action":"touch","path":"` + prefix + `/sub/n.txt"}`, Change{Paths: []string{dir + "/sub/n.txt"}}},
		{"delete", `{"action":"delete","path":"` + prefix + `/sub"}`, Change{Paths: []string{dir + "/sub"}}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if rec := postReq(t, h, "/api/files/action", tc.body); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d %s", tc.desc, rec.Code, rec.Body.String())
			}
			got := log.take()
			if len(got) != 1 || !slices.Equal(got[0].Paths, tc.want.Paths) || got[0].From != tc.want.From || got[0].To != tc.want.To {
				t.Errorf("%s notified %+v, want [%+v]", tc.desc, got, tc.want)
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		rec := postReq(t, h, "/api/files/action", `{"action":"rename","path":"`+prefix+`/b.txt","name":"taken.txt"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("rename onto a taken name = %d, want 409", rec.Code)
		}
		if got := log.take(); len(got) != 0 {
			t.Errorf("a refused rename notified %+v, want nothing", got)
		}
	})
}

// A saved file reports its path; a refused save reports nothing.
func TestWrite_NotifiesItsPath(t *testing.T) {
	h, log, dir, prefix := notifyingDir(t)
	if rec := putReq(t, h, "/api/file?path="+prefix+"/w.txt", `{"content":"hello"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	if got := log.take(); len(got) != 1 || !slices.Equal(got[0].Paths, []string{dir + "/w.txt"}) || got[0].From != "" {
		t.Errorf("a save notified %+v, want one change naming %s/w.txt", got, dir)
	}
	stale := `{"content":"x","file_id":"sha256:` + strings.Repeat("0", 64) + `"}`
	if rec := putReq(t, h, "/api/file?path="+prefix+"/w.txt", stale); rec.Code != http.StatusConflict {
		t.Fatalf("stale PUT = %d, want 409", rec.Code)
	}
	if got := log.take(); len(got) != 0 {
		t.Errorf("a refused save notified %+v, want nothing", got)
	}
}

// An upload reports every file that landed, a partial batch included.
func TestUpload_NotifiesTheUploadedPaths(t *testing.T) {
	h, log, dir, prefix := notifyingDir(t)
	if err := os.Mkdir(filepath.Join(dir, "up"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := serveUpload(t, h, uploadOrdered(t, prefix+"/up", []string{"x.txt", "y.txt"}, [][]byte{[]byte("x"), []byte("y")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	got := log.take()
	if len(got) != 1 || !slices.Equal(got[0].Paths, []string{dir + "/up/x.txt", dir + "/up/y.txt"}) {
		t.Errorf("an upload notified %+v, want one change naming both files", got)
	}

	rec = serveUpload(t, h, uploadOrdered(t, prefix+"/up", []string{"z.txt", ".."}, [][]byte{[]byte("z"), []byte("!")}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a batch with a bad name = %d, want 400", rec.Code)
	}
	if got := log.take(); len(got) != 1 || !slices.Equal(got[0].Paths, []string{dir + "/up/z.txt"}) {
		t.Errorf("a partial batch notified %+v, want the one file that landed", got)
	}
}

// The notifier runs detached from the request, so a client that walks away after the rename lands still has its
// tabs follow.
func TestChange_NotifiesUnderAContextTheRequestCannotCancel(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "A")
	var notifyErr error
	h, err := New(Sensitive{}, []string{dir}, WithChangeNotify(func(ctx context.Context, _ Change) { notifyErr = ctx.Err() }))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	body := `{"action":"rename","path":"` + strings.TrimPrefix(dir, "/") + `/a.txt","name":"b.txt"}`
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/files/action", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	if notifyErr != nil {
		t.Errorf("the notifier's context Err = %v, want nil after the request's was cancelled", notifyErr)
	}
}

// A rename onto a path an editor tab holds is refused before it happens; the source stays and nothing is notified.
func TestRename_RefusesADestinationOpenInAnEditorTab(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "A")
	log := &changeLog{}
	var asked []string
	held := func(p string) bool { asked = append(asked, p); return p == dir+"/b.txt" }
	h, err := New(Sensitive{}, []string{dir}, WithChangeNotify(log.notify), WithOpenInEditor(held))
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimPrefix(dir, "/")
	rec := postReq(t, h, "/api/files/action", `{"action":"rename","path":"`+prefix+`/a.txt","name":"b.txt"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "b.txt is open in an editor tab; close it first") {
		t.Fatalf("rename onto a held path = %d %s, want 409 naming the editor tab", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("the refused rename moved the source: %v", err)
	}
	if got := log.take(); len(got) != 0 {
		t.Errorf("a refused rename notified %+v", got)
	}
	if !slices.Equal(asked, []string{dir + "/b.txt"}) {
		t.Errorf("the guard was asked %v, want the destination alone", asked)
	}
	if rec := postReq(t, h, "/api/files/action", `{"action":"rename","path":"`+prefix+`/a.txt","name":"c.txt"}`); rec.Code != http.StatusOK {
		t.Errorf("rename onto a free path = %d %s, want 200", rec.Code, rec.Body.String())
	}
}
