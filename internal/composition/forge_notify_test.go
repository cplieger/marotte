package composition

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/subject"
)

// recordingHub keeps every frame broadcast, and with versions set, the forge
// inventory version of each frame's moment.
type recordingHub struct {
	versions *subject.Versions
	frames   []marotte.ServerEvent
	seen     []string
	mu       sync.Mutex
}

func (h *recordingHub) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frames = append(h.frames, evt)
	if h.versions != nil {
		v, _ := h.versions.Current(subject.KindForgeInventory, "github:github.com")
		h.seen = append(h.seen, v)
	}
}

func (*recordingHub) Epoch() string { return "epoch" }

func (h *recordingHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.frames)
}

type quietNotifier struct{}

func (quietNotifier) Notify(context.Context, *marotte.NotificationPayload) {}

// stubGitHub is a plaintext loopback GitHub API answering alice's identity and
// an empty repository list, counting identity reads.
func stubGitHub(t *testing.T) (srv *httptest.Server, whoamis *atomic.Int32) {
	t.Helper()
	whoamis = &atomic.Int32{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/user":
			whoamis.Add(1)
			_, _ = io.WriteString(w, `{"login":"alice"}`)
		case "/api/v3/user/repos":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, whoamis
}

// wiredForges is the manager, routes and notifier composed as Build composes
// them, with the names cache's builds blocked until the test ends.
type wiredForges struct {
	m        *forges.Manager
	mux      *http.ServeMux
	notifier *forgeNotifier
	hub      *recordingHub
	names    *repoNamesCache
}

func wireForges(t *testing.T) *wiredForges {
	t.Helper()
	isolateForgeGit(t)
	hub := &recordingHub{}
	notifier := &forgeNotifier{hub: hub}
	m := forges.NewManager(t.TempDir(), notifier.notify)
	m.Load(t.Context())
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	steer := steering.New(t.TempDir(), t.TempDir())
	names := newRepoNamesCache(context.WithoutCancel(t.Context()), steer, func(context.Context) map[string][]string {
		<-blocked
		return nil
	})
	poller := forges.NewPRStatusPoller(forges.NewManagerPRSource(m, func(context.Context) []forges.RepoOrigin { return nil }),
		quietNotifier{}, func() forges.Gate { return forges.Gate{} })
	notifier.poller, notifier.names = poller, names
	mux := http.NewServeMux()
	forges.NewHTTPHandler(m).RegisterRoutes(mux)
	return &wiredForges{m: m, mux: mux, notifier: notifier, hub: hub, names: names}
}

func (w *wiredForges) send(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))
	return rec
}

func (w *wiredForges) signIn(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"kind": "github", "host": strings.TrimPrefix(srv.URL, "http://"), "token": "ghp-test",
		"web_base_url": srv.URL, "plaintext_http": true, "private_addresses": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := w.send(t, http.MethodPost, "/api/forges/login", string(body))
	var got forges.LoginResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ForgeID == "" {
		t.Fatalf("POST /api/forges/login = %d %s, want 200 with the minted id", rec.Code, rec.Body)
	}
	return got.ForgeID
}

func hostsOf(snap steering.ForgeSnapshot) []string {
	out := make([]string, 0, len(snap.Providers))
	for _, p := range snap.Providers {
		out = append(out, p.Host)
	}
	return out
}

// A chat started right after a sign-in or a sign-out lists the right accounts,
// with no wait on the repository names refresh, and a check whose only news is
// its time notifies nothing.
func TestForgeNotify_ANewChatSeesTheAccountAtOnce(t *testing.T) {
	srv, whoamis := stubGitHub(t)
	w := wireForges(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	id := w.signIn(t, srv)
	if got := hostsOf(forgeSnapshot(w.m, w.names)); len(got) != 1 || got[0] != host {
		t.Fatalf("forgeSnapshot right after the sign-in = %v, want [%s] with the names refresh still blocked", got, host)
	}
	if rec := w.send(t, http.MethodDelete, "/api/forges/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE the connection = %d %s", rec.Code, rec.Body)
	}
	if got := hostsOf(forgeSnapshot(w.m, w.names)); len(got) != 0 {
		t.Fatalf("forgeSnapshot right after the sign-out = %v, want none", got)
	}

	w.signIn(t, srv)
	before, reads := w.hub.count(), whoamis.Load()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		forges.NewKeeper(w.m).Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for whoamis.Load() == reads {
		if time.Now().After(deadline) {
			t.Fatal("the keeper pass sent no check within ten seconds")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := w.hub.count(); got != before {
		t.Errorf("a pass whose check changed only checked_at broadcast %d frames, want none", got-before)
	}
}

// A sign-in drops the replaced account's inventory before any tab is told, so
// no tab re-reads the old account's entry on the broadcast.
func TestForgeNotify_ASignInDropsTheInventoryBeforeTheBroadcast(t *testing.T) {
	versions := &subject.Versions{}
	hub := &recordingHub{versions: versions}
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	names := newRepoNamesCache(context.WithoutCancel(t.Context()), steering.New(t.TempDir(), t.TempDir()),
		func(context.Context) map[string][]string { <-blocked; return nil })
	poller := forges.NewPRStatusPoller(nil, quietNotifier{}, func() forges.Gate { return forges.Gate{} },
		forges.WithInventoryPush(versions, hub))
	n := &forgeNotifier{hub: hub, poller: poller, names: names}
	before, _ := versions.Current(subject.KindForgeInventory, "github:github.com")

	n.notify(t.Context(), forges.Change{ID: "github:github.com", AccountChanged: true})

	if len(hub.seen) != 1 || hub.seen[0] == before {
		t.Errorf("inventory version at the broadcast = %v, was %q: want it already moved by the drop", hub.seen, before)
	}
}

// Wiring the notifier after the manager makes construction order load-bearing,
// so a change notified before the poller is wired says so and sends nothing.
func TestForgeNotify_BeforeWiringLogsAndBroadcastsNothing(t *testing.T) {
	logs := captureDefaultLogger(t)
	hub := &recordingHub{}
	n := &forgeNotifier{hub: hub}

	n.notify(t.Context(), forges.Change{ID: "github:github.com", AccountChanged: true})

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("notify before wiring logged %d lines, want one:\n%s", len(lines), logs)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode the line %q: %v", lines[0], err)
	}
	if rec["level"] != "ERROR" || rec["msg"] != "forges: a change was notified before the forge poller was wired" ||
		rec["connection"] != "github:github.com" {
		t.Errorf("logged %v, want the one Error line naming the connection", rec)
	}
	if got := hub.count(); got != 0 {
		t.Errorf("notify before wiring broadcast %d frames, want none", got)
	}
}
