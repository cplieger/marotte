package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

// seamEngine is a kiro-cli-chat fake answering every request from files under dir, so a cell can change KAS's
// answers mid-test. Each request runs in its own subshell, so a blocked method does not stall the others.
type seamEngine struct {
	dir    string
	script string
}

const seamEngineScript = `#!/bin/sh
D='%s'
answer() {
  line=$1
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(printf '%%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  wf=$(printf '%%s' "$line" | sed -n 's/.*"workflowId":"\([^"]*\)".*/\1/p')
  key=$(printf '%%s' "$method" | tr '/' '_')
  [ -n "$method" ] && printf '%%s %%s\n' "$method" "$wf" >> "$D/calls.log"
  if [ -f "$D/block_$key" ]; then
    touch "$D/entered_$key"
    while [ -f "$D/block_$key" ]; do sleep 0.02; done
  fi
  for n in "$D"/notify/*.json; do
    [ -e "$n" ] || continue
    mv "$n" "$n.$$" 2>/dev/null && cat "$n.$$" && rm -f "$n.$$"
  done
  [ -n "$id" ] && [ -n "$method" ] || return 0
  if [ "$method" = initialize ]; then
    printf '{"jsonrpc":"2.0","id":%%s,"result":{"protocolVersion":1}}\n' "$id"
    return 0
  fi
  for f in "$D/err_all" "$D/err_${key}_$wf"; do
    if [ -f "$f" ]; then printf '{"jsonrpc":"2.0","id":%%s,"error":%%s}\n' "$id" "$(cat "$f")"; return 0; fi
  done
  if [ -f "$D/res_${key}_$wf" ]; then printf '{"jsonrpc":"2.0","id":%%s,"result":%%s}\n' "$id" "$(cat "$D/res_${key}_$wf")"; return 0; fi
  if [ -f "$D/err_$key" ]; then printf '{"jsonrpc":"2.0","id":%%s,"error":%%s}\n' "$id" "$(cat "$D/err_$key")"; return 0; fi
  if [ -f "$D/res_$key" ]; then printf '{"jsonrpc":"2.0","id":%%s,"result":%%s}\n' "$id" "$(cat "$D/res_$key")"; return 0; fi
  printf '{"jsonrpc":"2.0","id":%%s,"result":{}}\n' "$id"
}
while IFS= read -r line; do answer "$line" & done
wait
`

const (
	kasList    = "_kiro/workflow/list"
	kasInspect = "_kiro/workflow/inspect"
	kasDelete  = "_kiro/workflow/delete"
	kasCancel  = "_kiro/workflow/cancel"
	kasSessDel = "session/delete"
)

func newSeamEngine(t *testing.T) *seamEngine {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notify"), 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	e := &seamEngine{dir: dir, script: filepath.Join(dir, "kiro-cli-chat")}
	// ForkLock: a concurrent fork inheriting the write fd fails the exec with ETXTBSY (golang/go#22315).
	syscall.ForkLock.RLock()
	err := os.WriteFile(e.script, fmt.Appendf(nil, seamEngineScript, dir), 0o700)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write the engine fake: %v", err)
	}
	e.list(t)
	e.notFound(t, kasInspect, "")
	return e
}

func (e *seamEngine) CLIPath() string             { return e.script }
func (*seamEngine) Installed() <-chan struct{}    { return nil }
func (*seamEngine) Env() []string                 { return nil }
func (*seamEngine) EnvAllow() map[string]struct{} { return nil }

func kasKey(method, wf string) string {
	k := strings.ReplaceAll(method, "/", "_")
	if wf != "" {
		k += "_" + wf
	}
	return k
}

func (e *seamEngine) write(t *testing.T, name string, body []byte) {
	t.Helper()
	tmp := filepath.Join(e.dir, "."+name)
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(e.dir, name)); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// answer makes method (for wf, when set) reply result.
func (e *seamEngine) answer(t *testing.T, method, wf string, result any) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_ = os.Remove(filepath.Join(e.dir, "err_"+kasKey(method, wf)))
	e.write(t, "res_"+kasKey(method, wf), raw)
}

// fail makes method (for wf, when set) refuse with KAS's untyped error carrying details.
func (e *seamEngine) fail(t *testing.T, method, wf, details string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"code": -32603, "message": "Internal error", "data": map[string]string{"details": details},
	})
	e.write(t, "err_"+kasKey(method, wf), raw)
}

// notFound makes method (for wf, when set) refuse as KAS does for a run it has no state for (kiro-cli 2.37.0).
func (e *seamEngine) notFound(t *testing.T, method, wf string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"code": -32603, "message": "Workflow not found",
		"data": map[string]string{"details": "Workflow not found", "errorType": "WorkflowNotFoundError"},
	})
	e.write(t, "err_"+kasKey(method, wf), raw)
}

// down makes every request after this one fail.
func (e *seamEngine) down(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"code": -32603, "message": "Internal error", "data": map[string]string{"details": "engine draining"}})
	e.write(t, "err_all", raw)
}

func seamRow(wf, status, parent string) map[string]string {
	return map[string]string{
		"workflowId": wf, "status": status, "parentSessionId": parent,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	}
}

// list sets the workflow list's rows.
func (e *seamEngine) list(t *testing.T, rows ...map[string]string) {
	t.Helper()
	if rows == nil {
		rows = []map[string]string{}
	}
	e.answer(t, kasList, "", map[string]any{"runs": rows})
}

// block parks method until the returned release runs; entered waits for the first request to park.
func (e *seamEngine) block(t *testing.T, method string) (entered, release func()) {
	t.Helper()
	key := kasKey(method, "")
	e.write(t, "block_"+key, nil)
	var once sync.Once
	release = func() { once.Do(func() { _ = os.Remove(filepath.Join(e.dir, "block_"+key)) }) }
	t.Cleanup(release)
	entered = func() {
		t.Helper()
		waitFor(t, method+" parked", func() bool {
			_, err := os.Stat(filepath.Join(e.dir, "entered_"+key))
			return err == nil
		})
	}
	return entered, release
}

// notify queues an engine-lane notification, written before the next request's reply.
func (e *seamEngine) notify(t *testing.T, method string, params map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	name := fmt.Sprintf("%d.json", time.Now().UnixNano())
	tmp := filepath.Join(e.dir, "notify", "."+name)
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(e.dir, "notify", name)); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// count is how many requests for method the engine received.
func (e *seamEngine) count(t *testing.T, method string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, "calls.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the engine log: %v", err)
	}
	n := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if m, _, _ := strings.Cut(line, " "); m == method {
			n++
		}
	}
	return n
}

// inspected makes inspect answer wf with status.
func (e *seamEngine) inspected(t *testing.T, wf, status, parent string) {
	t.Helper()
	e.answer(t, kasInspect, wf, map[string]any{
		"workflowId": wf, "state": map[string]any{"workflowId": wf, "status": status, "parentSessionId": parent},
	})
}

// countFor is how many requests for method naming wf the engine received.
func (e *seamEngine) countFor(t *testing.T, method, wf string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, "calls.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the engine log: %v", err)
	}
	n := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line == method+" "+wf {
			n++
		}
	}
	return n
}

// called reports whether the engine received method for wf (any workflow when wf is empty).
func (e *seamEngine) called(t *testing.T, method, wf string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, "calls.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the engine log: %v", err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		m, w, _ := strings.Cut(line, " ")
		if m == method && (wf == "" || w == wf) {
			return true
		}
	}
	return false
}

// installingEngine is a managed install that never becomes active: the engine never starts.
type installingEngine struct{}

func (installingEngine) CLIPath() string               { return "" }
func (installingEngine) Installed() <-chan struct{}    { return make(chan struct{}) }
func (installingEngine) Env() []string                 { return nil }
func (installingEngine) EnvAllow() map[string]struct{} { return nil }

// seamLogs captures every log record; tests that read it cannot run in parallel.
type seamLogs struct {
	counts map[string]int
	lines  []string
	mu     sync.Mutex
}

func captureSeamLogs(t *testing.T) *seamLogs {
	t.Helper()
	l := &seamLogs{counts: map[string]int{}}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(seamLogHandler{l: l}))
	return l
}

func (l *seamLogs) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[msg]
}

// has reports whether a record whose text contains every part was logged.
func (l *seamLogs) has(parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
next:
	for _, line := range l.lines {
		for _, p := range parts {
			if !strings.Contains(line, p) {
				continue next
			}
		}
		return true
	}
	return false
}

type seamLogHandler struct {
	l     *seamLogs
	attrs []slog.Attr
}

func (seamLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h seamLogHandler) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	b.WriteString(r.Level.String() + " " + r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	h.l.mu.Lock()
	h.l.counts[r.Message]++
	h.l.lines = append(h.l.lines, b.String())
	h.l.mu.Unlock()
	return nil
}

func (h seamLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return seamLogHandler{l: h.l, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}

func (h seamLogHandler) WithGroup(string) slog.Handler { return h }

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

// editRecord rewrites chat id's header on disk, as an earlier build or boot left it.
func editRecord(t *testing.T, root, id string, edit func(doc map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "chats", id, "chat.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Setup: parse %s: %v", path, err)
	}
	edit(doc)
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

const (
	seamPassLine      = "chat purge scheduled"
	seamChatPassLine  = "chat purge: pass complete"
	seamReconcileLine = "keep-state reconcile finished"
	seamRetentionDays = 7
)

// seamIdle is past the retention window the rig configures.
const seamIdle = (seamRetentionDays + 1) * 24 * time.Hour

// seamBoot is one marotte boot over root, wired the way Build wires retention: the real chat, lease and tab
// stores, the session reaper, the runtime over the fake engine, the load-time tab prune, the orphan sweep and
// wireRetention's scheduler and reconcile.
type seamBoot struct {
	eng         *seamEngine
	logs        *seamLogs
	chats       *chat.Store
	tabs        *tabs.Store
	leases      *runlease.Store
	h           *agent.Runtime
	sched       *archive.PurgeScheduler
	mux         *http.ServeMux
	cancel      context.CancelFunc
	schedCtx    context.Context
	schedStop   context.CancelFunc
	stopKeep    func()
	stopSweep   func()
	root        string
	workDir     string
	sessionsDir string
	started     bool
	stopped     bool
}

type seamOpts struct {
	engine      agent.EngineSource
	noReconcile bool
	// waitReconcile is false when the reconcile is expected not to finish (a stalled source).
	noWait bool
}

func seamRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KIRO_HOME", filepath.Join(root, "kiro"))
	writeConfig(t, filepath.Join(root, "config.json"), fmt.Sprintf(`{"chat_retention_days":%d}`, seamRetentionDays))
	return root
}

func bootSeam(t *testing.T, root string, eng *seamEngine, logs *seamLogs, o seamOpts) *seamBoot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b := &seamBoot{
		eng: eng, logs: logs, root: root, cancel: cancel,
		workDir: filepath.Join(root, "work"), sessionsDir: filepath.Join(root, "kiro", "sessions"),
	}
	if err := os.MkdirAll(b.workDir, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var err error
	b.chats, err = chat.NewStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("Setup: chat store: %v", err)
	}
	b.leases = openRunLeaseStore(root)
	reaper := kirosession.New(b.sessionsDir, b.workDir)
	b.tabs = openTabStore(root)
	var src agent.EngineSource = eng
	if o.engine != nil {
		src = o.engine
	}
	b.h = agent.New(ctx, b.workDir, src, b.chats,
		agent.WithConfigDir(root),
		agent.WithSessionReaper(reaper, b.chats.ReferencedSessionIDs),
		agent.WithRunLeases(b.leases),
		agent.WithTabs(b.tabs))
	chat.WithBroadcaster(b.h)(b.chats)
	chat.WithEpoch(b.h.Epoch)(b.chats)
	pruneTabs(ctx, b.tabs, b.chats)
	b.stopSweep = startOrphanSweep(ctx, b.h.Runs().BootSweep, b.h.EngineReady())
	sched, start, err := wireRetention(ctx, &Config{ConfigDir: root, WorkDir: b.workDir}, b.chats, b.h,
		retentionStores{tabs: b.tabs, leases: b.leases, reaper: reaper})
	if err != nil {
		t.Fatalf("wireRetention: %v", err)
	}
	b.sched = sched
	b.schedCtx, b.schedStop = context.WithCancel(ctx)
	b.mux = http.NewServeMux()
	b.h.RegisterRoutes(b.mux)
	t.Cleanup(func() { b.shutdown(t) })
	if !o.noReconcile {
		n := logs.count(seamReconcileLine)
		b.stopKeep = start()
		if !o.noWait {
			waitFor(t, "the keep-state reconcile", func() bool { return logs.count(seamReconcileLine) > n })
		}
	}
	return b
}

func (b *seamBoot) shutdown(t *testing.T) {
	t.Helper()
	if b.stopped {
		return
	}
	b.stopped = true
	if b.stopKeep != nil {
		b.stopKeep()
	}
	b.schedStop()
	b.sched.Stop()
	b.stopSweep()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.h.Shutdown(ctx); err != nil {
		t.Errorf("runtime shutdown: %v", err)
	}
	b.cancel()
}

// pass runs one purge pass to completion: the first starts the scheduler, later ones trigger it. Passes a tab
// close woke are let finish first, so the pass waited on began after the cell's last change.
func (b *seamBoot) pass(t *testing.T) {
	t.Helper()
	b.quiet(t)
	n := b.logs.count(seamPassLine)
	if b.started {
		b.sched.Trigger()
	} else {
		b.start()
	}
	waitFor(t, "a purge pass", func() bool { return b.logs.count(seamPassLine) > n })
}

func (b *seamBoot) start() {
	b.started = true
	b.sched.Start(b.schedCtx)
}

// chatPass starts the scheduler and waits for its chat purge alone, for a cell whose side passes wait on an engine
// that never starts.
func (b *seamBoot) chatPass(t *testing.T) {
	t.Helper()
	n := b.logs.count(seamChatPassLine)
	b.start()
	waitFor(t, "the chat purge", func() bool { return b.logs.count(seamChatPassLine) > n })
}

func (b *seamBoot) quiet(t *testing.T) {
	t.Helper()
	if !b.started {
		return
	}
	for {
		n := b.logs.count(seamPassLine)
		time.Sleep(150 * time.Millisecond)
		if b.logs.count(seamPassLine) == n {
			return
		}
	}
}

// cmd posts one client command and returns the status and the decoded body.
func (b *seamBoot) cmd(t *testing.T, typ marotte.CommandType, chatID string, payload any) (int, map[string]any) {
	t.Helper()
	env := map[string]any{"type": typ}
	if chatID != "" {
		env["chat_id"] = chatID
	}
	if payload != nil {
		env["payload"] = payload
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("encode %s: %v", typ, err)
	}
	return b.serve(t, http.MethodPost, "/api/command", raw)
}

func (b *seamBoot) serve(t *testing.T, method, path string, body []byte) (int, map[string]any) {
	t.Helper()
	return b.serveAs(t, method, path, body, "")
}

// serveAs is serve from origin, as marotte's own page sends it; "" is an API caller's request.
func (b *seamBoot) serveAs(t *testing.T, method, path string, body []byte, origin string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	b.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// create makes chat id through create_chat (its tab opens) and binds session to its chain.
func (b *seamBoot) create(t *testing.T, id, session string) string {
	t.Helper()
	code, body := b.cmd(t, marotte.CmdCreateChat, id, map[string]any{"name": id})
	if code != http.StatusOK {
		t.Fatalf("Setup: create_chat %s = %d %v", id, code, body)
	}
	if session != "" {
		b.bind(t, id, session)
	}
	return b.tabOf(t, id)
}

// seed makes a tabless chat id with session on its chain, as a resume that never opened a tab leaves it.
func (b *seamBoot) seed(t *testing.T, id, session string) {
	t.Helper()
	b.create(t, id, session)
	b.closeTab(t, id)
}

func (b *seamBoot) bind(t *testing.T, id, session string) {
	t.Helper()
	if _, err := b.chats.Mutate(t.Context(), marotte.ChatID(id), func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.RecordSession(session)
		return true
	}); err != nil {
		t.Fatalf("Setup: bind %s to %s: %v", session, id, err)
	}
	dir := filepath.Join(b.sessionsDir, "bucket", session)
	writeFixture(t, filepath.Join(dir, "session.json"), fmt.Sprintf(`{"workspacePaths":[%q]}`, b.workDir))
}

func (b *seamBoot) tabOf(t *testing.T, id string) string {
	t.Helper()
	open, _ := b.tabs.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindChat && s.Ref == id {
			return s.ID
		}
	}
	return ""
}

func (b *seamBoot) runTabOf(wf string) string {
	open, _ := b.tabs.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindRun && s.Ref == wf {
			return s.ID
		}
	}
	return ""
}

func (b *seamBoot) closeTab(t *testing.T, id string) {
	t.Helper()
	tab := b.tabOf(t, id)
	if tab == "" {
		t.Fatalf("Setup: %s has no open tab", id)
	}
	b.closeTabID(t, tab)
}

// personClose closes chat id's tab from the page, a person's close.
func (b *seamBoot) personClose(t *testing.T, id string) {
	t.Helper()
	tab := b.tabOf(t, id)
	if tab == "" {
		t.Fatalf("Setup: %s has no open tab", id)
	}
	raw, err := json.Marshal(map[string]any{"type": marotte.CmdCloseTab, "payload": map[string]any{"id": tab}})
	if err != nil {
		t.Fatal(err)
	}
	ref := b.tabRef(tab)
	if code, body := b.serveAs(t, http.MethodPost, "/api/command", raw, "http://marotte.test"); code != http.StatusOK {
		t.Errorf("a person's close_tab %s = %d %v", tab, code, body)
	}
	b.awaitTeardown(t, ref)
}

func (b *seamBoot) closeTabID(t *testing.T, tab string) {
	t.Helper()
	ref := b.tabRef(tab)
	if code, body := b.cmd(t, marotte.CmdCloseTab, "", map[string]any{"id": tab}); code != http.StatusOK {
		t.Fatalf("Setup: close_tab %s = %d %v", tab, code, body)
	}
	b.awaitTeardown(t, ref)
}

// tabRef is the chat an open chat tab shows, "" for any other tab.
func (b *seamBoot) tabRef(tab string) marotte.ChatID {
	open, _ := b.tabs.List()
	for _, s := range open {
		if s.ID == tab && s.Kind == marotte.TabKindChat {
			return marotte.ChatID(s.Ref)
		}
	}
	return ""
}

// awaitTeardown waits for the background teardown a close of chat ref started, so a step after the close sees it
// done: close_tab answers before it.
func (b *seamBoot) awaitTeardown(t *testing.T, ref marotte.ChatID) {
	t.Helper()
	if ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := b.h.AwaitNoClose(ctx, ref); err != nil {
		t.Fatalf("the teardown of %s did not finish: %v", ref, err)
	}
}

// loneRunTab opens wf's run tab, which opens under its launcher's tab, then moves it under another chat's tab and
// closes the launcher's, so only the run-tab fact holds the launcher.
func (b *seamBoot) loneRunTab(t *testing.T, launcher, wf string) {
	t.Helper()
	host := b.create(t, "c-host", "")
	if code, body := b.openTab(t, marotte.TabKindRun, wf, ""); code != http.StatusOK {
		t.Fatalf("Setup: open the run tab = %d %v", code, body)
	}
	tab := b.runTabOf(wf)
	if code, body := b.cmd(t, marotte.CmdReparentTab, "", map[string]any{"id": tab, "parent": host}); code != http.StatusOK {
		t.Fatalf("Setup: reparent the run tab = %d %v", code, body)
	}
	b.closeTab(t, launcher)
	keys := b.keys(t, launcher)
	if _, ok := keys[marotte.KeyTab]; ok {
		t.Fatalf("Setup: the launcher still holds its tab fact: %v", keys)
	}
	if k, _ := marotte.KeyRunTab(wf); keys[k] == (marotte.KeepFact{}) {
		t.Fatalf("the run tab wrote no fact on its launcher: %v", keys)
	}
}

func (b *seamBoot) openTab(t *testing.T, kind marotte.TabKind, ref, parent string) (int, map[string]any) {
	t.Helper()
	p := map[string]any{"kind": kind, "ref": ref}
	if parent != "" {
		p["parent"] = parent
	}
	return b.cmd(t, marotte.CmdOpenTab, "", p)
}

// age backdates the chat's last use (both stamps) by ago, as idle time would.
func (b *seamBoot) age(t *testing.T, id string, ago time.Duration) {
	t.Helper()
	ageRecord(t, b.root, id, ago)
}

func ageRecord(t *testing.T, root, id string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago).UnixMilli()
	editRecord(t, root, id, func(doc map[string]any) {
		doc["updated_at"] = at
		if _, ok := doc["keep_filled_at"]; ok {
			doc["keep_filled_at"] = at
		}
	})
}

// ageMark backdates the Since of the chat's keep fact key by ago, as a run with no sign of life since would leave it.
func (b *seamBoot) ageMark(t *testing.T, id, key string, ago time.Duration) {
	t.Helper()
	editRecord(t, b.root, id, func(doc map[string]any) {
		keep, _ := doc["keep"].(map[string]any)
		f, ok := keep[key].(map[string]any)
		if !ok {
			t.Fatalf("Setup: %s holds no %s to age: %v", id, key, keep)
		}
		f["since"] = time.Now().Add(-ago).UnixMilli()
	})
}

func (b *seamBoot) present(t *testing.T, id string) bool {
	t.Helper()
	_, found, err := b.chats.Lookup(t.Context(), marotte.ChatID(id))
	if err != nil {
		return true
	}
	return found
}

func (b *seamBoot) keys(t *testing.T, id string) map[string]marotte.KeepFact {
	t.Helper()
	c, found, err := b.chats.Lookup(t.Context(), marotte.ChatID(id))
	if err != nil || !found {
		return nil
	}
	return c.Keep
}

func (b *seamBoot) sessionDir(session string) string {
	return filepath.Join(b.sessionsDir, "bucket", session)
}

// waitKey waits until chat id's record holds key (or no longer does, when want is false).
func (b *seamBoot) waitKey(t *testing.T, id, key string, want bool) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%s holding %s = %v", id, key, want), func() bool {
		_, ok := b.keys(t, id)[key]
		return ok == want
	})
}

// frame delivers an engine-lane notification by asking the engine for its run list, the reply it rides ahead of.
func (b *seamBoot) frame(t *testing.T, method string, params map[string]any) {
	t.Helper()
	b.eng.notify(t, method, params)
	_, _ = b.h.Runs().RunRows(t.Context())
}

func (b *seamBoot) runStart(t *testing.T, wf, parent string) {
	t.Helper()
	b.frame(t, "_kiro/workflow/run_start", map[string]any{"workflowId": wf, "workflowName": "r", "parentSessionId": parent})
}

func (b *seamBoot) runComplete(t *testing.T, wf string) {
	t.Helper()
	b.frame(t, "_kiro/workflow/run_complete", map[string]any{"workflowId": wf, "status": "completed"})
}

// purged asserts the chat was purged with its teardown: no record, no tab, its session reaped.
func (b *seamBoot) purged(t *testing.T, id, session string) {
	t.Helper()
	if b.present(t, id) {
		t.Errorf("%s survived the pass; want purged (keep %v)", id, b.keys(t, id))
	}
	if tab := b.tabOf(t, id); tab != "" {
		t.Errorf("%s was purged but tab %s still shows it", id, tab)
	}
	if session != "" {
		waitFor(t, "the purged chain's session reaped", func() bool {
			_, err := os.Stat(b.sessionDir(session))
			return os.IsNotExist(err)
		})
	}
}

// kept asserts the chat survived the pass with its chain's session.
func (b *seamBoot) kept(t *testing.T, id, session string) {
	t.Helper()
	if !b.present(t, id) {
		t.Errorf("%s was purged; want kept", id)
		return
	}
	if session != "" {
		if _, err := os.Stat(b.sessionDir(session)); err != nil {
			t.Errorf("%s was kept but its session %s was reaped: %v", id, session, err)
		}
	}
}
