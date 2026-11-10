package filebrowse

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const ownedRefusal = "tools.json is written through its editor; edit it instead"

func postAction(t *testing.T, h *Handler, action, path, name string) (int, string) {
	t.Helper()
	body, err := json.Marshal(fileAction{Action: action, Path: path, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action", string(body))
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply.Error
}

func TestFileActions_RefuseToMkdirTouchDeleteOrRenameAnOwnedFile(t *testing.T) {
	for _, tc := range []struct {
		name, action, path, rename string
		present                    bool
	}{
		{name: "mkdir_an_absent_owned_file", action: "mkdir", path: "tools.json"},
		{name: "touch_an_absent_owned_file", action: "touch", path: "tools.json"},
		{name: "touch_a_present_owned_file", action: "touch", path: "tools.json", present: true},
		{name: "delete_the_owned_file", action: "delete", path: "tools.json", present: true},
		{name: "rename_the_owned_file_away", action: "rename", path: "tools.json", rename: "old.json", present: true},
		{name: "rename_another_file_onto_the_owned_file", action: "rename", path: "other.json", rename: "tools.json", present: true},
		{name: "rename_another_file_onto_an_absent_owned_file", action: "rename", path: "other.json", rename: "tools.json"},
		{name: "mkdir_below_an_absent_owned_file", action: "mkdir", path: "tools.json/x"},
		{name: "touch_below_an_absent_owned_file", action: "touch", path: "tools.json/x"},
		{name: "rename_onto_a_name_below_an_absent_owned_file", action: "rename", path: "tools.json/y", rename: "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			owned := filepath.Join(dir, "tools.json")
			other := filepath.Join(dir, "other.json")
			writeTestFile(t, other, "other")
			if tc.present {
				writeTestFile(t, owned, "owned")
			}
			h := newOwned(t, dir, WithOwner(owned, &fakeOwner{path: owned}))

			code, msg := postAction(t, h, tc.action, filepath.Join(dir, tc.path), tc.rename)

			if code != http.StatusForbidden || msg != ownedRefusal {
				t.Errorf("%s %s = %d %q, want 403 %q", tc.action, tc.path, code, msg, ownedRefusal)
			}
			got, err := os.ReadFile(owned)
			switch {
			case tc.present && (err != nil || string(got) != "owned"):
				t.Errorf("owned file after the refusal = %q, %v; want %q unchanged", got, err, "owned")
			case !tc.present && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("owned path after the refusal: read error %v, want nothing at it", err)
			}
			if got := readTestFile(t, other); got != "other" {
				t.Errorf("other file = %q, want it untouched", got)
			}
		})
	}
}

func TestFileActions_OnOtherPathsBesideAnOwnedFileStillRun(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "tools.json")
	writeTestFile(t, owned, "owned")
	h := newOwned(t, dir, WithOwner(owned, &fakeOwner{path: owned}))

	for _, step := range []struct{ action, path, rename string }{
		{action: "touch", path: "tools.json.bak"},
		{action: "mkdir", path: "tools"},
		{action: "rename", path: "tools.json.bak", rename: "tools.old"},
		{action: "delete", path: "tools.old"},
	} {
		if code, msg := postAction(t, h, step.action, filepath.Join(dir, step.path), step.rename); code != http.StatusOK {
			t.Errorf("%s %s = %d %q, want 200", step.action, step.path, code, msg)
		}
	}
	if got := readTestFile(t, owned); got != "owned" {
		t.Errorf("owned file = %q, want it untouched", got)
	}
}
