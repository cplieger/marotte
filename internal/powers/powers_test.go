package powers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const registryBody = `{"schemaVersion":"1","powers":[
 {"name":"postman","displayName":"API Testing\u202ewith Postman","description":"Line one\nline two",
  "repositoryUrl":"https://github.com/kirodotdev/powers/tree/main/postman","repositoryBranch":"main","pathInRepo":"postman"},
 {"name":"Bad Name","displayName":"dropped"},
 {"name":"evil","repositoryUrl":"https://example.com/x/y","repositoryBranch":"main","pathInRepo":"p"}
]}`

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"postman": true, "aws.cdk": true, "a-b9": true, "a": true,
		"": false, "A": false, "-a": false, "a-": false, "a--b": false, "a..b": false,
		"a/b": false, "..": false, strings.Repeat("a", 65): false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRawBase(t *testing.T) {
	for _, tc := range []struct{ repo, branch, path, want string }{
		{
			"https://github.com/kirodotdev/powers/tree/main/postman", "main", "postman",
			"https://raw.githubusercontent.com/kirodotdev/powers/main/postman",
		},
		{"https://github.com/o/r", "dev", "", "https://raw.githubusercontent.com/o/r/dev"},
		{"https://github.com/o/r", "main", "a/../b", ""},
		{"https://example.com/o/r", "main", "p", ""},
		{"http://github.com/o/r", "main", "p", ""},
		{"https://github.com/o", "main", "p", ""},
		{"https://github.com/o/r", "", "p", ""},
		{"https://github.com/o/r", "main", "a b", "https://raw.githubusercontent.com/o/r/main/a%20b"},
	} {
		if got := rawBase(tc.repo, tc.branch, tc.path); got != tc.want {
			t.Errorf("rawBase(%q, %q, %q) = %q, want %q", tc.repo, tc.branch, tc.path, got, tc.want)
		}
	}
}

func TestCatalog_FetchesSanitizesAndCaches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(registryBody))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	entries, err := c.Entries(t.Context())
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "postman" || entries[1].Name != "evil" {
		t.Fatalf("Entries = %+v, want postman and evil (the invalid name dropped)", entries)
	}
	if strings.ContainsRune(entries[0].DisplayName, '\u202e') || strings.Contains(entries[0].Description, "\n") {
		t.Errorf("display strings not sanitized: %q / %q", entries[0].DisplayName, entries[0].Description)
	}
	if entries[1].rawBase != "" {
		t.Errorf("a non-GitHub repository resolved to %q", entries[1].rawBase)
	}
	if _, err := c.Entries(t.Context()); err != nil {
		t.Fatalf("second Entries: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("registry fetched %d times, want 1 inside the cache window", hits.Load())
	}
}

func TestCatalog_AFailedRefetchKeepsTheLastGoodCopy(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(registryBody))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	if _, err := c.Entries(t.Context()); err != nil {
		t.Fatalf("Entries: %v", err)
	}
	fail.Store(true)
	c.fetchedAt = c.fetchedAt.Add(-2 * catalogTTL)
	entries, err := c.Entries(t.Context())
	if err != nil || len(entries) != 2 {
		t.Errorf("Entries after a failed refetch = %d, %v; want the cached 2, nil", len(entries), err)
	}
	before := hits.Load()
	if entries, err := c.Entries(t.Context()); err != nil || len(entries) != 2 {
		t.Errorf("Entries inside the failure backoff = %d, %v; want the cached 2, nil", len(entries), err)
	}
	if hits.Load() != before {
		t.Errorf("a call inside the failure backoff fetched again (%d requests, want %d)", hits.Load(), before)
	}

	cold := NewCatalog(srv.Client(), srv.URL)
	if _, err := cold.Entries(t.Context()); err == nil {
		t.Errorf("a cold catalogue answered a failed fetch with no error")
	}
	before = hits.Load()
	if _, err := cold.Entries(t.Context()); err == nil || hits.Load() != before {
		t.Errorf("a cold catalogue inside the failure backoff = %v after %d new requests; want the error and none",
			err, hits.Load()-before)
	}
}

func TestCatalog_AStaleCopyIsServedWhileOneCallerRefreshes(t *testing.T) {
	var hits atomic.Int32
	var block atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
		_, _ = w.Write([]byte(registryBody))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	if _, err := c.Entries(t.Context()); err != nil {
		t.Fatalf("Entries: %v", err)
	}
	c.fetchedAt = c.fetchedAt.Add(-2 * catalogTTL)
	block.Store(true)
	refreshed := make(chan error, 1)
	go func() {
		_, err := c.Entries(t.Context())
		refreshed <- err
	}()
	<-entered

	got := make(chan int, 1)
	go func() {
		entries, _ := c.Entries(t.Context())
		got <- len(entries)
	}()
	select {
	case n := <-got:
		if n != 2 {
			t.Errorf("a caller during the refresh got %d entries, want the stale 2", n)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("a caller during the refresh waited for it instead of taking the stale copy")
	}
	close(release)
	if err := <-refreshed; err != nil {
		t.Errorf("the refreshing caller: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("registry fetched %d times, want 2 (the first read and one refresh)", hits.Load())
	}
}

func TestCatalog_ConcurrentColdCallersShareOneFetch(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-release
		_, _ = w.Write([]byte(registryBody))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	const callers = 4
	errs := make(chan error, callers)
	for range callers {
		go func() {
			_, err := c.Entries(t.Context())
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no fetch reached the registry")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range callers {
		if err := <-errs; err != nil {
			t.Errorf("Entries: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("registry fetched %d times for %d concurrent cold callers, want 1", hits.Load(), callers)
	}
}

func TestCatalog_RefusesAnOversizedRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Valid JSON once truncated, so only the size check can refuse it.
		_, _ = w.Write([]byte(`{"powers":[{"name":"a"}]}` + strings.Repeat(" ", maxRegistryBytes)))
	}))
	defer srv.Close()
	if _, err := NewCatalog(srv.Client(), srv.URL).Entries(t.Context()); err == nil {
		t.Errorf("an oversized registry was decoded")
	}
}

func TestCatalog_ServersReadsThePowersMCPFile(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"mcpServers":{"b":{},"a":{}}}`))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	e := Entry{Name: "x", rawBase: srv.URL + "/o/r/main/x"}
	names, known := c.Servers(t.Context(), &e)
	if !known || !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("Servers = %v, %v; want [a b], true", names, known)
	}
	if path != "/o/r/main/x/mcp.json" {
		t.Errorf("fetched %q, want the Power's mcp.json", path)
	}
	if names, known := c.Servers(t.Context(), &Entry{Name: "y"}); known || names != nil {
		t.Errorf("an unresolvable repository reported %v, %v", names, known)
	}
}

func TestCatalog_ServersSeesAChangedMCPFileOnceTheCacheAges(t *testing.T) {
	var body atomic.Value
	body.Store(`{"mcpServers":{}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	e := Entry{Name: "x", rawBase: srv.URL + "/o/r/main/x"}
	if names, _ := c.Servers(t.Context(), &e); len(names) != 0 {
		t.Fatalf("Setup: Servers = %v, want none", names)
	}
	body.Store(`{"mcpServers":{"added":{}}}`)
	if names, _ := c.Servers(t.Context(), &e); len(names) != 0 {
		t.Errorf("Servers within serversTTL = %v, want the cached empty list", names)
	}
	c.mu.Lock()
	c.servers["x"] = serverList{at: time.Now().Add(-serversTTL - time.Second)}
	c.mu.Unlock()
	if names, known := c.Servers(t.Context(), &e); !known || !slices.Equal(names, []string{"added"}) {
		t.Errorf("Servers after the cache aged = %v, %v; want [added], true", names, known)
	}
}

// toServer sends every request to srv, keeping its path, so a catalogue entry's
// raw.githubusercontent.com URL reaches the test's own handler.
type toServer struct{ srv *httptest.Server }

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = "http", s.srv.Listener.Addr().String(), ""
	return http.DefaultTransport.RoundTrip(r)
}

func TestManager_AConfirmationReadsTheRegistryAsItIsNow(t *testing.T) {
	var repo atomic.Value
	repo.Store("first")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry":
			fmt.Fprintf(w, `{"powers":[{"name":"x","repositoryUrl":"https://github.com/o/%s","repositoryBranch":"main","pathInRepo":"x"}]}`, repo.Load())
		case "/o/first/main/x/mcp.json":
			_, _ = w.Write([]byte(`{"mcpServers":{"old":{}}}`))
		case "/o/second/main/x/mcp.json":
			_, _ = w.Write([]byte(`{"mcpServers":{"new":{}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: toServer{srv}}
	m := NewManager(NewCatalog(client, srv.URL+"/registry"), &recordingWriter{}, nil, nil, t.TempDir())
	if names, known, err := m.Servers(t.Context(), "x"); err != nil || !known || !slices.Equal(names, []string{"old"}) {
		t.Fatalf("Setup: Servers(x) = %v, %v, %v; want [old]", names, known, err)
	}
	repo.Store("second")
	names, known, err := m.Servers(t.Context(), "x")
	if err != nil || !known || !slices.Equal(names, []string{"new"}) {
		t.Errorf("Servers(x) after the registry moved the Power = %v, %v, %v; want [new], true, nil", names, known, err)
	}
}

func TestCatalog_AdmitReusesTheConfirmationsFetchAndRefreshesAnOlderOne(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = w.Write([]byte(registryBody))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	if _, ok, err := c.Revalidate(t.Context(), "postman"); !ok || err != nil {
		t.Fatalf("Setup: Revalidate = %v, %v", ok, err)
	}
	if _, ok, err := c.Admit(t.Context(), "postman"); !ok || err != nil || fetches.Load() != 1 {
		t.Errorf("Admit right after Revalidate = %v, %v with %d fetches; want the same fetch", ok, err, fetches.Load())
	}
	c.mu.Lock()
	c.fetchedAt = time.Now().Add(-serversTTL - time.Second)
	c.mu.Unlock()
	if _, _, err := c.Admit(t.Context(), "postman"); err != nil || fetches.Load() != 2 {
		t.Errorf("Admit on a catalogue older than serversTTL: err %v, %d fetches; want a refetch", err, fetches.Load())
	}
}

func TestCatalog_ServersSanitizesTheDeclaredNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"mcpServers":{"a\nb":{},"\u202eevil":{},"evil":{},"  ":{}}}`))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	names, known := c.Servers(t.Context(), &Entry{Name: "x", rawBase: srv.URL + "/o/r/main/x"})
	if !known || !slices.Equal(names, []string{"a b", "evil"}) {
		t.Errorf("Servers = %q, %v; want [\"a b\" \"evil\"], true", names, known)
	}
}

func TestCatalog_ServersReportsAnOversizedListAsUnknown(t *testing.T) {
	servers := make([]string, maxServerNames+1)
	for i := range servers {
		servers[i] = fmt.Sprintf("%q:{}", fmt.Sprintf("s%02d", i))
	}
	body := `{"mcpServers":{` + strings.Join(servers, ",") + `}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), srv.URL)
	if names, known := c.Servers(t.Context(), &Entry{Name: "x", rawBase: srv.URL + "/o/r/main/x"}); known || names != nil {
		t.Errorf("Servers over the cap = %d names, %v; want nil, false", len(names), known)
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Under ForkLock, so a concurrent test's fork cannot hold this descriptor
	// open when the file is executed (golang/go#22315).
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyServers_RendersLegacyPowersAndSkipsPlugins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "postman", "mcp.json"),
		`{"mcpServers":{"postman":{"url":"https://mcp.postman.com/minimal","autoApprove":["x"],"allowedTools":["y"]}}}`, 0o644)
	writeFile(t, filepath.Join(dir, "plug", "mcp.json"), `{"mcpServers":{"s":{"command":"c"}}}`, 0o644)
	writeFile(t, filepath.Join(dir, "plug", "dev.kiro", "plugin.json"), `{}`, 0o644)
	writeFile(t, filepath.Join(dir, "broken", "mcp.json"), `{not json`, 0o644)
	writeFile(t, filepath.Join(dir, "steering-only", "POWER.md"), `# x`, 0o644)

	servers, errs := LegacyServers(dir)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "broken") {
		t.Errorf("errs = %v, want one naming the broken power", errs)
	}
	if !slices.Equal(sortedNames(servers), []string{"power-postman-postman"}) {
		t.Fatalf("servers = %v, want only power-postman-postman", sortedNames(servers))
	}
	var entry map[string]any
	if err := json.Unmarshal(servers["power-postman-postman"], &entry); err != nil {
		t.Fatal(err)
	}
	if _, ok := entry["autoApprove"]; ok {
		t.Errorf("autoApprove survived: %v", entry)
	}
	if _, ok := entry["allowedTools"]; ok {
		t.Errorf("allowedTools survived: %v", entry)
	}
	if entry["url"] != "https://mcp.postman.com/minimal" {
		t.Errorf("url = %v, want the server's own", entry["url"])
	}
}

func TestLegacyServers_AnAbsentDirectoryIsEmpty(t *testing.T) {
	servers, errs := LegacyServers(filepath.Join(t.TempDir(), "none"))
	if len(servers) != 0 || errs != nil {
		t.Errorf("LegacyServers(absent) = %v, %v; want empty, nil", servers, errs)
	}
}

type recordingWriter struct {
	got   map[string]json.RawMessage
	calls int
}

func (w *recordingWriter) WritePowersServers(_ context.Context, s map[string]json.RawMessage) (bool, error) {
	w.calls++
	w.got = s
	return true, nil
}

func fakeCLI(t *testing.T, installed string, exit int) (cli, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	cli = filepath.Join(dir, "kiro-cli")
	script := `#!/bin/sh
echo "$@" > "` + argsFile + `"
if [ "` + string(rune('0'+exit)) + `" != 0 ]; then echo 'progress'; echo 'No power named "'"$3"'" in the catalog' >&2; exit 1; fi
if [ "$2" = install ]; then mkdir -p "` + installed + `/$3"; echo '{"mcpServers":{"s":{"command":"c"}}}' > "` + installed + `/$3/mcp.json"; fi
if [ "$2" = uninstall ]; then rm -rf "` + installed + `/$3"; fi
`
	writeFile(t, cli, script, 0o755)
	return cli, argsFile
}

func fixedMode(mode ServersMode) ModeFunc { return func() ServersMode { return mode } }

func newTestManager(t *testing.T, exit int) (*Manager, *recordingWriter, string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(registryBody))
	}))
	t.Cleanup(srv.Close)
	installed := filepath.Join(t.TempDir(), "installed")
	cli, args := fakeCLI(t, installed, exit)
	w := &recordingWriter{}
	m := NewManager(NewCatalog(srv.Client(), srv.URL), w,
		func() string { return cli }, os.Environ, installed)
	return m, w, args, installed
}

func TestManager_InstallRunsTheCLIAndRendersTheBlock(t *testing.T) {
	m, w, args, _ := newTestManager(t, 0)
	if err := m.Install(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.ReadFile(args)
	if strings.TrimSpace(string(got)) != "powers install postman" {
		t.Errorf("kiro-cli args = %q, want powers install postman", got)
	}
	if _, ok := w.got["power-postman-s"]; !ok || w.calls != 1 {
		t.Errorf("writer got %v after %d calls, want power-postman-s once", sortedNames(w.got), w.calls)
	}

	if err := m.Uninstall(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer still holds %v after the uninstall", sortedNames(w.got))
	}
}

func TestManager_ASuppressedSyncRendersNoServers(t *testing.T) {
	m, w, _, _ := newTestManager(t, 0)
	if err := m.Install(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m.Sync(t.Context(), fixedMode(ServersSuppressed)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(w.got) != 0 || w.calls != 2 {
		t.Errorf("a suppressed sync wrote %v after %d calls, want an empty block on the second", sortedNames(w.got), w.calls)
	}
	if err := m.Sync(t.Context(), fixedMode(ServersActive)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, ok := w.got["power-postman-s"]; !ok {
		t.Errorf("an active sync after the lock lifted wrote %v, want power-postman-s back", sortedNames(w.got))
	}
}

func TestManager_ALockThatFlipsMidInstallSuppressesThatInstallsRender(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(registryBody))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	installed := filepath.Join(dir, "installed")
	entered, gate := filepath.Join(dir, "entered"), filepath.Join(dir, "gate")
	cli := filepath.Join(dir, "kiro-cli")
	writeFile(t, cli, `#!/bin/sh
mkdir -p "`+installed+`/$3"
echo '{"mcpServers":{"s":{"command":"c"}}}' > "`+installed+`/$3/mcp.json"
: > "`+entered+`"
while [ ! -e "`+gate+`" ]; do sleep 0.01; done
`, 0o755)
	w := &recordingWriter{}
	m := NewManager(NewCatalog(srv.Client(), srv.URL), w,
		func() string { return cli }, os.Environ, installed)

	var mode atomic.Int32
	mode.Store(int32(ServersActive))
	done := make(chan error, 1)
	go func() {
		done <- m.Install(t.Context(), "postman", func() ServersMode { return ServersMode(mode.Load()) })
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kiro-cli never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mode.Store(int32(ServersSuppressed))
	writeFile(t, gate, "", 0o600)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Install did not return")
	}
	if len(w.got) != 0 || w.calls != 1 {
		t.Errorf("the install rendered %v after %d writes, want one empty block", sortedNames(w.got), w.calls)
	}
}

type failingWriter struct{ calls int }

func (w *failingWriter) WritePowersServers(context.Context, map[string]json.RawMessage) (bool, error) {
	w.calls++
	return false, errors.New("disk full")
}

func TestManager_ARenderFailureAfterTheCLIIsARenderError(t *testing.T) {
	m, _, _, installed := newTestManager(t, 0)
	w := &failingWriter{}
	m.writer = w
	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"install", func() error { return m.Install(t.Context(), "postman", fixedMode(ServersActive)) }},
		{"uninstall", func() error { return m.Uninstall(t.Context(), "postman", fixedMode(ServersActive)) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			err := op.run()
			if _, ok := errors.AsType[*RenderError](err); !ok {
				t.Fatalf("%s = %v, want a RenderError", op.name, err)
			}
			if !strings.Contains(err.Error(), "disk full") {
				t.Errorf("%s error %q lost the writer's cause", op.name, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(installed, "postman")); !os.IsNotExist(err) {
		t.Errorf("the uninstall's CLI run did not land: %v", err)
	}
	if err := m.Sync(t.Context(), fixedMode(ServersActive)); err == nil {
		t.Error("Sync with a failing writer returned nil")
	} else if _, ok := errors.AsType[*RenderError](err); ok {
		t.Error("Sync wrapped its failure as a RenderError; no CLI change preceded it")
	}
}

func TestManager_InstallRefusesANameNotInTheCatalogue(t *testing.T) {
	m, w, args, _ := newTestManager(t, 0)
	if err := m.Install(t.Context(), "not-listed", fixedMode(ServersActive)); !errors.Is(err, ErrUnknownPower) {
		t.Errorf("Install(not-listed) = %v, want ErrUnknownPower", err)
	}
	if _, err := os.Stat(args); err == nil || w.calls != 0 {
		t.Errorf("an unlisted name reached kiro-cli or the writer")
	}
	if err := m.Uninstall(t.Context(), "../x", fixedMode(ServersActive)); !errors.Is(err, ErrUnknownPower) {
		t.Errorf("Uninstall(../x) = %v, want ErrUnknownPower", err)
	}
}

func TestManager_AFailedRunCarriesItsLastLine(t *testing.T) {
	m, w, _, _ := newTestManager(t, 1)
	err := m.Install(t.Context(), "postman", fixedMode(ServersActive))
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || !strings.Contains(cliErr.Message, `No power named "postman"`) {
		t.Fatalf("Install = %v, want a CLIError carrying the CLI's last line", err)
	}
	if w.calls != 0 {
		t.Errorf("a failed install still rendered the block")
	}
}

func TestManager_ACancelledRunKillsTheCLIsChildren(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(registryBody))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cli := filepath.Join(dir, "kiro-cli")
	writeFile(t, cli, "#!/bin/sh\nsleep 300 &\necho $! > \""+pidFile+"\"\nwait\n", 0o755)
	m := NewManager(NewCatalog(srv.Client(), srv.URL), &recordingWriter{},
		func() string { return cli }, os.Environ, filepath.Join(dir, "installed"))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.Install(ctx, "postman", fixedMode(ServersActive)) }()
	pid := waitForPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Install after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Install did not return after its context was cancelled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the CLI's child (pid %d) outlived the cancelled run", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fake CLI never reported its child's pid")
	return 0
}

// processAlive treats a zombie as dead: an orphan waits on PID 1 to reap it.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

func TestCappedBuffer_KeepsTheTail(t *testing.T) {
	var b cappedBuffer
	_, _ = b.Write([]byte(strings.Repeat("a", maxCLIOutput)))
	_, _ = b.Write([]byte("tail"))
	if s := b.String(); len(s) != maxCLIOutput || !strings.HasSuffix(s, "tail") {
		t.Errorf("buffer is %d bytes ending %q, want %d ending tail", len(s), s[len(s)-4:], maxCLIOutput)
	}
}

func TestManager_InstallRefusesUntilTheAdministratorRulesAreKnown(t *testing.T) {
	m, w, args, _ := newTestManager(t, 0)
	if err := m.Install(t.Context(), "postman", fixedMode(ServersUnresolved)); !errors.Is(err, ErrPolicyUnknown) {
		t.Fatalf("Install(unresolved) = %v, want ErrPolicyUnknown", err)
	}
	if _, err := os.Stat(args); err == nil || w.calls != 0 {
		t.Errorf("an install before the rules were known reached kiro-cli or the writer")
	}
}

func TestManager_InstallRefusesUnderAPowersLock(t *testing.T) {
	m, w, args, _ := newTestManager(t, 0)
	if err := m.Install(t.Context(), "postman", fixedMode(ServersSuppressed)); !errors.Is(err, ErrPowersLocked) {
		t.Fatalf("Install(suppressed) = %v, want ErrPowersLocked", err)
	}
	if _, err := os.Stat(args); err == nil || w.calls != 0 {
		t.Errorf("an install under the powers lock reached kiro-cli or the writer")
	}
}

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWriter) WritePowersServers(context.Context, map[string]json.RawMessage) (bool, error) {
	w.entered <- struct{}{}
	<-w.release
	return true, nil
}

func TestManager_AnInstallWaitingOnTheLockSeesThePolicyResolve(t *testing.T) {
	m, _, args, _ := newTestManager(t, 0)
	w := &blockingWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m.writer = w
	go func() { _ = m.Sync(t.Context(), fixedMode(ServersUnresolved)) }()
	<-w.entered

	var mode atomic.Int32
	mode.Store(int32(ServersUnresolved))
	read := make(chan struct{}, 8)
	done := make(chan error, 1)
	go func() {
		done <- m.Install(t.Context(), "postman", func() ServersMode {
			read <- struct{}{}
			return ServersMode(mode.Load())
		})
	}()
	select {
	case <-read:
		t.Fatal("the install read the policy before it held the manager lock")
	case <-time.After(200 * time.Millisecond):
	}
	mode.Store(int32(ServersActive))
	close(w.release)
	<-w.entered
	if err := <-done; err != nil {
		t.Fatalf("Install after the rules resolved = %v, want nil", err)
	}
	if got, _ := os.ReadFile(args); strings.TrimSpace(string(got)) != "powers install postman" {
		t.Errorf("kiro-cli args = %q, want powers install postman", got)
	}
}

func TestManager_AnUnreadablePowerFailsOnlyItsOwnVerb(t *testing.T) {
	m, w, args, installed := newTestManager(t, 0)
	writeFile(t, filepath.Join(installed, "broken", "mcp.json"), `{not json`, 0o644)

	if err := m.Install(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
		t.Fatalf("Install(postman) beside a broken power = %v, want nil", err)
	}
	if _, ok := w.got["power-postman-s"]; !ok {
		t.Errorf("writer got %v, want power-postman-s", sortedNames(w.got))
	}
	err := m.Sync(t.Context(), fixedMode(ServersActive))
	if p, ok := errors.AsType[*PartialError](err); !ok || !slices.Equal(p.Failed, []string{"broken"}) {
		t.Errorf("Sync = %v, want a PartialError naming broken", err)
	}

	writeFile(t, filepath.Join(filepath.Dir(args), "kiro-cli"), `#!/bin/sh
mkdir -p "`+installed+`/$3"
echo '{not json' > "`+installed+`/$3/mcp.json"
`, 0o755)
	err = m.Install(t.Context(), "evil", fixedMode(ServersActive))
	if _, ok := errors.AsType[*RenderError](err); !ok || !strings.Contains(err.Error(), "evil") {
		t.Fatalf("Install(evil) with an unreadable mcp.json = %v, want a RenderError naming evil", err)
	}
	if _, ok := w.got["power-postman-s"]; !ok {
		t.Errorf("after the failed render the writer holds %v, want power-postman-s kept", sortedNames(w.got))
	}
}

func TestLegacyServers_TwoPowersDerivingOneKeyAreBothReported(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a-b", "mcp.json"), `{"mcpServers":{"c":{"command":"first"}}}`, 0o644)
	writeFile(t, filepath.Join(dir, "a", "mcp.json"), `{"mcpServers":{"b-c":{"command":"second"},"d":{"command":"kept"}}}`, 0o644)
	writeFile(t, filepath.Join(dir, "z", "mcp.json"), `{"mcpServers":{"s":{"command":"z"}}}`, 0o644)

	servers, errs := LegacyServers(dir)
	if got := sortedNames(servers); !slices.Equal(got, []string{"power-a-d", "power-z-s"}) {
		t.Errorf("servers = %v, want the colliding power-a-b-c dropped and the rest kept", got)
	}
	var reported []string
	for _, err := range errs {
		if se, ok := errors.AsType[*ScanError](err); ok {
			reported = append(reported, se.Power)
		}
	}
	slices.Sort(reported)
	if !slices.Equal(reported, []string{"a", "a-b"}) {
		t.Errorf("reported %v (errs %v), want ScanErrors naming a and a-b", reported, errs)
	}
}

func TestManager_APowerCollidingOnTwoKeysIsReportedOnce(t *testing.T) {
	m, _, _, installed := newTestManager(t, 0)
	writeFile(t, filepath.Join(installed, "a-b", "mcp.json"), `{"mcpServers":{"c":{},"e":{}}}`, 0o644)
	writeFile(t, filepath.Join(installed, "a", "mcp.json"), `{"mcpServers":{"b-c":{},"b-e":{}}}`, 0o644)

	err := m.Sync(t.Context(), fixedMode(ServersActive))
	p, ok := errors.AsType[*PartialError](err)
	if !ok {
		t.Fatalf("Sync with two colliding keys = %v, want a PartialError", err)
	}
	got := slices.Sorted(slices.Values(p.Failed))
	if !slices.Equal(got, []string{"a", "a-b"}) {
		t.Errorf("PartialError.Failed = %v, want a and a-b once each", p.Failed)
	}
}

func TestManager_InstallingAPowerWhoseServerKeyCollidesIsARenderError(t *testing.T) {
	m, w, args, installed := newTestManager(t, 0)
	writeFile(t, filepath.Join(installed, "evil-x", "mcp.json"), `{"mcpServers":{"y":{"command":"other"}}}`, 0o644)
	writeFile(t, filepath.Join(installed, "postman", "mcp.json"), `{"mcpServers":{"s":{"command":"c"}}}`, 0o644)
	writeFile(t, filepath.Join(filepath.Dir(args), "kiro-cli"), `#!/bin/sh
mkdir -p "`+installed+`/$3"
echo '{"mcpServers":{"x-y":{"command":"mine"}}}' > "`+installed+`/$3/mcp.json"
`, 0o755)

	err := m.Install(t.Context(), "evil", fixedMode(ServersActive))
	if _, ok := errors.AsType[*RenderError](err); !ok {
		t.Fatalf("Install(evil) whose server key collides = %v, want a RenderError", err)
	}
	if _, ok := w.got["power-evil-x-y"]; ok {
		t.Errorf("the colliding key was written: %v", sortedNames(w.got))
	}
	if _, ok := w.got["power-postman-s"]; !ok {
		t.Errorf("writer holds %v, want the healthy power-postman-s kept", sortedNames(w.got))
	}
}
