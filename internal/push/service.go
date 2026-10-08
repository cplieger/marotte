package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/ssrf/v4"
)

// DefaultTitle is the notification title used for all Web Push messages.
const DefaultTitle = "Marotte"

// pushDebounce is the per-subject quiet window; pushBodyCap caps title+body so an
// oversize send is not silently rejected by the vendor.
const (
	pushDebounce    = 5 * time.Second
	pushResponseCap = 64 << 10 // 64 KiB — vendors return tiny bodies
	pushBodyCap     = 3000     // title+body ceiling; pre-pad room under 4096 record
	pushFanOutLimit = 3        // max concurrent push sends per notification

	pushMaxAttempts = 3 // total tries for a retryable delivery failure

	// debounceHighWater is when preflightSend prunes expired debounce entries: per-subject
	// keys would otherwise grow one slot per chat and PR ever notified about.
	debounceHighWater = 64
)

// pushSubjectGlobal is the debounce subject for a notification with nothing single behind
// it. Not a legal subject spelling (chat ids have no space, PR keys start `pr:`).
const pushSubjectGlobal = "<workspace global>"

// pushDebounceKey is what a quiet window belongs to: one KIND about one SUBJECT. A
// kind-only window drops a second subject's send (the poller has already advanced its
// `seen` state), so coalescing two subjects is data loss.
type pushDebounceKey struct {
	kind    marotte.PushKind
	subject string
}

// debounceKey builds the window key for one send.
func debounceKey(kind marotte.PushKind, subject marotte.PushSubject) pushDebounceKey {
	switch {
	case subject.ChatID != "":
		return pushDebounceKey{kind: kind, subject: string(subject.ChatID)}
	case subject.Key != "":
		return pushDebounceKey{kind: kind, subject: subject.Key}
	default:
		return pushDebounceKey{kind: kind, subject: pushSubjectGlobal}
	}
}

// Retry timing for a 429/5xx. Vars so a test can collapse the ladder. The budget is the
// notification's USEFULNESS window: a delivery landing later is worse than none.
var (
	pushRetryBudget = 60 * time.Second
	pushRetryBase   = 1 * time.Second
)

type vapidKeys struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// Service manages push subscriptions and sends notifications.
type Service struct {
	// lifetime is the service's OWN child of New's ctx: the writeLoop-liveness signal.
	// Merging it with a caller's ctx reopens a shutdown hang (see persist.go's guarded waits).
	lifetime      context.Context
	saveCh        chan saveRequest
	writeLoopDone chan struct{}
	cancel        context.CancelFunc
	client        *http.Client
	lastPush      map[pushDebounceKey]time.Time
	subs          map[string]marotte.PushSubscription
	prefs         map[marotte.PushKind]bool
	keys          vapidKeys
	vapidPriv     *ecdsa.PrivateKey
	// presence is the send filter's input: a subscription whose profile reads
	// present is receiving the event on its stream and is not pushed. nil sends
	// to every subscription, which is the fail-open direction.
	presence *Presence
	// suppressed counts filter skips per kind for the test-only probe; zero while presence
	// shows attended profiles means the tags are not matching.
	suppressed map[marotte.PushKind]*atomic.Uint64
	subject    string
	dir        string
	// deferred is the held set of the deferred-send variant (deferred.go); empty
	// for the life of the process while that switch is off.
	deferred deferred
	mu       sync.Mutex
	healthy  bool
	// keysGenerated records that loadKeys minted a new keypair (every stored subscription is
	// now undeliverable). Unguarded: New sets and reads it before the write loop starts.
	keysGenerated bool
}

// saveRequest pairs a subscription snapshot with a done channel
// so the caller can wait for the write to complete.
type saveRequest struct {
	done chan struct{}
	subs []marotte.PushSubscription
}

// Option configures a Service at construction.
type Option func(*Service)

// WithPresence wires the presence table the send filter reads. Absent, nothing is
// filtered and every subscription is pushed.
func WithPresence(p *Presence) Option {
	return func(s *Service) { s.presence = p }
}

// New creates a Service, loads persisted subscriptions and preferences, and starts the
// write loop. subject is the VAPID subject. ctx is the service's lifetime and is required.
func New(ctx context.Context, configDir, subject string, opts ...Option) *Service {
	ctx, cancel := context.WithCancel(ctx)
	prefs := make(map[marotte.PushKind]bool, len(kindRegistry))
	suppressed := make(map[marotte.PushKind]*atomic.Uint64, len(kindRegistry))
	for _, kr := range kindRegistry {
		prefs[kr.Kind] = kr.DefaultOn
		suppressed[kr.Kind] = new(atomic.Uint64)
	}
	s := &Service{
		subs:          make(map[string]marotte.PushSubscription),
		lastPush:      make(map[pushDebounceKey]time.Time),
		suppressed:    suppressed,
		subject:       subject,
		dir:           configDir,
		lifetime:      ctx,
		cancel:        cancel,
		prefs:         prefs,
		saveCh:        make(chan saveRequest, 1),
		writeLoopDone: make(chan struct{}),
		healthy:       true,
	}
	for _, o := range opts {
		o(s)
	}
	// isAllowedPushEndpoint is the name-based gate; ssrf.SafeTransport re-validates the
	// connected IP (DNS rebinding), and CheckRedirect re-checks the allowlist on every hop.
	pushTransport := ssrf.SafeTransport(
		ssrf.WithAllowedPorts(443),
	)
	pushTransport.MaxIdleConnsPerHost = 2
	pushTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	s.client = &http.Client{
		Timeout:   10 * time.Second,
		Transport: pushTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("push: too many redirects")
			}
			if !isAllowedPushEndpoint(req.URL.String()) {
				return errors.New("push: redirect to non-allowed host")
			}
			return nil
		},
	}
	s.loadKeys()
	s.loadSubs()
	s.loadPreferences(s.lifetime)
	go s.writeLoop()
	s.mu.Lock()
	n := len(s.subs)
	s.mu.Unlock()
	slog.Info("push: ready", "subscribers", n, "healthy", s.healthy)
	return s
}

// Close cancels in-flight pushes and waits for the write loop to drain pending saves.
func (s *Service) Close() {
	s.cancel()
	s.deferred.stop()
	<-s.writeLoopDone
}

// PublicKey returns the VAPID public key used for push subscription registration.
func (s *Service) PublicKey() string { return s.keys.PublicKey }

// SetPreferences sets the enabled flag of each kind prefs names; a kind it omits keeps its flag.
func (s *Service) SetPreferences(prefs map[marotte.PushKind]bool) {
	s.mu.Lock()
	maps.Copy(s.prefs, prefs)
	s.mu.Unlock()
}

// Preferences returns a copy of the per-kind enabled flags the service enforces now.
func (s *Service) Preferences() map[marotte.PushKind]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.prefs)
}

// Subscribe registers a push subscription endpoint. Duplicate endpoints are silently overwritten.
func (s *Service) Subscribe(sub marotte.PushSubscription) {
	s.mu.Lock()
	s.subs[sub.Endpoint] = sub
	s.mu.Unlock()
	s.saveSubsAsync(s.lifetime)
	// Log only the host: the URL path carries the per-subscriber token.
	host := "unknown"
	if u, err := url.Parse(sub.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	slog.Info("push: subscribed", "host", host)
}

// Unsubscribe removes the subscription for the given push endpoint.
func (s *Service) Unsubscribe(endpoint string) {
	s.mu.Lock()
	delete(s.subs, endpoint)
	s.mu.Unlock()
	s.saveSubsAsync(s.lifetime)
}

// HasSubscribers reports whether any push subscriptions are currently registered.
func (s *Service) HasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// Wants reports whether a send of kind would pass the service's own gates: the
// service is healthy, the kind is registered and enabled, and a subscription exists
// to receive it. The presence filter is not consulted, because a profile that reads
// present is a reader rather than a reason to withhold the event.
func (s *Service) Wants(kind marotte.PushKind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy && kind.Valid() && s.prefs[kind] && len(s.subs) > 0
}

// kindRegistry is the single source of truth for push kinds (validated against
// marotte.PushKind.Valid at init). An empty SettingsKey is an unsilenceable floor
// (PushKindPermission only); keyed entries take their defaults from settings.Default*.
var kindRegistry = []KindPref{
	{marotte.PushKindAgentFinished, settings.KeyNotifyAgentFinished, settings.DefaultNotifyAgentFinished},
	{marotte.PushKindPRStatus, settings.KeyNotifyPRStatus, settings.DefaultNotifyPRStatus},
	{marotte.PushKindRunOutcome, settings.KeyNotifyRunOutcome, settings.DefaultNotifyRunOutcome},
	{marotte.PushKindPermission, "", true},
}

// KindPref is one registered kind. Exported so the settings write path can
// derive its preference map from this registry instead of keeping a third
// hand-maintained copy beside marotte.pushKinds.
type KindPref struct {
	Kind        marotte.PushKind
	SettingsKey string
	DefaultOn   bool
}

// Kinds returns the registered kinds and their settings keys. A caller
// wanting only the configurable ones filters on a non-empty SettingsKey.
// Returns a copy so no consumer can reorder or extend the registry.
func Kinds() []KindPref { return slices.Clone(kindRegistry) }

func init() {
	if err := validateKindRegistry(kindRegistry); err != nil {
		panic("push: " + err.Error())
	}
}

// validateKindRegistry enforces the two rules the registry's types cannot: the
// registry must agree with marotte.PushKind.Valid(), and an empty SettingsKey
// (an unconfigurable floor) is legal only for the permission kind, and only
// when DefaultOn — otherwise a forgotten key would silently ship a
// permanently-on, unwritable toggle.
func validateKindRegistry(entries []KindPref) error {
	for _, kr := range entries {
		if !kr.Kind.Valid() {
			return errors.New("kindRegistry contains invalid PushKind: " + string(kr.Kind))
		}
		if kr.SettingsKey != "" {
			continue
		}
		if kr.Kind != marotte.PushKindPermission {
			return errors.New("kindRegistry entry " + string(kr.Kind) +
				" declares no settings key; only the permission floor may omit one")
		}
		if !kr.DefaultOn {
			return errors.New("the keyless permission floor must be DefaultOn: " +
				"an unanswered ask blocks the turn")
		}
	}
	return nil
}

// writeLoop drains saveCh and writes the latest snapshot; the single writer goroutine.
func (s *Service) writeLoop() {
	defer close(s.writeLoopDone)
	for {
		select {
		case req, ok := <-s.saveCh:
			if !ok {
				return
			}
			s.writeSubsSnapshot(req.subs)
			close(req.done)
		case <-s.lifetime.Done():
			for {
				select {
				case req := <-s.saveCh:
					s.writeSubsSnapshot(req.subs)
					close(req.done)
				default:
					return
				}
			}
		}
	}
}

func (s *Service) loadPreferences(ctx context.Context) {
	// Resolved without holding mu: settings.Field does disk I/O.
	local := ResolvePreferences(ctx, s.dir)
	s.mu.Lock()
	s.prefs = local
	s.mu.Unlock()
}

// ResolvePreferences resolves every registered kind's enabled flag from configDir's config.json:
// the per-kind toggles, then the master switch. A missing or unparseable value takes the kind's
// kindRegistry default.
func ResolvePreferences(ctx context.Context, configDir string) map[marotte.PushKind]bool {
	prefs := make(map[marotte.PushKind]bool, len(kindRegistry))
	for _, kr := range kindRegistry {
		if kr.SettingsKey == "" {
			prefs[kr.Kind] = kr.DefaultOn
			continue
		}
		if v, ok := settings.Field[bool](ctx, configDir, kr.SettingsKey); ok {
			prefs[kr.Kind] = v
		} else {
			prefs[kr.Kind] = kr.DefaultOn
		}
	}
	// The master switch is applied LAST so nothing re-widens it. Only an explicit false
	// zeroes: its default is off while each kind has its own, so absent is not a decision.
	if enabled, ok := settings.Field[bool](ctx, configDir, settings.KeyNotificationsEnabled); ok && !enabled {
		for kind := range prefs {
			prefs[kind] = false
		}
	}
	return prefs
}
