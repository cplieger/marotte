package preview

import (
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
)

// newDenyFixture serves workDir with the deny list rooted at configDir, the
// shape of a deployment whose work dir holds the config dir (KIRO_WORK_DIR=/).
func newDenyFixture(t *testing.T, workDir, configDir string) *fixture {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	h := New(workDir, filebrowse.NewSensitive(configDir), newTestSigner(t, now), slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &fixture{h: h, mux: mux, ws: workDir, now: now}
}

func TestGrant_RefusesAFolderThatExposesTheDenyList(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "state", "config")
	write(t, filepath.Join(cfg, "mcp.json"), `{"mcpServers":{}}`)
	f := newDenyFixture(t, root, cfg)
	cases := []struct{ name, page string }{
		{"inside a listed directory", filepath.Join(cfg, "home", "proj", "page.html")},
		{"folder enclosing listed files", filepath.Join(cfg, "page.html")},
		{"folder enclosing the config dir", filepath.Join(root, "state", "page.html")},
		{"folder named as a listed file's copy", filepath.Join(cfg, "mcp.json.d", "page.html")},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			write(t, tc.page, "<p>page")
			if got, body := f.grantStatus(tc.page); got != http.StatusForbidden || !strings.Contains(body, "configuration or credentials") {
				t.Errorf("grant(%s) = %d %s, want 403 naming the configuration", tc.name, got, body)
			}
			if rec := f.do(http.MethodGet, "/api/preview/stamp?path="+url.QueryEscape(tc.page), ""); rec.Code != http.StatusForbidden {
				t.Errorf("stamp(%s) = %d %s, want 403", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

func TestGrant_ServesAnOrdinaryPageBesideTheConfigDir(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "state", "config")
	write(t, filepath.Join(cfg, "mcp.json"), `{"mcpServers":{}}`)
	page := filepath.Join(root, "demo", "index.html")
	write(t, page, "<p>ordinary page")
	f := newDenyFixture(t, root, cfg)
	g := f.grant(t, page)
	if rec := f.do(http.MethodGet, g.URL, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ordinary page") {
		t.Errorf("GET %s = %d %q, want 200 with the page", g.URL, rec.Code, rec.Body)
	}
}

// The no-follow walk starts at the work dir's file descriptor, so a work dir that
// is itself a symlink resolves elsewhere than its spelling says.
func TestGrant_RefusesAWorkDirSymlinkedIntoTheDenyList(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	page := filepath.Join(cfg, "home", "proj", "page.html")
	write(t, page, "<p>page")
	link := filepath.Join(root, "work")
	symlink(t, filepath.Join(cfg, "home"), link)
	f := newDenyFixture(t, link, cfg)
	if got, body := f.grantStatus(filepath.Join(link, "proj", "page.html")); got != http.StatusForbidden {
		t.Errorf("grant(work/proj/page.html) = %d %s, want 403: the folder resolves into %s/home", got, body, cfg)
	}
}

// A work dir spelled under the config dir is refused by its spelling even where it
// resolves elsewhere, as the file browser refuses both forms.
func TestGrant_RefusesAWorkDirSpelledInsideTheDenyList(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	elsewhere := filepath.Join(root, "elsewhere")
	write(t, filepath.Join(elsewhere, "proj", "page.html"), "<p>page")
	write(t, filepath.Join(cfg, "home", ".keep"), "")
	link := filepath.Join(cfg, "home", "work")
	symlink(t, elsewhere, link)
	f := newDenyFixture(t, link, cfg)
	if got, body := f.grantStatus(filepath.Join(link, "proj", "page.html")); got != http.StatusForbidden {
		t.Errorf("grant(config/home/work/proj/page.html) = %d %s, want 403: the folder is spelled under %s/home", got, body, cfg)
	}
}

func TestServe_RefusesATokenForADenyListedFolder(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	folder := filepath.Join(cfg, "home", "proj")
	write(t, filepath.Join(folder, "secret.txt"), "deny-listed-bytes")
	f := newDenyFixture(t, root, cfg)
	base := PathPrefix + f.h.signer.Mint(folder, f.now.Add(time.Hour)) + "/"
	if rec := f.do(http.MethodGet, base+"secret.txt", ""); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "deny-listed-bytes") {
		t.Errorf("GET %ssecret.txt = %d %q, want 403 without the file", base, rec.Code, rec.Body)
	}
}
