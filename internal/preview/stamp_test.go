package preview

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/sys/unix"
)

func (f *fixture) stamp(t *testing.T, p string) marotte.PreviewStamp {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/preview/stamp?path="+url.QueryEscape(p), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stamp(%q) = %d %s, want 200", p, rec.Code, rec.Body)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("stamp response carries Access-Control-Allow-Origin")
	}
	var s marotte.PreviewStamp
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func touch(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestStamp_TracksTheFolder(t *testing.T) {
	f := newFixture(t)
	page := filepath.Join(f.demo, "index.html")
	base := f.stamp(t, page)
	if base.Epoch != f.h.signer.Epoch() || base.Stamp == "" || base.Truncated {
		t.Fatalf("stamp = %+v, want the signer's epoch, a digest, not truncated", base)
	}
	if _, err := os.ReadFile(filepath.Join(f.demo, "data.json")); err != nil {
		t.Fatal(err)
	}
	if again := f.stamp(t, page); again.Stamp != base.Stamp {
		t.Errorf("stamp after a read = %q, want unchanged %q", again.Stamp, base.Stamp)
	}
	write(t, filepath.Join(f.demo, "data.json"), `{"a":22}`)
	if changed := f.stamp(t, page); changed.Stamp == base.Stamp {
		t.Error("stamp after a write did not change")
	}
	before := f.stamp(t, page)
	write(t, page, `<meta name="marotte-preview" content="desktop">`)
	if after := f.stamp(t, page); after.Stamp == before.Stamp {
		t.Error("stamp after editing the page's hint meta did not change")
	}
}

func TestGrant_CarriesTheFolderStampOfItsDocument(t *testing.T) {
	f := newFixture(t)
	page := filepath.Join(f.demo, "index.html")
	g := f.grant(t, page)
	if s := f.stamp(t, page); g.Stamp != s.Stamp {
		t.Errorf("grant stamp = %q, want the stamp route's %q for the same tree", g.Stamp, s.Stamp)
	}
	write(t, filepath.Join(f.demo, "data.json"), `{"a":22}`)
	if s := f.stamp(t, page); s.Stamp == g.Stamp {
		t.Error("stamp after a write equals the earlier grant's, want it to differ")
	}
	if again := f.grant(t, page); again.Stamp != f.stamp(t, page).Stamp {
		t.Errorf("fresh grant stamp = %q, want the current tree's", again.Stamp)
	}
}

func TestGrant_ARewriteDuringTheGrantForcesAReload(t *testing.T) {
	f := newFixture(t)
	page := filepath.Join(f.demo, "index.html")
	t.Cleanup(func() { grantStamped = func() {} })
	grantStamped = func() {
		grantStamped = func() {}
		write(t, page, `<!doctype html><html><head><meta name="marotte-preview" content="desktop"></head><body>rewritten</body></html>`)
	}
	g := f.grant(t, page)
	if s := f.stamp(t, page); s.Stamp == g.Stamp {
		t.Errorf("stamp after a rewrite inside the grant = %q, equal to the grant's baseline, want it to differ so the client reloads (grant hint %+v)",
			s.Stamp, g.Hint)
	}
}

func TestStamp_IgnoresDotNodeModulesAndSymlinks(t *testing.T) {
	f := newFixture(t)
	page := filepath.Join(f.demo, "index.html")
	base := f.stamp(t, page)
	write(t, filepath.Join(f.demo, ".cache", "x"), "1")
	write(t, filepath.Join(f.demo, ".env"), "SECRET=2")
	write(t, filepath.Join(f.demo, "node_modules", "pkg", "i.js"), "1")
	write(t, filepath.Join(filepath.Dir(f.demo), "outside-change.txt"), "x")
	touch(t, filepath.Join(f.ws, "root.html"), time.Now().Add(time.Hour))
	if got := f.stamp(t, page); got.Stamp != base.Stamp {
		t.Errorf("stamp changed on an ignored write: %q -> %q", base.Stamp, got.Stamp)
	}
}

func TestStamp_BoundsTheWalk(t *testing.T) {
	f := newFixture(t)
	deep := filepath.Join(f.demo, "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8")
	write(t, filepath.Join(deep, "keep.txt"), "x")
	page := filepath.Join(f.demo, "index.html")
	base := f.stamp(t, page)
	write(t, filepath.Join(deep, "too-deep.txt"), "x")
	if got := f.stamp(t, page); got.Stamp != base.Stamp {
		t.Error("a depth-9 file changed the stamp")
	}
	many := filepath.Join(f.demo, "many")
	for i := range stampMaxEntries + 1 {
		write(t, filepath.Join(many, "f"+strconv.Itoa(i)), "")
	}
	if got := f.stamp(t, page); !got.Truncated || got.Entries != stampMaxEntries {
		t.Errorf("stamp over %d entries = %+v, want truncated at the cap", stampMaxEntries, got)
	}
}

func TestStamp_ReadsABoundedNumberOfNames(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 * stampMaxEntries {
		write(t, filepath.Join(dir, "f"+strconv.Itoa(i)), "")
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	w, err := walkStamp(fd)
	if err != nil {
		t.Fatal(err)
	}
	if !w.truncated || w.entries != stampMaxEntries || w.scanned > stampMaxEntries+stampBatch {
		t.Errorf("walk of %d names = entries %d scanned %d truncated %v, want %d entries, at most %d scanned, truncated",
			3*stampMaxEntries, w.entries, w.scanned, w.truncated, stampMaxEntries, stampMaxEntries+stampBatch)
	}
}

func TestStamp_ADirectoryOfIgnoredNamesSpendsTheScanBudget(t *testing.T) {
	dir := t.TempDir()
	for i := range stampMaxScanned + 1 {
		write(t, filepath.Join(dir, ".f"+strconv.Itoa(i)), "")
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	w, err := walkStamp(fd)
	if err != nil {
		t.Fatal(err)
	}
	if !w.truncated || w.entries != 0 || w.scanned != stampMaxScanned {
		t.Errorf("walk of %d dot names = entries %d scanned %d truncated %v, want 0 entries, %d scanned, truncated",
			stampMaxScanned+1, w.entries, w.scanned, w.truncated, stampMaxScanned)
	}
}

func TestStamp_Refusals(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodPost, "/api/preview/stamp?path="+url.QueryEscape(filepath.Join(f.demo, "index.html")), "")
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
		t.Errorf("POST stamp = %d Allow %q, want 405 GET", rec.Code, rec.Header().Get("Allow"))
	}
	for p, want := range map[string]int{"rel.html": 400, "/etc/x.html": 403, filepath.Join(f.ws, "root.html"): 400, filepath.Join(f.demo, "nope.html"): 404} {
		if rec := f.do(http.MethodGet, "/api/preview/stamp?path="+url.QueryEscape(p), ""); rec.Code != want {
			t.Errorf("stamp(%q) = %d, want %d", p, rec.Code, want)
		}
	}
}
