package applogin

import (
	"bytes"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/go/slogx/capture"
)

func TestConstruct_ChainOverTheStore(t *testing.T) {
	h := newHarness(t)
	if h.svc == nil || h.authn == nil {
		t.Fatal("Build returned no service or authenticator")
	}
	for name, got := range map[string]string{"AdminScope": h.svc.AdminScope(), "ManageUsersScope": h.svc.ManageUsersScope(), "CreateKeysScope": h.svc.CreateKeysScope()} {
		if got != "" {
			t.Errorf("%s = %q, want empty", name, got)
		}
	}
	if _, err := access.New(UnavailableStore{Cause: errors.New("test: no store")}, access.Config{
		Cookie: ParseProxies("").CookieConfig(), Verifier: h.agent,
	}); err != nil {
		t.Errorf("access.New over UnavailableStore = %v", err)
	}
}

func TestConstruct_NilStoreRefused(t *testing.T) {
	if _, err := access.New((*Store)(nil), access.Config{}); err == nil {
		t.Error("access.New accepted a typed-nil *Store")
	}
}

// accessGoroutines lists the goroutines running a frame of the library's module.
func accessGoroutines() map[string]bool {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	out := map[string]bool{}
	for g := range bytes.SplitSeq(buf, []byte("\n\n")) {
		header, _, _ := bytes.Cut(g, []byte("\n"))
		if bytes.Contains(g, []byte("github.com/cplieger/access.")) || bytes.Contains(g, []byte("github.com/cplieger/access/")) {
			id := strings.Fields(string(header))
			if len(id) > 1 {
				out[id[1]] = true
			}
		}
	}
	return out
}

func leaked(before map[string]bool) []string {
	var extra []string
	deadline := time.Now().Add(2 * time.Second)
	for {
		extra = extra[:0]
		for id := range accessGoroutines() {
			if !before[id] {
				extra = append(extra, id)
			}
		}
		if len(extra) == 0 || time.Now().After(deadline) {
			return extra
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func refuseAfterAccountNew(t *testing.T) {
	buildRefuseHook = func() error { return errors.New("test: refused after account.New") }
	t.Cleanup(func() { buildRefuseHook = nil })
}

func TestConstruct_RefusedAttemptStopsServiceAndLimiter(t *testing.T) {
	refuseAfterAccountNew(t)
	d := newTestDisk(t)
	before := accessGoroutines()
	for i := range 10 {
		a, err := Build(t.Context(), Options{Disk: d, Path: d.path, Proxies: ParseProxies(""), Origin: parseOrigin(testOrigin, nil), MemoryLimit: 1 << 30})
		if err == nil {
			t.Fatalf("attempt %d: Build = nil, want the injected refusal", i)
		}
		select {
		case <-a.Stopped():
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: Stopped never closed", i)
		}
	}
	if extra := leaked(before); len(extra) != 0 {
		t.Errorf("goroutines of the library still running after the refused attempts stopped: %v", extra)
	}
}

func TestConstruct_RefusedAttemptHeldPastWatchdog(t *testing.T) {
	logs := capture.Default(t)
	refuseAfterAccountNew(t)
	stopWatchdog = 20 * time.Millisecond
	release := make(chan struct{})
	stopHook = func() { <-release }
	t.Cleanup(func() { stopWatchdog, stopHook = 10*time.Second, nil })
	d := newTestDisk(t)
	before := accessGoroutines()
	start := time.Now()
	a, err := Build(t.Context(), Options{Disk: d, Path: d.path, Proxies: ParseProxies(""), Origin: parseOrigin(testOrigin, nil), MemoryLimit: 1 << 30})
	if err == nil {
		t.Fatal("Build = nil, want the injected refusal")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Build took %s to return the refusal", took)
	}
	time.Sleep(stopWatchdog + 200*time.Millisecond)
	select {
	case <-a.Stopped():
		t.Fatal("Stopped closed while stopHook held the cleanup")
	default:
	}
	const msg = "access: a sign-in attempt has not stopped"
	if n := logs.CountExact(msg); n != 1 {
		t.Errorf("watchdog ERROR logged %d times, want 1", n)
	}
	if v, ok := logs.AttrValueExact(msg, "after"); !ok || v != stopWatchdog.String() {
		t.Errorf("watchdog ERROR after = (%q, %t), want %s", v, ok, stopWatchdog)
	}
	close(release)
	select {
	case <-a.Stopped():
	case <-time.After(5 * time.Second):
		t.Fatal("Stopped never closed after the release")
	}
	if extra := leaked(before); len(extra) != 0 {
		t.Errorf("library goroutines left after the release: %v", extra)
	}
}

func TestConstruct_BuilderStartAfterStopIsRefused(t *testing.T) {
	d := newTestDisk(t)
	a, err := Build(t.Context(), Options{Disk: d, Path: d.path, Proxies: ParseProxies(""), Origin: parseOrigin(testOrigin, nil), MemoryLimit: 1 << 30})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var starts atomic.Int32
	start := a.start
	a.start = func(origin *Origin, sealer *access.Sealer) <-chan struct{} {
		starts.Add(1)
		return start(origin, sealer)
	}
	a.stop()
	a.StartProviderBuilder(parseOrigin(testOrigin, nil), access.NewEphemeralSealer())
	select {
	case <-a.Stopped():
	case <-time.After(5 * time.Second):
		t.Fatal("Stopped never closed")
	}
	if n := starts.Load(); n != 0 {
		t.Errorf("a builder started %d times after stop, want none: nothing would wait for it", n)
	}
}

func TestHashBudgetBytes(t *testing.T) {
	params := access.DefaultArgon2Params()
	cost := access.HashCost(params)
	for _, c := range []struct {
		name  string
		limit int64
		n     int64
	}{
		{"none_read", 0, 1},
		{"tiny_clamps_to_one", cost, 1},
		{"three", 3 * 4 * cost, 3},
		{"huge_clamps_to_eight", 1 << 40, 8},
	} {
		if got := HashBudgetBytes(c.limit, params); got != c.n*cost {
			t.Errorf("%s: HashBudgetBytes(%d) = %d, want %d", c.name, c.limit, got, c.n*cost)
		}
	}
	const meminfo = "MemTotal:       16384 kB\nMemFree: 1 kB\n"
	for _, c := range []struct {
		name, max string
		want      int64
	}{
		{"max_means_none", "max\n", 16384 * 1024},
		{"limit", strconv.Itoa(512<<20) + "\n", 512 << 20},
		{"unreadable", "", 16384 * 1024},
	} {
		if got := parseMemoryLimit(c.max, meminfo); got != c.want {
			t.Errorf("%s: parseMemoryLimit = %d, want %d", c.name, got, c.want)
		}
	}
}
