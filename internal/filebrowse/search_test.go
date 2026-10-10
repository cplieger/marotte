package filebrowse

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/textsearch"
)

// searchHandlerAt's single mount CLAIMS policyDir (e.g. "/config", so the real sensitive prefixes
// apply) while its os.Root is backed by a throwaway tree the test can populate; testHandlerAt
// discards the backing path, and a content search has to write files into it.
func searchHandlerAt(t *testing.T, policyDir string) (h *Handler, backing string) {
	t.Helper()
	backing = t.TempDir()
	root, err := os.OpenRoot(backing)
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{mounts: []mount{{
		root: root,
		dir:  policyDir,
		name: strings.TrimPrefix(policyDir, "/"),
	}}}, backing
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func searchReq(t *testing.T, h *Handler, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return searchReqCtx(t, h, t.Context(), params)
}

func searchReqCtx(t *testing.T, h *Handler, ctx context.Context, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/files/search?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) FileSearchResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res FileSearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return res
}

func matchPaths(res FileSearchResult) []string {
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, m.Path)
	}
	return out
}

func TestSearch_FindsMatchRecursively(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"top.txt":            "nothing here\n",
		"a/b/deep.go":        "package b\n\nfunc needle() {}\n",
		"a/b/c/deeper.txt":   "line one\nsecond needle line\n",
		"a/unrelated.md":     "prose\n",
		"a/b/case.txt":       "NEEDLE shouting\n",
		"a/b/c/d/e/far.conf": "needle\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	want := []string{
		filepath.Join(dir, "a/b/c/d/e/far.conf"),
		filepath.Join(dir, "a/b/c/deeper.txt"),
		filepath.Join(dir, "a/b/case.txt"),
		filepath.Join(dir, "a/b/deep.go"),
	}
	got := matchPaths(res)
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if res.Truncated {
		t.Error("truncated = true on a tree well under every cap")
	}
	if res.Scanned != 6 {
		t.Errorf("scanned = %d, want 6 (every file was read)", res.Scanned)
	}
	if res.Matched != 4 {
		t.Errorf("matched = %d, want 4: one matching line per hit, nothing cut", res.Matched)
	}
	for _, m := range res.Matches {
		if m.Line < 1 {
			t.Errorf("%s: line = %d, want >= 1", m.Path, m.Line)
		}
	}
	deeper := res.Matches[1]
	if deeper.Line != 2 || deeper.Excerpt != "second needle line" {
		t.Errorf("deeper.txt hit = line %d %q, want line 2 %q",
			deeper.Line, deeper.Excerpt, "second needle line")
	}
}

func TestSearch_CaseSensitivity(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"lower.txt": "needle\n",
		"upper.txt": "NEEDLE\n",
	})

	insensitive := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(insensitive.Matches) != 2 {
		t.Errorf("insensitive matches = %v, want both files", matchPaths(insensitive))
	}

	sensitive := decodeSearch(t, searchReq(t, h,
		map[string]string{"mode": "contents", "path": prefix, "q": "needle", "case": "1"}))
	if len(sensitive.Matches) != 1 || sensitive.Matches[0].Path != filepath.Join(dir, "lower.txt") {
		t.Errorf("case=1 matches = %v, want only lower.txt", matchPaths(sensitive))
	}

	other := decodeSearch(t, searchReq(t, h,
		map[string]string{"mode": "contents", "path": prefix, "q": "needle", "case": "true"}))
	if len(other.Matches) != 2 {
		t.Errorf("case=true matches = %v, want both (only \"1\" enables it)", matchPaths(other))
	}
}

// The file budget is spent at admission and names how many files the walk
// READS: a tree of exactly that many is read whole and is a complete answer, and
// one more is the file the walk refuses, which is what Truncated says. The files
// match nothing, so the reply cap cannot be what stops the walk.
func TestSearch_FileBudgetMarksOnlyTheFileItRefuses(t *testing.T) {
	tests := []struct {
		name          string
		files         int
		wantTruncated bool
	}{
		{name: "exactly_the_budget", files: maxSearchFiles, wantTruncated: false},
		{name: "one_file_past_the_budget", files: maxSearchFiles + 1, wantTruncated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			for i := range tc.files {
				name := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
				if err := os.WriteFile(name, []byte("nothing matching\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
			if res.Truncated != tc.wantTruncated {
				t.Errorf("truncated = %v over %d files, want %v", res.Truncated, tc.files, tc.wantTruncated)
			}
			if res.Scanned != maxSearchFiles {
				t.Errorf("scanned = %d over %d files, want %d: the budget is read at admission, before any file is skipped",
					res.Scanned, tc.files, maxSearchFiles)
			}
			if len(res.Matches) != 0 {
				t.Errorf("matches = %v, want none: the fixture holds the needle nowhere", matchPaths(res))
			}
		})
	}
}

// The reply cap crossed in the LAST chunk of a tree the walk read whole is a
// cut and not a hole: every file was read, so Matched says what was cut and
// Truncated stays false. A directory reports its end only on the read after
// its last entries, which a stop before that read would mistake for more to do.
func TestSearch_ReplyCapOnAFullyReadTreeIsACutNotAHole(t *testing.T) {
	h, dir, prefix := testDir(t)
	const perFile = maxFileMatches + 5
	files := maxSearchMatches/maxFileMatches + 2
	for i := range files {
		name := filepath.Join(dir, fmt.Sprintf("m%03d.txt", i))
		if err := os.WriteFile(name, []byte(strings.Repeat("needle\n", perFile)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(res.Matches) != maxSearchMatches {
		t.Errorf("matches = %d, want exactly the cap %d", len(res.Matches), maxSearchMatches)
	}
	if want := files * perFile; res.Matched != want {
		t.Errorf("matched = %d, want %d: every matching line counted, past both caps", res.Matched, want)
	}
	if res.Scanned != files {
		t.Errorf("scanned = %d, want %d: every file was read", res.Scanned, files)
	}
	if res.Truncated {
		t.Error("truncated = true after a walk that read every file: a full reply is a cut, not a hole")
	}
	byPath := map[string]int{}
	for _, m := range res.Matches {
		byPath[m.Path]++
	}
	for p, n := range byPath {
		if n > maxFileMatches {
			t.Errorf("%s contributed %d matches, want at most %d", p, n, maxFileMatches)
		}
	}
}

func TestSearch_SensitivePathNeverInResults(t *testing.T) {
	// The walk reaches these sensitive paths from ABOVE, the case an os.Root cannot cover.
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{
		"mcp-secrets.json":             `{"token":"needle-secret"}`,
		"mcp.json":                     `{"server":"needle-server"}`,
		"push-subs.json":               `{"sub":"needle"}`,
		"vapid-keys.json":              `{"key":"needle"}`,
		"home/.aws/sso/cache/tok.json": `{"accessToken":"needle"}`,
		"home/.ssh/id_rsa":             "needle private key\n",
		"chats/c1.json":                `{"messages":"needle"}`,
		"kiro/steering/x.md":           "needle\n",
		"visible.txt":                  "needle in a browsable file\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": "config", "q": "needle"}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != "/config/visible.txt" {
		t.Fatalf("matches = %v, want only /config/visible.txt", got)
	}
	body := res.Matches[0].Excerpt
	for _, leak := range []string{"needle-secret", "needle-server", "accessToken", "private key"} {
		if strings.Contains(body, leak) {
			t.Errorf("excerpt leaked %q", leak)
		}
	}
}

func TestSearch_SensitivePathNeverAName(t *testing.T) {
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{
		"mcp-secrets.json":             "x",
		"mcp.json":                     "x",
		"push-subs.json":               "x",
		"vapid-keys.json":              "x",
		"home/.aws/sso/cache/tok.json": "x",
		"home/.ssh/id_rsa":             "x",
		"chats/c1.json":                "x",
		"kiro/steering/x.md":           "x",
		"visible.json":                 "x",
	})
	// A deny-listed directory is listed like the browser lists it (Blocks passes the container);
	// nothing inside it, and no deny-listed file, is ever a name row.
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{q: "json", want: []string{"/config/visible.json"}},
		{q: "mcp"},
		{q: "vapid"},
		{q: "push"},
		{q: "tok"},
		{q: "id_rsa"},
		{q: "ssh"},
		{q: "aws"},
		{q: "c1"},
		{q: "steering"},
		{q: "chats", want: []string{"/config/chats"}},
	} {
		t.Run(tc.q, func(t *testing.T) {
			res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "names", "path": "config", "q": tc.q}))
			if got := matchPaths(res); !slices.Equal(got, tc.want) {
				t.Errorf("names search %q under /config = %v, want %v", tc.q, got, tc.want)
			}
		})
	}
}

func TestSearch_SkipsBinaryAndNonRegular(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"text.txt":   "needle\n",
		"binary.bin": "needle\x00 with a NUL\n",
	})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "text.txt") {
		t.Fatalf("matches = %v, want only text.txt", got)
	}
	if res.Scanned != 2 {
		t.Errorf("scanned = %d, want 2 (text + binary read; the FIFO is name-only)", res.Scanned)
	}
	if res.Truncated {
		t.Error("truncated = true: a binary and a FIFO are skips, not losses")
	}
}

func TestSearch_SingleFileRoot(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"one.txt": "alpha\nneedle\n"})

	res := decodeSearch(t, searchReq(t, h,
		map[string]string{"mode": "contents", "path": filepath.Join(prefix, "one.txt"), "q": "needle"}))
	if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
		t.Fatalf("matches = %+v, want one hit on line 2", res.Matches)
	}
}

func TestSearch_FileRootMeetsTheFilesFilter(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"one.txt": "needle\n"})
	root := filepath.Join(prefix, "one.txt")

	for _, tc := range []struct {
		files string
		want  int
	}{
		{files: "!*.txt", want: 0},
		{files: "*.md", want: 0},
		{files: "one.txt", want: 1},
	} {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": root, "q": "needle", "files": tc.files}))
		if len(res.Matches) != tc.want || res.Scanned != tc.want {
			t.Errorf("files=%s matches = %d scanned = %d, want %d of each", tc.files, len(res.Matches), res.Scanned, tc.want)
		}
	}
}

func TestExcerptLine(t *testing.T) {
	long := strings.Repeat("a", 200) + "needle" + strings.Repeat("b", 200)
	tests := []struct {
		name    string
		line    string
		hitRune int
		want    string
	}{
		{name: "short line comes back whole", line: "  hello needle  ", hitRune: 8, want: "hello needle"},
		{name: "CRLF loses its CR", line: "needle\r", hitRune: 0, want: "needle"},
		{name: "multi-byte is not split", line: "héllo needle", hitRune: 6, want: "héllo needle"},
	}
	needle := textsearch.NewNeedle("needle", false)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := excerptLine(needle, tc.line, tc.hitRune); got != tc.want {
				t.Errorf("excerptLine(%q, %d) = %q, want %q", tc.line, tc.hitRune, got, tc.want)
			}
		})
	}

	t.Run("long line is windowed around the match", func(t *testing.T) {
		got, _ := excerptLine(needle, long, 200)
		if !strings.Contains(got, "needle") {
			t.Errorf("excerpt %q lost the match", got)
		}
		if !strings.HasPrefix(got, "\u2026") || !strings.HasSuffix(got, "\u2026") {
			t.Errorf("excerpt %q must be elided on both sides", got)
		}
		if len([]rune(got)) > 2*searchExcerptRadius+8 {
			t.Errorf("excerpt is %d runes, want roughly the window", len([]rune(got)))
		}
	})
}

// searchDirAt opens one directory the way the walk opens it: through the mount's
// own root handle, one component at a time, refusing a symlink at every step.
func searchDirAt(t *testing.T, h *Handler, abs string) *os.File {
	t.Helper()
	f, err := openPinnedRoot(loc{m: &h.mounts[0], abs: abs})
	if err != nil {
		t.Fatalf("openPinnedRoot(%q): %v", abs, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// allocatedBy reports how many bytes fn allocated. TotalAlloc is cumulative and
// unaffected by collection, so the measurement is stable; every assertion on it
// below compares against a bound orders of magnitude away from the value the
// defect produced, never against a tight budget.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestSearch_DirectoryLargerThanOneChunkIsFullyWalked(t *testing.T) {
	h, dir, prefix := testDir(t)
	// Matches deliberately in the SECOND ReadDir chunk: a loop stopping after the first read finds
	// none.
	const total = searchReadDirChunk + 7
	hitFrom, hitTo := searchReadDirChunk, searchReadDirChunk+5
	for i := range total {
		body := "nothing here\n"
		if i >= hitFrom && i < hitTo {
			body = "needle\n"
		}
		name := filepath.Join(dir, fmt.Sprintf("f%04d.txt", i))
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(res.Matches) != hitTo-hitFrom {
		t.Errorf("matches = %d, want %d: %v", len(res.Matches), hitTo-hitFrom, matchPaths(res))
	}
	if res.Scanned != total {
		t.Errorf("scanned = %d, want every one of the %d entries", res.Scanned, total)
	}
	if res.Truncated {
		t.Error("truncated = true on a directory well under every cap")
	}
}

// TestSearch_DepthCapStopsAndSaysSo pins the descriptor budget: the walk holds
// one directory handle per level of descent, so a tree deeper than the cap
// reports an incomplete answer rather than spending the process's descriptors.
func TestSearch_DepthCapStopsAndSaysSo(t *testing.T) {
	h, dir, prefix := testDir(t)
	deep := dir
	for range maxSearchDepth + 2 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "buried.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shallow := filepath.Join(dir, "shallow.txt")
	if err := os.WriteFile(shallow, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	got := matchPaths(res)
	if len(got) != 1 || got[0] != shallow {
		t.Fatalf("matches = %v, want only the shallow file", got)
	}
	if !res.Truncated {
		t.Error("truncated = false after refusing to descend: files were left unread")
	}
}

// atCeiling builds content of exactly maxSearchFileSize bytes whose final line
// is the needle, unterminated. A read that stopped one byte short would leave a
// word that no longer matches, which is the difference between a bounded read
// and a silently truncated one.
func atCeiling() string {
	const tail = "needle"
	return strings.Repeat("a", maxSearchFileSize-len(tail)-1) + "\n" + tail
}

// A file at exactly the per-file ceiling is searched, to its last byte, whether
// it is reached by walking a directory or named directly as the search root.
func TestSearch_FileAtExactCeilingIsSearchedWhole(t *testing.T) {
	t.Run("named_as_the_search_root", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"big.txt": atCeiling()})

		res := decodeSearch(t, searchReq(t, h,
			map[string]string{"mode": "contents", "path": filepath.Join(prefix, "big.txt"), "q": "needle"}))
		if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
			t.Fatalf("matches = %+v, want one hit on line 2 of a %d-byte file", res.Matches, maxSearchFileSize)
		}
		if res.Scanned != 1 {
			t.Errorf("scanned = %d, want 1: the root file was opened and read", res.Scanned)
		}
		if res.Truncated {
			t.Error("truncated = true: a file AT the ceiling was read whole, nothing was cut")
		}
	})

	t.Run("reached_by_walking_its_directory", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"sub/big.txt": atCeiling()})

		res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
		if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
			t.Fatalf("matches = %+v, want one hit on line 2 of a %d-byte file", res.Matches, maxSearchFileSize)
		}
		if res.Truncated {
			t.Error("truncated = true: a file AT the ceiling was read whole, nothing was cut")
		}
	})
}

// readSearchFile bounds the bytes it actually reads, off the descriptor: a file
// past the ceiling is read to the ceiling and REPORTED as partial, never refused
// and never passed off as whole.
func TestReadSearchFile_PastTheCeilingIsCutAndReportedPartial(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		wantRead    int
		wantPartial bool
	}{
		{name: "exactly_at_the_ceiling", size: maxSearchFileSize, wantRead: maxSearchFileSize, wantPartial: false},
		{name: "one_byte_past_the_ceiling", size: maxSearchFileSize + 1, wantRead: maxSearchFileSize, wantPartial: true},
		{name: "far_past_the_ceiling", size: 4 * maxSearchFileSize, wantRead: maxSearchFileSize, wantPartial: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(strings.Repeat("a", tc.size)), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			data, partial, err := readSearchFile(t.Context(), f, int64(tc.size))
			if err != nil {
				t.Fatalf("readSearchFile(a %d-byte file) error = %v, want nil: the ceiling is a bound, not a refusal", tc.size, err)
			}
			if len(data) != tc.wantRead {
				t.Errorf("readSearchFile(a %d-byte file) read %d bytes, want %d", tc.size, len(data), tc.wantRead)
			}
			// The descriptor's offset is what was read from disk, which the cut result cannot show.
			if off, seekErr := f.Seek(0, io.SeekCurrent); seekErr != nil || off > maxSearchFileSize+1 {
				t.Errorf("readSearchFile(a %d-byte file) left the offset at %d (err %v), want at most %d", tc.size, off, seekErr, maxSearchFileSize+1)
			}
			if partial != tc.wantPartial {
				t.Errorf("readSearchFile(a %d-byte file) partial = %v, want %v", tc.size, partial, tc.wantPartial)
			}
		})
	}
}

// A blank line is a line: the match after one is reported at the line it is
// really on, and everything past the blank line is still searched.
func TestSearch_BlankLinesAreCounted(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"gap.txt": "alpha\n\nneedle\n"})

	res := decodeSearch(t, searchReq(t, h,
		map[string]string{"mode": "contents", "path": filepath.Join(prefix, "gap.txt"), "q": "needle"}))
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly one hit", res.Matches)
	}
	if got := res.Matches[0].Line; got != 3 {
		t.Errorf("match line = %d, want 3 (the blank second line counts)", got)
	}
	if got := res.Matches[0].Excerpt; got != "needle" {
		t.Errorf("match excerpt = %q, want %q", got, "needle")
	}
}

// The depth budget names the deepest directory the walk still ENTERS: a file
// sitting at exactly that depth is found, and finding it is not a partial
// answer. The refusal one level deeper is pinned separately.
func TestSearch_FileAtExactDepthBudgetIsFound(t *testing.T) {
	h, dir, prefix := testDir(t)
	deep := dir
	for range maxSearchDepth {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	buried := filepath.Join(deep, "buried.txt")
	if err := os.WriteFile(buried, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if got := matchPaths(res); len(got) != 1 || got[0] != buried {
		t.Fatalf("matches = %v, want the file at depth %d", got, maxSearchDepth)
	}
	if res.Truncated {
		t.Error("truncated = true after a walk that read every file it should have")
	}
}

// TestSearch_DirectoryNameMatches: a folder is a first-class hit, and it shares
// the name rank, so it sorts ahead of its own matching child by path with no
// third comparator rule.
func TestSearch_DirectoryNameMatches(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle-dir/needle-child.txt": "nothing matching in here\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	want := []string{
		filepath.Join(dir, "needle-dir"),
		filepath.Join(dir, "needle-dir/needle-child.txt"),
	}
	if got := matchPaths(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("matches = %v, want the directory ahead of its child: %v", got, want)
	}
	if res.Matches[0].Kind != MatchKindDir {
		t.Errorf("directory kind = %q, want %q", res.Matches[0].Kind, MatchKindDir)
	}
	if res.Matches[1].Kind != MatchKindName {
		t.Errorf("child kind = %q, want %q", res.Matches[1].Kind, MatchKindName)
	}
}

// TestSearch_NameMatchHonoursCaseSensitivity: the name test reads the SAME
// folded needle the content test reads, so the toggle cannot mean two things.
func TestSearch_NameMatchHonoursCaseSensitivity(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"Needle-upper.txt": "no match inside\n",
		"needle-lower.txt": "no match inside\n",
	})
	lower := filepath.Join(dir, "needle-lower.txt")
	upper := filepath.Join(dir, "Needle-upper.txt")

	tests := map[string]struct {
		q        string
		caseFlag string
		want     []string
	}{
		"insensitive reaches both spellings": {
			q: "needle", want: []string{upper, lower},
		},
		"insensitive folds the needle as well as the name": {
			q: "NEEDLE", want: []string{upper, lower},
		},
		"case=1 with a lowercase needle reaches only the lowercase name": {
			q: "needle", caseFlag: "1", want: []string{lower},
		},
		"case=1 with an uppercase needle reaches only the uppercase name": {
			q: "Needle", caseFlag: "1", want: []string{upper},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			params := map[string]string{"path": prefix, "q": tc.q}
			if tc.caseFlag != "" {
				params["case"] = tc.caseFlag
			}
			got := matchPaths(decodeSearch(t, searchReq(t, h, params)))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSearch_ExcludeSkipsANameMatch: a name hit is only ever recorded for an
// entry classify ADMITTED, so every gate the content path applies applies here.
func TestSearch_ExcludeSkipsANameMatch(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle-keep.txt": "no match inside\n",
		"needle-drop.txt": "no match inside\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"path": prefix, "q": "needle", "files": "!needle-drop.txt",
	}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != filepath.Join(dir, "needle-keep.txt") {
		t.Fatalf("matches = %v, want only needle-keep.txt", got)
	}
}

// TestSearch_SensitivePathNeverMatchesByName: every sensitive file below is NAMED
// for the needle, so the name gate is the only thing that can keep it out. A name
// hit on one of these would disclose a path the content path is careful never to
// report.
func TestSearch_SensitivePathNeverMatchesByName(t *testing.T) {
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{
		"mcp-secrets.json":      "opaque\n",
		"mcp.json":              "server config\n",
		"push-subs.json":        "subscriptions\n",
		"vapid-keys.json":       "keys\n",
		"home/.ssh/id_rsa.json": "private key\n",
		"chats/c1.json":         "transcript\n",
		"kiro/steering/x.json":  "steering\n",
		"visible.json":          "a browsable file\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "config", "q": "json"}))

	got := matchPaths(res)
	if len(got) != 1 || got[0] != "/config/visible.json" {
		t.Fatalf("matches = %v, want only /config/visible.json", got)
	}
	if res.Matches[0].Kind != MatchKindName {
		t.Errorf("kind = %q, want %q", res.Matches[0].Kind, MatchKindName)
	}
}

// TestSearch_PerFileCapReportsTheCut: one file with more matching lines than a
// file may contribute answers the cap's worth of rows, counts every line, and
// leaves Truncated alone — a cap is not a hole in what was searched.
func TestSearch_PerFileCapReportsTheCut(t *testing.T) {
	h, dir, prefix := testDir(t)
	const lines = maxFileMatches + 5
	writeTree(t, dir, map[string]string{"many.txt": strings.Repeat("needle\n", lines)})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	if len(res.Matches) != maxFileMatches {
		t.Errorf("matches = %d, want the per-file cap %d", len(res.Matches), maxFileMatches)
	}
	if res.Matched != lines {
		t.Errorf("matched = %d, want every one of the %d matching lines", res.Matched, lines)
	}
	if res.Truncated {
		t.Error("truncated = true: the per-file cap is a cut the count reports, not a hole in the scan")
	}
}

// TestSearch_SearchRootIsNotItsOwnNameMatch: a root is the FRAME of the search
// rather than a result inside it, and a hit on it would open the folder the
// reader is already in. Both doors are covered — a resolved path, and each mount
// on the "/" fan-out.
func TestSearch_SearchRootIsNotItsOwnNameMatch(t *testing.T) {
	t.Run("a resolved search root", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		writeTree(t, dir, map[string]string{"needle-root/inner.txt": "no match inside\n"})

		res := decodeSearch(t, searchReq(t, h, map[string]string{
			"path": filepath.Join(prefix, "needle-root"), "q": "needle",
		}))
		if got := matchPaths(res); len(got) != 0 {
			t.Errorf("matches = %v, want none: the root itself is not a result", got)
		}
	})

	t.Run("each mount on the / fan-out", func(t *testing.T) {
		a := filepath.Join(t.TempDir(), "needle-mount-a")
		b := filepath.Join(t.TempDir(), "needle-mount-b")
		for _, root := range []string{a, b} {
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTree(t, root, map[string]string{"inner.txt": "no match inside\n"})
		}
		h, err := New(Sensitive{}, []string{a, b})
		if err != nil {
			t.Fatal(err)
		}

		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": "/", "q": "needle"}))
		if got := matchPaths(res); len(got) != 0 {
			t.Errorf("matches = %v, want none: a mount is a frame too", got)
		}
	})
}

// pinGolden marshals v, rewrites path behind UPDATE_GOLDEN=1, and compares the
// bytes. The failure names the regeneration command and the TypeScript consumer
// to re-run, because a cross-language fixture is one atomic change.
func pinGolden(t *testing.T, path string, v any, regen, consumer string) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run %s): %v", path, regen, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run %s, "+
			"then re-run the TS half: npx vitest --run %s (from static-src/).",
			path, want, got, regen, consumer)
	}
}

// contentWalk is a contents-mode walk over needle, driven below the handler.
func contentWalk(ctx context.Context, needle string, caseSensitive bool) (*searchWalk, *contentVisitor) {
	v := newContentVisitor(ctx, needle, caseSensitive)
	return &searchWalk{ctx: ctx, v: v, maxDirs: maxSearchDirs, ignoreRules: true, caseSensitive: caseSensitive}, v
}

var bothModes = []string{"names", "contents"}

func TestSearch_OutsideGrantedRootsRefused(t *testing.T) {
	h, _, _ := testDir(t)
	for _, mode := range bothModes {
		for _, p := range []string{"etc", "etc/passwd", "../etc/passwd"} {
			rec := searchReq(t, h, map[string]string{"mode": mode, "path": p, "q": "root"})
			if rec.Code != http.StatusForbidden {
				t.Errorf("mode=%s path=%q status = %d, want 403", mode, p, rec.Code)
			}
		}
	}
}

// The roots are resolved before anything else is read, so a malformed query outside the grants is
// still a 403.
func TestSearch_OutsideGrantedRootsRefusedBeforeValidation(t *testing.T) {
	h, _, _ := testDir(t)
	for name, params := range map[string]map[string]string{
		"malformed files pattern": {"q": "x", "files": "[a-"},
		"malformed query glob":    {"q": "*.[c"},
		"unknown mode":            {"q": "x", "mode": "regex"},
	} {
		params["path"] = "etc"
		if rec := searchReq(t, h, params); rec.Code != http.StatusForbidden {
			t.Errorf("%s outside the grants: status = %d, want 403", name, rec.Code)
		}
	}
}

func TestSearch_SymlinkOutOfMountNotWalked(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"own.txt": "needle here\n"})
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"needle-secret.txt": "needle outside\n"})
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}

	t.Run("contents", func(t *testing.T) {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
		if got := matchPaths(res); len(got) != 1 || got[0] != filepath.Join(dir, "own.txt") {
			t.Fatalf("matches = %v, want only own.txt", got)
		}
		if res.Truncated {
			t.Error("truncated = true: the symlink loop was followed until a cap absorbed it")
		}
	})
	t.Run("names", func(t *testing.T) {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
		if got := matchPaths(res); len(got) != 0 {
			t.Errorf("matches = %v, want none: the only needle-named file is behind a symlink out of the mount", got)
		}
		if res.Scanned != 3 || res.Truncated {
			t.Errorf("scanned = %d truncated = %v, want the three entries of the root and no loop", res.Scanned, res.Truncated)
		}
	})
}

func TestSearch_CancelledWritesNothing(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"needle.txt": "needle\n"})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, mode := range bothModes {
		rec := searchReqCtx(t, h, ctx, map[string]string{"mode": mode, "path": prefix, "q": "needle"})
		if rec.Body.Len() != 0 {
			t.Errorf("mode=%s body = %q, want empty on a cancelled request", mode, rec.Body.String())
		}
	}
}

func TestSearch_RootFansOutOverMounts(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeTree(t, a, map[string]string{"needle-a.txt": "needle\n"})
	writeTree(t, b, map[string]string{"needle-b.txt": "needle\n"})
	h, err := New(Sensitive{}, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range bothModes {
		for _, p := range []string{"/", "", "."} {
			res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": mode, "path": p, "q": "needle"}))
			if len(res.Matches) != 2 {
				t.Errorf("mode=%s path=%q matches = %v, want both mounts searched", mode, p, matchPaths(res))
			}
		}
	}
}

func TestSearch_MalformedRequestIsRejected(t *testing.T) {
	h, _, prefix := testDir(t)
	for name, params := range map[string]map[string]string{
		"malformed files pattern":            {"q": "x", "files": "[a-"},
		"malformed query glob":               {"q": "*.[c"},
		"too many query terms":               {"q": strings.Repeat("a ", maxFilterItems+1)},
		"unknown mode":                       {"q": "x", "mode": "regex"},
		"malformed filter in contents mode":  {"q": "x", "mode": "contents", "files": "{a"},
		"double star inside a files segment": {"q": "x", "files": "src/a**b/file.ts"},
		"double star inside a query term":    {"q": "a**b"},
	} {
		params["path"] = prefix
		if rec := searchReq(t, h, params); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (a bad pattern must not read as \"no match\")", name, rec.Code)
		}
	}
}

func TestSearch_OnlyGetIsAllowed(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/search?q=x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// TestMatchLines_ExcerptWindowFollowsTheOriginalBytes: a case fold can change a rune's byte length
// (U+212A KELVIN SIGN, three bytes, folds to a one-byte `k`), so an offset measured in the FOLDED
// line points somewhere else in the original. The window is placed by rune index, which the fold
// preserves.
func TestMatchLines_ExcerptWindowFollowsTheOriginalBytes(t *testing.T) {
	line := strings.Repeat("\u212a", 300) + "needle" + strings.Repeat("z", 50)
	v := newContentVisitor(t.Context(), "NEEDLE", false)

	hits, matched := v.matchLines("/x/kelvin.txt", line+"\n")
	if matched != 1 || len(hits) != 1 {
		t.Fatalf("matchLines = %d hits, matched %d, want one of each", len(hits), matched)
	}
	if !strings.Contains(hits[0].Excerpt, "needle") {
		t.Errorf("excerpt = %q, want it to hold the match: the window drifted off the hit", hits[0].Excerpt)
	}
}

// TestSearch_SwappedNameIsNotReadAfterAdmission asserts that between classifying an entry as a
// browsable regular file and reading its bytes, a name swapped for a link must not be read.
func TestSearch_SwappedNameIsNotReadAfterAdmission(t *testing.T) {
	const secret = "needle-secret-token"

	t.Run("the admitted file itself", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"mcp-secrets.json": `{"refresh_token":"` + secret + `"}`,
			"pub/notes.txt":    "needle in a browsable file\n",
		})
		dir := searchDirAt(t, h, "/config/pub")
		cand := searchCandidate{name: "notes.txt", abs: "/config/pub/notes.txt"}

		notes := filepath.Join(backing, "pub", "notes.txt")
		if err := os.Remove(notes); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../mcp-secrets.json", notes); err != nil {
			t.Fatal(err)
		}

		got := newContentVisitor(t.Context(), "needle", false).readCandidate(dir, cand)
		if len(got.hits) != 0 {
			t.Fatalf("read %d hits from a name swapped to a symlink: %+v", len(got.hits), got.hits)
		}
		if got.unread {
			t.Error("readCandidate reported a swap refusal as an unread file; a refused symlink is a deliberate skip, not a loss")
		}
	})

	t.Run("an ancestor of the admitted file", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"chats/c1.json": `{"messages":"` + secret + `"}`,
			"pub/notes.txt": "needle in a browsable file\n",
		})
		d := &walkDir{f: searchDirAt(t, h, "/config"), abs: "/config", pruneGit: true}

		pub := filepath.Join(backing, "pub")
		if err := os.Rename(pub, pub+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("chats", pub); err != nil {
			t.Fatal(err)
		}

		walk, v := contentWalk(t.Context(), "needle", false)
		if !walk.descend(d, &walkEntry{name: "pub", abs: "/config/pub", srel: "pub"}) {
			t.Error("a refused descent must skip the entry, not stop the whole scan")
		}
		if len(v.rows) != 0 {
			t.Fatalf("walked into a swapped ancestor and produced %+v", v.rows)
		}
	})

	// The premise, pinned so openChild keeps its reason: an *os.Root follows an in-mount symlink,
	// and passing O_NOFOLLOW to Root.OpenFile does not stop it.
	t.Run("the mount root follows an in-root symlink", func(t *testing.T) {
		h, backing := searchHandlerAt(t, "/config")
		writeTree(t, backing, map[string]string{
			"mcp-secrets.json": `{"refresh_token":"` + secret + `"}`,
		})
		if err := os.Symlink("mcp-secrets.json", filepath.Join(backing, "decoy.txt")); err != nil {
			t.Fatal(err)
		}
		root := h.mounts[0].root

		data, err := atomicfile.ReadBoundedInRoot(t.Context(), root, "decoy.txt", maxSearchFileSize)
		if err != nil || !strings.Contains(string(data), secret) {
			t.Fatalf("confined read of a symlink = %q, %v; want the target's bytes", data, err)
		}
		f, err := root.OpenFile("decoy.txt", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatalf("Root.OpenFile with O_NOFOLLOW refused a symlink: %v", err)
		}
		_ = f.Close()

		dir := searchDirAt(t, h, "/config")
		if got, openErr := openChild(dir, "decoy.txt", "/config/decoy.txt", pinnedFileFlags); openErr == nil {
			_ = got.Close()
			t.Error("openChild followed a symlink; the search's whole confinement rests on it not doing that")
		} else if !isSwapRefusal(openErr) {
			t.Errorf("openChild refused with %v, want the kernel's symlink refusal", openErr)
		}
	})
}

func TestSearch_HugeDirectoryIsNotReadBeforeCancellationIsChecked(t *testing.T) {
	h, dir, _ := testDir(t)
	const entries = 20_000
	for i := range entries {
		name := filepath.Join(dir, fmt.Sprintf("e%05d.bin", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	walk, v := contentWalk(ctx, "needle", false)

	grew := allocatedBy(func() { walk.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	const bound = 256 << 10
	if grew > bound {
		t.Errorf("a cancelled scan allocated %d bytes over a %d-entry directory, want under %d: "+
			"the directory was read before the context was consulted", grew, entries, bound)
	}
	if v.files != 0 {
		t.Errorf("files = %d, want 0 on an already-cancelled scan", v.files)
	}
}

func TestSearch_BinaryIsRejectedBeforeItsBytesAreRead(t *testing.T) {
	h, dir, _ := testDir(t)
	// One binary per read worker at the per-file ceiling: reading before sniffing would hold
	// searchWorkers * maxSearchFileSize of unreported bytes.
	payload := make([]byte, maxSearchFileSize)
	copy(payload, "needle\x00binary")
	for i := range searchWorkers {
		name := filepath.Join(dir, fmt.Sprintf("b%02d.bin", i))
		if err := os.WriteFile(name, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	walk, v := contentWalk(t.Context(), "needle", false)
	grew := allocatedBy(func() { walk.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	if v.files != searchWorkers {
		t.Fatalf("files = %d, want %d (every binary is opened and counted)", v.files, searchWorkers)
	}
	if v.matched != 0 {
		t.Errorf("matched = %d, want 0: a match inside a binary is not reportable", v.matched)
	}
	bound := uint64(searchWorkers*binarySniffN) + (256 << 10)
	if grew > bound {
		t.Errorf("scanning %d ceiling-sized binaries allocated %d bytes, want under %d: "+
			"the bytes were read before the sniff decided they were unreportable",
			searchWorkers, grew, bound)
	}
}

// A file at the ceiling is read into ONE buffer that the scan then reads as a string without
// copying, so a worker holds about one ceiling and searchReadBudget bounds the fan-out.
// Case-sensitive, so the fold allocates nothing and the read is the only thing measured.
func TestSearch_TextFileIsReadIntoOneBuffer(t *testing.T) {
	h, dir, _ := testDir(t)
	writeTree(t, dir, map[string]string{"big.txt": atCeiling()})

	walk, v := contentWalk(t.Context(), "needle", true)
	grew := allocatedBy(func() { walk.addRoot(loc{m: &h.mounts[0], abs: dir}) })

	if v.files != 1 || v.matched != 1 {
		t.Fatalf("files = %d, matched = %d, want 1 and 1 (the file was read to its last line)", v.files, v.matched)
	}
	const bound = maxSearchFileSize + (256 << 10)
	if grew > bound {
		t.Errorf("reading one %d-byte text file allocated %d bytes, want under %d: the file was copied after it was read",
			maxSearchFileSize, grew, bound)
	}
}

// The reply budget is INCLUSIVE: a reply holding exactly maxSearchMatches rows is full, and the
// walk stops at the entry in hand.
func TestContentVisitor_ReplyBudgetIsInclusive(t *testing.T) {
	for _, tc := range []struct {
		rows int
		want bool
	}{{rows: 0, want: false}, {rows: maxSearchMatches - 1, want: false}, {rows: maxSearchMatches, want: true}} {
		v := newContentVisitor(t.Context(), "needle", false)
		v.rows = make([]FileMatch, tc.rows)
		if got := v.full(); got != tc.want {
			t.Errorf("full() with %d rows = %v, want %v", tc.rows, got, tc.want)
		}
	}
}

// The directory budget names the last directory the walk still ENTERS: the directory that spends
// it is listed and its files read, and the one after it is refused before its listing, which is
// what Truncated reports. Driven at the counter, because the real budget is 20 000 directories.
func TestWalkDir_DirectoryBudgetIsInclusive(t *testing.T) {
	tests := []struct {
		name          string
		dirs          int
		wantWalked    bool
		wantMatches   int
		wantTruncated bool
	}{
		{name: "the_last_directory_of_the_budget", dirs: maxSearchDirs - 1, wantWalked: true, wantMatches: 1, wantTruncated: false},
		{name: "one_directory_past_the_budget", dirs: maxSearchDirs, wantWalked: false, wantMatches: 0, wantTruncated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("needle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}

			walk, v := contentWalk(t.Context(), "needle", false)
			walk.dirs = tc.dirs
			if got := walk.walk(&walkDir{f: f, abs: dir, pruneGit: true}); got != tc.wantWalked {
				t.Errorf("walk() with dirs=%d = %v, want %v", tc.dirs, got, tc.wantWalked)
			}
			res := v.result(walk.truncated)
			if len(res.Matches) != tc.wantMatches {
				t.Errorf("walk() with dirs=%d found %d rows, want %d: %+v", tc.dirs, len(res.Matches), tc.wantMatches, res.Matches)
			}
			if res.Truncated != tc.wantTruncated {
				t.Errorf("walk() with dirs=%d left truncated = %v, want %v", tc.dirs, res.Truncated, tc.wantTruncated)
			}
		})
	}
}

func TestContentSearch_ReturnsNoNameRows(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"needle.txt":     "nothing matching inside this one\n",
		"needle-dir/x.c": "int x;\n",
		"prose.txt":      "the needle is in here\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want only the one content row", res.Matches)
	}
	if m := res.Matches[0]; m.Kind != MatchKindContent || m.Path != filepath.Join(dir, "prose.txt") || m.Line != 1 {
		t.Errorf("row = %+v, want prose.txt line 1 as a content row", m)
	}
}

func TestContentSearch_FilterGatesBeforeRead(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"a.go":         "needle\n",
		"src/b.go":     "needle\n",
		"src/c.md":     "needle\n",
		"docs/d.md":    "needle\n",
		"docs/e.go.md": "needle\n",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle", "files": ".go"}))
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "src/b.go")}
	if got := matchPaths(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("matches = %v, want %v", got, want)
	}
	if res.Scanned != 2 {
		t.Errorf("scanned = %d, want 2: a file the filter refuses is never read", res.Scanned)
	}
}

func TestContentSearch_CutIsAPathOrderPrefix(t *testing.T) {
	h, dir, prefix := testDir(t)
	tree := map[string]string{
		"a.txt": strings.Repeat("needle\n", 5),
		"b.txt": strings.Repeat("needle\n", maxFileMatches),
	}
	for i := range 15 {
		tree[fmt.Sprintf("a/f%02d.txt", i)] = strings.Repeat("needle\n", maxFileMatches)
	}
	writeTree(t, dir, tree)

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	if len(res.Matches) != maxSearchMatches {
		t.Fatalf("matches = %d, want the cap %d", len(res.Matches), maxSearchMatches)
	}
	first, sixth, last := res.Matches[0], res.Matches[5], res.Matches[maxSearchMatches-1]
	if first.Path != filepath.Join(dir, "a.txt") || first.Line != 1 {
		t.Errorf("row 0 = %s:%d, want a.txt:1 (a.txt sorts before the a/ folder)", first.Path, first.Line)
	}
	if sixth.Path != filepath.Join(dir, "a/f00.txt") || sixth.Line != 1 {
		t.Errorf("row 5 = %s:%d, want a/f00.txt:1", sixth.Path, sixth.Line)
	}
	if last.Path != filepath.Join(dir, "a/f09.txt") || last.Line != 15 {
		t.Errorf("row 199 = %s:%d, want a/f09.txt:15", last.Path, last.Line)
	}
	if !slices.IsSortedFunc(res.Matches, func(a, b FileMatch) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	}) {
		t.Error("rows are not in path order")
	}
	if res.Matched != 5+15*maxFileMatches || !res.Truncated {
		t.Errorf("matched = %d truncated = %v, want %d and true: b.txt was never reached", res.Matched, res.Truncated, 5+15*maxFileMatches)
	}
}

func TestContentSearch_EmptyQueryDoesNotWalkEvenWithAFilter(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"a.go": "package a\n"})

	rec := searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "", "files": "*.go"})
	res := decodeSearch(t, rec)
	if len(res.Matches) != 0 || res.Scanned != 0 || res.Truncated {
		t.Errorf("result = %+v, want an empty scan", res)
	}
	if !strings.Contains(rec.Body.String(), `"matches":[]`) {
		t.Error("body must carry an empty array, not null")
	}
}

func TestContentSearch_NeedleKeepsItsEdgeSpaces(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"a.txt": "x needle\nneedle y\na  b\n"})

	for _, tc := range []struct {
		q    string
		line int
	}{
		{q: " needle", line: 1},
		{q: "needle ", line: 2},
		{q: "  ", line: 3},
	} {
		res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": tc.q}))
		if len(res.Matches) != 1 || res.Matches[0].Line != tc.line {
			t.Errorf("q=%q matches = %+v, want only line %d", tc.q, res.Matches, tc.line)
		}
	}
}

// A needle that begins, ends or consists of whitespace is shown and marked whole: the excerpt's
// trim stops at the match.
func TestContentSearch_EdgeWhitespaceMatchStaysInTheExcerpt(t *testing.T) {
	for _, tc := range []struct {
		name, line, q, excerpt string
		ranges                 []MatchRange
	}{
		{name: "leading_space_at_column_one", line: " needle tail", q: " needle", excerpt: " needle tail", ranges: []MatchRange{{Start: 0, End: 7}}},
		{name: "trailing_space_at_line_end", line: "head needle ", q: "needle ", excerpt: "head needle ", ranges: []MatchRange{{Start: 5, End: 12}}},
		{name: "all_whitespace_line", line: "    ", q: "  ", excerpt: "    ", ranges: []MatchRange{{Start: 0, End: 2}, {Start: 2, End: 4}}},
		{name: "unmatched_edges_still_trimmed", line: "  a  b  ", q: "  b", excerpt: "a  b", ranges: []MatchRange{{Start: 1, End: 4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			writeTree(t, dir, map[string]string{"a.txt": tc.line + "\n"})

			res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": tc.q}))
			if len(res.Matches) != 1 {
				t.Fatalf("q=%q over %q: matches = %+v, want one row", tc.q, tc.line, res.Matches)
			}
			if got := res.Matches[0].Excerpt; got != tc.excerpt {
				t.Errorf("q=%q over %q: excerpt = %q, want %q", tc.q, tc.line, got, tc.excerpt)
			}
			if got := res.Matches[0].Ranges; !slices.Equal(got, tc.ranges) {
				t.Errorf("q=%q over %q: ranges = %v, want %v", tc.q, tc.line, got, tc.ranges)
			}
		})
	}
}

// A needle longer than the excerpt radius still fits the window whole, so its row keeps its mark.
func TestExcerptLine_LongNeedleKeepsItsRange(t *testing.T) {
	long := strings.Repeat("x", 2*searchExcerptRadius)
	line := strings.Repeat("a", 3*searchExcerptRadius) + long + strings.Repeat("b", 3*searchExcerptRadius)

	excerpt, ranges := excerptLine(textsearch.NewNeedle(long, true), line, 3*searchExcerptRadius)
	if len(ranges) != 1 {
		t.Fatalf("excerptLine over a %d-rune needle: ranges = %v, want one", len(long), ranges)
	}
	if got := string(utf16.Decode(utf16.Encode([]rune(excerpt))[ranges[0].Start:ranges[0].End])); got != long {
		t.Errorf("excerpt[%d:%d] = %q, want the needle", ranges[0].Start, ranges[0].End, got)
	}
}

func TestContentSearch_RangesMarkEveryOccurrenceInTheExcerpt(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"x.txt": "\U0001F3AF needle, NEEDLE and n\u00e9edle needle\n"})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want one row", res.Matches)
	}
	want := []MatchRange{{Start: 3, End: 9}, {Start: 11, End: 17}, {Start: 29, End: 35}}
	if got := res.Matches[0].Ranges; !slices.Equal(got, want) {
		t.Errorf("ranges = %v, want %v: UTF-16 offsets into %q, the astral target counting two", got, want, res.Matches[0].Excerpt)
	}
}

type fileSearchRun struct {
	Query  string           `json:"query"`
	Mode   string           `json:"mode"`
	Result FileSearchResult `json:"result"`
}

type fileSearchFixture struct {
	Comment  []string      `json:"_comment"`
	Names    fileSearchRun `json:"names"`
	Contents fileSearchRun `json:"contents"`
}

var fileSearchFixtureComment = []string{
	"Two GET /api/files/search replies, produced by the real handler over one seeded tree",
	"mounted at /workspace: the names mode and the contents mode.",
	"",
	"filebrowse.FileSearchResult, FileMatch, MatchRange and FileMatchKind are wiregen-registered;",
	"TestFileSearchWireContract (Go) asserts the handler's replies marshal to exactly these",
	"bytes, and files-search.node.test.ts (TypeScript) decodes them through the generated",
	"decodeFileSearchResult. The names reply holds a dir row and two name rows, one whose",
	"basename starts with an astral character so its range is in UTF-16 units. The contents",
	"reply cuts one file at the per-file cap, so `matched` exceeds the row count.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/filebrowse/ -run TestFileSearchWireContract",
	"then re-run the TS half: npx vitest --run files-search.node.test.ts (from static-src/).",
}

// TestFileSearchWireContract pins the marshaled shape of both modes' replies over one tree.
func TestFileSearchWireContract(t *testing.T) {
	h, backing := searchHandlerAt(t, "/workspace")
	manyLines := maxFileMatches + 1
	writeTree(t, backing, map[string]string{
		"needle-dir/inner.txt":  "nothing in here\n",
		"notes-needle.md":       "# notes\n\nthe needle is on line three\n",
		"many.txt":              strings.Repeat("needle again\n", manyLines),
		"plain.txt":             "nothing matching\n",
		"\U0001F3AF-needle.txt": "a needle and another needle\n",
	})

	fx := fileSearchFixture{Comment: fileSearchFixtureComment}
	fx.Names = fileSearchRun{Query: "needle", Mode: "names"}
	fx.Names.Result = decodeSearch(t, searchReq(t, h, map[string]string{"path": "workspace", "q": "needle"}))
	fx.Contents = fileSearchRun{Query: "needle", Mode: "contents"}
	fx.Contents.Result = decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": "workspace", "q": "needle"}))

	names := fx.Names.Result
	if len(names.Matches) != 3 || names.Matches[0].Kind != MatchKindDir || names.Scanned != 6 {
		t.Fatalf("names reply = %+v, want the dir row first of three over 6 entries", names)
	}
	contents := fx.Contents.Result
	if wantRows := maxFileMatches + 2; len(contents.Matches) != wantRows {
		t.Fatalf("contents rows = %d, want %d: %+v", len(contents.Matches), wantRows, contents.Matches)
	}
	if want := manyLines + 2; contents.Matched != want || contents.Scanned != 5 || contents.Truncated {
		t.Fatalf("contents tally = %+v, want matched %d, scanned 5, not truncated", contents.Tally, want)
	}

	pinGolden(t, "testdata/file_search.json", fx, "TestFileSearchWireContract", "files-search.node.test.ts")
}
