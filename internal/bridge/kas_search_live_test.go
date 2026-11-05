package bridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/kirosearch"
	"github.com/cplieger/marotte/internal/marotte"
)

var kasSearch = flag.Bool("kas-search", false,
	"run TestKASSendsSearchToTheClient: drive the kiro-cli named by MAROTTE_LIVE_KIRO_CLI through grep_search and file_search")

// search answers KAS's search requests the way internal/agent does, over the capture workspace.
func (c *capture) search(msg *marotte.RPCResponse) (any, error) {
	bin, err := exec.LookPath("rg")
	if err != nil {
		bin = ""
	}
	s := kirosearch.Searcher{Bin: bin}
	roots := []string{c.work}
	if msg.Method == "_kiro/search/text_search" {
		var p struct {
			Pattern        string `json:"pattern"`
			IncludePattern string `json:"includePattern"`
			ExcludePattern string `json:"excludePattern"`
			CaseSensitive  bool   `json:"caseSensitive"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		return map[string]any{"matches": s.Text(context.Background(), roots, kirosearch.TextQuery{
			Pattern: p.Pattern, IncludePattern: p.IncludePattern, ExcludePattern: p.ExcludePattern, CaseSensitive: p.CaseSensitive,
		})}, nil
	}
	var p struct {
		Pattern    string `json:"pattern"`
		Exclude    string `json:"exclude"`
		MaxResults int    `json:"maxResults"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	return map[string]any{"files": s.Files(context.Background(), roots, kirosearch.FilesQuery{
		Pattern: p.Pattern, Exclude: p.Exclude, MaxResults: p.MaxResults,
	})}, nil
}

// TestKASSendsSearchToTheClient proves the pinned kiro-cli sends grep_search and file_search to the
// client once marotte advertises textSearch and findFiles, and that KAS logs no rg fallback. It
// spends model credits, so it needs MAROTTE_LIVE_KIRO_CLI and -kas-search.
func TestKASSendsSearchToTheClient(t *testing.T) {
	bin := os.Getenv("MAROTTE_LIVE_KIRO_CLI")
	if bin == "" {
		t.Skip("MAROTTE_LIVE_KIRO_CLI names no kiro-cli binary")
	}
	if !*kasSearch {
		t.Skip("-kas-search not set: the test spends credits")
	}
	pin := pinnedKiroCLI(t)
	checkBinaryVersion(t, bin, pin)
	c := newCapture(t, &captureEnv{cli: bin, kiroCLI: pin})
	ctx, cancel := context.WithTimeout(t.Context(), 2*turnBudget)
	defer cancel()
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		t.Fatal(err)
	}
	s, lane, err := c.newSession(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prompt(ctx, s, lane, "Call the grep_search tool with query \"release train\", then the file_search tool with query \"notes\". "+
		"Use no other tool. Reply with the file paths each returned."); err != nil {
		t.Fatal(err)
	}
	c.shutdown()

	answered := map[string]string{}
	c.mu.Lock()
	for _, l := range c.lines {
		var f struct {
			Method string `json:"method"`
		}
		if l.Dir == "in" && json.Unmarshal(l.Frame, &f) == nil && strings.HasPrefix(f.Method, "_kiro/search/") {
			answered[f.Method] = string(l.Frame)
		}
	}
	c.mu.Unlock()
	for _, m := range []string{"_kiro/search/text_search", "_kiro/search/find_files"} {
		if _, ok := answered[m]; !ok {
			t.Errorf("KAS never sent %s to the client", m)
		} else {
			t.Logf("%s: %s", m, answered[m])
		}
	}
	walkErr := filepath.WalkDir(c.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".log") {
			return err
		}
		t.Logf("checked KAS log %s", p)
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, bad := range []string{"ripgrep failed", "ripgrep --files failed", "extMethod call failed"} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("%s logs %q: KAS fell back to its own search", p, bad)
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		t.Errorf("reading KAS's logs: %v", walkErr)
	}
}
