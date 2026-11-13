package apitable_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/apitable"
	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/trigger"
	"github.com/cplieger/toolbelt/v3"
)

// noTools is a tools source whose engine never comes up; the routes still bind.
type noTools struct{}

func (noTools) Engine() (*toolbelt.Engine, error) { return nil, context.Canceled }

// composedServer is the server composition builds, with every optional surface on: tools, forges,
// preview, metrics, the notify door, the launch-rule store with its trigger confirmer and the
// kiro-cli rescan. entries replaces the table when non-nil.
func composedServer(t *testing.T, entries []route.Entry) *server.Server {
	t.Helper()
	ctx := t.Context()
	configDir, workDir := t.TempDir(), t.TempDir()
	chats, err := chat.NewStore(filepath.Join(configDir, "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	rules, states, err := launchrule.NewStore(configDir)
	if err != nil {
		t.Fatalf("launch-rule store: %v", err)
	}
	leases, err := runlease.NewStore(configDir)
	if err != nil {
		t.Fatalf("run leases: %v", err)
	}
	mcpStore, err := mcp.New(ctx, configDir, nil)
	if err != nil {
		t.Fatalf("mcp store: %v", err)
	}
	pushSvc := push.New(ctx, configDir, "mailto:test@example.com")
	rt := agent.New(ctx, workDir, nil, chats,
		agent.WithConfigDir(configDir), agent.WithLaunchRules(rules), agent.WithPush(pushSvc),
		agent.WithTriggerConfirmer(trigger.NewConfirmer(rules, states, leases)))
	t.Cleanup(func() {
		if err := rt.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	sensitive := filebrowse.NewSensitive(configDir)
	files, err := filebrowse.New(sensitive, []string{workDir})
	if err != nil {
		t.Fatalf("file browser: %v", err)
	}
	signer, err := preview.NewSigner()
	if err != nil {
		t.Fatalf("preview signer: %v", err)
	}
	tabStore, err := tabs.NewStore(configDir)
	if err != nil {
		t.Fatalf("tab store: %v", err)
	}
	approvals, err := specapproval.NewStore(configDir)
	if err != nil {
		t.Fatalf("spec approvals: %v", err)
	}
	if entries == nil {
		entries = apitable.Entries()
	}
	return server.New(
		server.WithEndpoints(entries, apitable.OpenAPI),
		server.WithAgent(rt),
		server.WithChats(chats),
		server.WithGit(git.NewHandler(workDir)),
		server.WithFiles(files),
		server.WithAuth(auth.NewHandler(signin.Open(ctx, configDir), "kiro")),
		server.WithPush(pushSvc),
		server.WithMCPConfig(mcpStore),
		server.WithMCPStatus(rt.MCPRegistry()),
		server.WithMCPRegistry(mcp.NewRegistryProxy()),
		server.WithForges(forges.NewHTTPHandler(forges.NewManager(configDir, nil))),
		server.WithPreview(preview.New(workDir, sensitive, signer, nil)),
		server.WithMetrics(obs.Handler()),
		server.WithTools(noTools{}),
		server.WithSteering(steering.New(steering.Config{WorkDir: workDir, ConfigDir: configDir, ToolsDir: t.TempDir()})),
		server.WithAccountUsage(rt),
		server.WithPolicy(rt.Config()),
		server.WithGovernanceLocks(rt.Config()),
		server.WithKiroDefaults(rt.Config()),
		server.WithPolicyReload(rt),
		server.WithTabs(tabStore),
		server.WithSpecApprovals(approvals),
		// Composition always sets it; a path that does not exist answers as kiro-cli not installed.
		server.WithKiroCLI(func() string { return filepath.Join(configDir, "no-kiro-cli") }, func() []string { return nil }),
		server.WithStaticFS(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}),
		server.WithKiroRescan(func(context.Context) (bool, error) { return true, nil }),
		server.WithConfigDir(configDir),
		server.WithWorkDir(workDir),
		server.WithSensitive(sensitive),
		server.WithNotifyDeliverer(rt.Notifier()),
	)
}

// bindings is every binding the composed server makes, in order.
func bindings(t *testing.T) []route.Binding {
	t.Helper()
	var rec recorder
	composedServer(t, nil).Register(&rec)
	if len(rec.bs) == 0 {
		t.Fatal("the composed server bound nothing")
	}
	return rec.bs
}

type recorder struct {
	mu sync.Mutex
	bs []route.Binding
}

func (r *recorder) Bind(b route.Binding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bs = append(r.bs, b)
}

var (
	wildcardTail    = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\.\.\.\}`)
	wildcardSegment = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
)

// concretePath turns a registered path into one a request can address.
func concretePath(path string) string {
	path = wildcardTail.ReplaceAllString(path, "probe/probe")
	path = wildcardSegment.ReplaceAllString(path, "probe")
	path = strings.TrimSuffix(path, "{$}")
	if strings.HasSuffix(path, "/") {
		path += "probe"
	}
	return path
}

// serveLoopback serves one request from a loopback peer, so loopback-only routes answer too.
func serveLoopback(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "http://127.0.0.1:9847"+path, http.NoBody)
	req.RemoteAddr = "127.0.0.1:50000"
	mux.ServeHTTP(rec, req)
	return rec
}

func isSafeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }
