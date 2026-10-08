package filebrowse

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

type hookRecorder struct {
	refuse  string
	checked []string
	saved   int
}

func (r *hookRecorder) hook() SaveHook {
	return SaveHook{
		Check: func(content []byte) error {
			r.checked = append(r.checked, string(content))
			if string(content) == r.refuse {
				return errors.New("tools.json: manifest version 1, want 2")
			}
			return nil
		},
		Saved: func() { r.saved++ },
	}
}

func putContent(t *testing.T, h *Handler, path, content string) (int, string) {
	t.Helper()
	body, err := json.Marshal(writeBody{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+url.QueryEscape(path), string(body))
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply.Error
}

func TestWriteFile_SaveHookRefusalWritesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &hookRecorder{refuse: "broken"}
	h, err := New(Sensitive{}, []string{dir}, WithSaveHook(target, rec.hook()))
	if err != nil {
		t.Fatal(err)
	}

	code, msg := putContent(t, h, target, "broken")

	if code != http.StatusBadRequest || msg != "tools.json: manifest version 1, want 2" {
		t.Errorf("PUT refused content = %d %q, want 400 carrying the hook's error", code, msg)
	}
	if got, _ := os.ReadFile(target); string(got) != "before" {
		t.Errorf("file after a refused save = %q, want %q untouched", got, "before")
	}
	if rec.saved != 0 {
		t.Errorf("Saved ran %d times after a refused save, want 0", rec.saved)
	}
}

func TestWriteFile_SaveHookRunsSavedAfterTheWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	var onDisk string
	h, err := New(Sensitive{}, []string{dir}, WithSaveHook(target, SaveHook{
		Check: func([]byte) error { return nil },
		Saved: func() {
			b, _ := os.ReadFile(target)
			onDisk = string(b)
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if code, msg := putContent(t, h, target, "after"); code != http.StatusOK {
		t.Fatalf("PUT valid content = %d %q, want 200", code, msg)
	}
	if onDisk != "after" {
		t.Errorf("Saved saw %q on disk, want the new content %q", onDisk, "after")
	}
}

func TestWriteFile_SaveHookLeavesOtherPathsAlone(t *testing.T) {
	dir := t.TempDir()
	rec := &hookRecorder{refuse: "broken"}
	h, err := New(Sensitive{}, []string{dir}, WithSaveHook(filepath.Join(dir, "tools.json"), rec.hook()))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "notes.json")

	if code, msg := putContent(t, h, other, "broken"); code != http.StatusOK {
		t.Fatalf("PUT another file = %d %q, want 200", code, msg)
	}
	if got, _ := os.ReadFile(other); string(got) != "broken" {
		t.Errorf("other file = %q, want %q written", got, "broken")
	}
	if len(rec.checked) != 0 || rec.saved != 0 {
		t.Errorf("hook saw %d checks and %d saves for another path, want 0 and 0", len(rec.checked), rec.saved)
	}
}

func TestWithSaveHook_LaterHookForTheSamePathReplacesTheEarlier(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	first := &hookRecorder{refuse: "broken"}
	second := &hookRecorder{}
	h, err := New(Sensitive{}, []string{dir},
		WithSaveHook(dir+"/./tools.json", first.hook()),
		WithSaveHook(target, second.hook()))
	if err != nil {
		t.Fatal(err)
	}

	if code, msg := putContent(t, h, target, "broken"); code != http.StatusOK {
		t.Fatalf("PUT with the replacing hook = %d %q, want 200 from the later hook", code, msg)
	}
	if len(first.checked) != 0 || first.saved != 0 {
		t.Errorf("replaced hook saw %d checks and %d saves, want 0 and 0", len(first.checked), first.saved)
	}
	if len(second.checked) != 1 || second.saved != 1 {
		t.Errorf("replacing hook saw %d checks and %d saves, want 1 and 1", len(second.checked), second.saved)
	}
}

func TestWithSaveHook_TheFirstRegisteredOfTwoSpellingsRuns(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	link := filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "tools.json")
	viaDotDot := dir + "/sub/../tools.json"
	for _, tc := range []struct {
		name          string
		first, second string
	}{
		{name: "symlink_first", first: viaLink, second: viaDotDot},
		{name: "dotdot_first", first: viaDotDot, second: viaLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range 50 {
				first, second := &hookRecorder{refuse: "broken"}, &hookRecorder{}
				h, err := New(Sensitive{}, []string{dir}, WithSaveHook(tc.first, first.hook()), WithSaveHook(tc.second, second.hook()))
				if err != nil {
					t.Fatal(err)
				}
				code, _ := putContent(t, h, target, "broken")
				if code != http.StatusBadRequest || len(second.checked) != 0 {
					t.Fatalf("handler %d: PUT %s with hooks on %s then %s = %d, second hook checked %d times; want 400 from the first hook only",
						i, target, tc.first, tc.second, code, len(second.checked))
				}
			}
		})
	}
}

func TestWithSaveHook_MatchesThroughASymlinkedRoot(t *testing.T) {
	resolvedDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(resolvedDir, link); err != nil {
		t.Fatal(err)
	}
	rec := &hookRecorder{refuse: "broken"}
	h, err := New(Sensitive{}, []string{resolvedDir}, WithSaveHook(filepath.Join(link, "tools.json"), rec.hook()))
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := putContent(t, h, filepath.Join(resolvedDir, "tools.json"), "broken"); code != http.StatusBadRequest {
		t.Errorf("PUT through the resolved path = %d, want 400 from the hook registered on the link", code)
	}
}

func TestWithSaveHook_FollowsASymlinkSwappedInAfterNew(t *testing.T) {
	dir := t.TempDir()
	hooked := filepath.Join(dir, "tools.json")
	other := filepath.Join(dir, "other.json")
	for _, p := range []string{hooked, other} {
		if err := os.WriteFile(p, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := &hookRecorder{refuse: "broken"}
	h, err := New(Sensitive{}, []string{dir}, WithSaveHook(hooked, rec.hook()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hooked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, hooked); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{hooked, other} {
		if code, _ := putContent(t, h, path, "broken"); code != http.StatusBadRequest {
			t.Errorf("PUT %s after the swap = %d, want 400 from the hook", filepath.Base(path), code)
		}
	}
	if got, _ := os.ReadFile(other); string(got) != "before" {
		t.Errorf("symlink target after refused saves = %q, want %q untouched", got, "before")
	}
}
