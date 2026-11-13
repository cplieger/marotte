package applogin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/ratelimit"
	"github.com/cplieger/go/ssrf"
	"github.com/cplieger/marotte/internal/filemode"
)

// Options is one construction attempt's input. Unauthorized, OnEvent and Breach are the
// production values in composition and recording ones in a test.
type Options struct {
	Disk         filemode.Disk
	Proxies      *Proxies
	Origin       *Origin
	Agent        access.CredentialVerifier
	Unauthorized func(http.ResponseWriter, *http.Request, error)
	OnEvent      func(account.Event)
	Breach       http.RoundTripper // nil: an ssrf-guarded HTTPS transport
	Path         string
	// MemoryLimit is the container's memory limit in bytes; 0 reads the cgroup.
	MemoryLimit int64
}

// Attempt is one construction attempt. From the limiter's creation onward it owns everything the
// attempt started, and stop is its only cleanup path.
type Attempt struct {
	cancel  context.CancelFunc
	start   func(origin *Origin, sealer *access.Sealer) <-chan struct{}
	store   *Store
	authn   *access.Authenticator
	svc     *account.Service
	limiter *ratelimit.Limiter
	builder <-chan struct{}
	stopped chan struct{}
	reset   Reset
	// The three flags name what the watchdog's ERROR reports as not stopped.
	svcDone, limiterDone, builderDone atomic.Bool
	stopOnce                          sync.Once
	mu                                sync.Mutex
	// stopping, under mu, refuses a builder start once stop has read builder: one started later
	// would run unwaited past Stopped and the store's Close.
	stopping bool
}

// stopWatchdog is how long stop waits before its ERROR; a variable so tests can shorten it.
var stopWatchdog = 10 * time.Second

// stopHook, tests only, runs in stop's goroutine after the service, limiter and builder have
// stopped and before Stopped closes.
var stopHook func()

// buildRefuseHook, tests only, refuses an attempt after account.New.
var buildRefuseHook func() error

// Tests only: cleanupInterval shortens the library's sweep (0 keeps its default), and hashParams
// swaps in cheaper valid Argon2 parameters.
var (
	cleanupInterval time.Duration
	hashParams      = access.DefaultArgon2Params
)

// Store is the attempt's store; nil when OpenStore failed.
func (a *Attempt) Store() *Store { return a.store }

// Authenticator is the attempt's authenticator; nil when the chain stopped before it.
func (a *Attempt) Authenticator() *access.Authenticator { return a.authn }

// Service is the attempt's account service; nil unless Build succeeded.
func (a *Attempt) Service() *account.Service { return a.svc }

// Reset returns the Reset OpenStore returned beside the store, zero otherwise.
func (a *Attempt) Reset() Reset { return a.reset }

// Stopped is closed once the attempt's service, limiter and builder have stopped.
func (a *Attempt) Stopped() <-chan struct{} { return a.stopped }

// StartProviderBuilder starts the store's provider builder under the attempt's context; composition
// calls it once it publishes the attempt's runtime. It does nothing once the attempt is stopping.
func (a *Attempt) StartProviderBuilder(origin *Origin, sealer *access.Sealer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.builder == nil && a.start != nil && !a.stopping {
		a.builder = a.start(origin, sealer)
	}
}

// stop begins ending the attempt and returns at once; later calls do nothing. It cancels the
// attempt's context, then in one goroutine shuts the service and the limiter down with a context
// that never ends, waits for the builder and the store, and closes Stopped. A watchdog logs one
// ERROR if that takes longer than stopWatchdog; the wait goes on.
func (a *Attempt) stop() {
	a.stopOnce.Do(func() {
		a.cancel()
		a.mu.Lock()
		a.stopping = true
		builder := a.builder
		a.mu.Unlock()
		// The seams are read here, not in the goroutines: a test resetting them would race a
		// previous test's attempt still stopping.
		hook, bound := stopHook, stopWatchdog
		go func() {
			defer close(a.stopped)
			if a.svc != nil {
				_ = a.svc.Shutdown(context.Background())
			}
			a.svcDone.Store(true)
			if a.limiter != nil {
				_ = a.limiter.Shutdown(context.Background())
			}
			a.limiterDone.Store(true)
			if builder != nil {
				<-builder
			}
			a.builderDone.Store(true)
			if a.store != nil {
				a.store.Close()
			}
			if hook != nil {
				hook()
			}
		}()
		go a.watch(bound)
	})
}

func (a *Attempt) watch(bound time.Duration) {
	t := time.NewTimer(bound)
	defer t.Stop()
	select {
	case <-a.stopped:
		return
	case <-t.C:
	}
	var running []string
	for _, c := range []struct {
		done *atomic.Bool
		name string
	}{{&a.svcDone, "service"}, {&a.limiterDone, "limiter"}, {&a.builderDone, "builder"}} {
		if !c.done.Load() {
			running = append(running, c.name)
		}
	}
	slog.Error("access: a sign-in attempt has not stopped", "after", bound, "not_stopped", running)
}

// Build runs the constructor chain from OpenStore to account.New. On a refusal it
// returns the attempt, already stopping, with the error at once; the caller waits on Stopped
// before its next attempt.
func Build(ctx context.Context, opts Options) (*Attempt, error) { //nolint:gocritic // hugeParam: one construction input
	actx, cancel := context.WithCancel(ctx)
	a := &Attempt{cancel: cancel, stopped: make(chan struct{})}
	refuseEarly := func(err error) (*Attempt, error) {
		cancel()
		if a.store != nil {
			a.store.Close()
		}
		close(a.stopped)
		a.stopOnce.Do(func() {})
		return a, err
	}
	store, reset, err := OpenStore(actx, opts.Path, opts.Disk)
	if err != nil {
		return refuseEarly(err)
	}
	a.store, a.reset = store, reset
	store.SetOriginValid(opts.Origin.Valid())
	authn, err := access.New(store, access.Config{
		Cookie:           opts.Proxies.CookieConfig(),
		IdleTimeout:      defaultIdle,
		AbsTimeout:       defaultAbsolute,
		TimeoutSource:    store.PolicyTimeouts,
		ActivityThrottle: time.Minute,
		Verifier:         opts.Agent,
		ClientAddr:       opts.Proxies.ClientAddr,
		Unauthorized:     opts.Unauthorized,
	})
	if err != nil {
		return refuseEarly(fmt.Errorf("access: build the authenticator: %w", err))
	}
	a.authn = authn
	ring, err := access.NewSealer(KeyBytes(store.DeviceKeys()...)...)
	if err != nil {
		return refuseEarly(fmt.Errorf("access: build the device sealer: %w", err))
	}
	lcfg := ratelimit.DefaultConfig()
	lcfg.Devices = ring
	limiter, err := ratelimit.New(actx, lcfg)
	if err != nil {
		return refuseEarly(fmt.Errorf("access: build the limiter: %w", err))
	}
	a.limiter = limiter
	if err := a.finish(actx, &opts, store, authn, limiter); err != nil {
		a.stop()
		return a, err
	}
	a.start = func(origin *Origin, sealer *access.Sealer) <-chan struct{} {
		return StartProviderBuilder(actx, store, origin, sealer)
	}
	return a, nil
}

// finish builds the hasher and the account service onto a; from here every refusal is stop's.
func (a *Attempt) finish(ctx context.Context, opts *Options, store *Store, authn *access.Authenticator, limiter *ratelimit.Limiter) error {
	params := hashParams()
	limit := opts.MemoryLimit
	if limit <= 0 {
		limit = cgroupMemoryLimit()
	}
	budget, err := access.NewHashBudget(HashBudgetBytes(limit, params))
	if err != nil {
		return fmt.Errorf("access: build the hash budget: %w", err)
	}
	hasher, err := access.NewHasher(access.HasherConfig{Params: params, Budget: budget})
	if err != nil {
		return fmt.Errorf("access: build the hasher: %w", err)
	}
	svc, err := account.New(ctx, account.Config{
		Store: store, Policy: account.SingleOwnerPolicy{}, Authenticator: authn,
		Hasher: hasher, Limiter: limiter, Rules: store.Rules,
		FailureKey: KeyBytes(store.FailureKey())[0],
		Breach:     BreachClient(opts.Breach),
		RP:         opts.Origin.RP, OIDC: store.OIDCProvider, OnEvent: opts.OnEvent,
		LogMaxChanges: 1000, LogMaxSessions: 1000, CleanupInterval: cleanupInterval,
		// Single owner: no key reaches an account operation. Spelled out so
		// turning one on is a visible diff.
		AdminScope: "", ManageUsersScope: "", CreateKeysScope: "",
	})
	if err != nil {
		return fmt.Errorf("access: build the account service: %w", err)
	}
	a.svc = svc
	store.bindLog(svc.AppendLog)
	if buildRefuseHook != nil {
		return buildRefuseHook()
	}
	return nil
}

// BreachClient is the breached-password check's outbound client; a nil rt is an ssrf-guarded
// HTTPS transport, so turning the check on never makes Rules invalid.
func BreachClient(rt http.RoundTripper) *http.Client {
	if rt == nil {
		rt = ssrf.SafeTransport(ssrf.WithAllowedPorts(443))
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: rt}
}

// HashBudgetBytes bounds concurrent password hashes to a quarter of limit, between 1 and 8
// derivations.
func HashBudgetBytes(limit int64, params access.Argon2Params) int64 {
	cost := access.HashCost(params)
	n := min(max(limit/4/cost, 1), 8)
	return n * cost
}

// cgroupMemoryLimit is the container's memory limit, or the host's memory when the cgroup sets
// none or cannot be read.
func cgroupMemoryLimit() int64 {
	raw, _ := os.ReadFile("/sys/fs/cgroup/memory.max")
	meminfo, _ := os.ReadFile("/proc/meminfo")
	return parseMemoryLimit(string(raw), string(meminfo))
}

// parseMemoryLimit reads cgroup v2's memory.max, where "max" means none, falling back to
// /proc/meminfo's MemTotal; 0 when neither parses.
func parseMemoryLimit(memoryMax, meminfo string) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(memoryMax), 10, 64); err == nil && v > 0 {
		return v
	}
	for line := range strings.SplitSeq(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
