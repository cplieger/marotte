package forges

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// Marotte's own OAuth applications on github.com and gitlab.com. Both ids are
// public: a device grant sends no client secret.
const (
	githubOAuthClientID = "Ov23li9ja8ak5hoZACXH"
	gitlabOAuthClientID = "55c83c54905fe86fc799b972eed5fe8c3af32f59bf7ac35d70b92240d45403ea"
	gitlabAPIScope      = "api"
)

var (
	githubOAuthScopes = []string{"repo", "read:org", "workflow"}
	gitlabOAuthScopes = []string{gitlabAPIScope}
)

// grantSweepGrace keeps an expired grant answerable for a while, so a client's
// last poll reads the expiry rather than an unknown id.
const grantSweepGrace = time.Minute

// maxHeldGrants bounds the grants held at once, a cancel freeing its place: any
// caller can start one, and each start sends a device-code request upstream.
const maxHeldGrants = 8

var (
	errGrantNotFound = errors.New("forges: no device grant in progress under that id")
	errTooManyGrants = errors.New("forges: too many sign-ins are in progress. Finish or cancel one, then try again")
)

// marotteApp is the grant request for Marotte's own application on family's
// public instance, and false for a family with no device grant. It is the one
// list of the families the device routes serve.
func marotteApp(family forgeapi.Family) (creds.GrantRequest, bool) {
	switch family {
	case forgeapi.FamilyGitHub:
		return creds.GrantRequest{ClientID: githubOAuthClientID, Scopes: grantScopes(family)}, true
	case forgeapi.FamilyGitLab:
		return creds.GrantRequest{ClientID: gitlabOAuthClientID, Scopes: grantScopes(family)}, true
	}
	return creds.GrantRequest{}, false
}

// grantScopes are what a device grant on family asks for, whichever
// application it signs in with (ADR-0068).
func grantScopes(family forgeapi.Family) []string {
	if family == forgeapi.FamilyGitLab {
		return gitlabOAuthScopes
	}
	return githubOAuthScopes
}

// grantRegistry holds the device grants in progress. A client names its grant
// by a random id, because the device code is a bearer secret for the grant's
// whole life. creds.DeviceGrant.Poll is not safe for concurrent use, so each
// grant is polled under its own lock, token request included; the map's lock
// is never held across a poll.
type grantRegistry struct {
	grants map[string]*heldGrant
	now    func() time.Time
	mu     sync.Mutex
	// starting counts the starts whose device-code request is in flight.
	starting int
}

type heldGrant struct {
	grant *creds.DeviceGrant
	conn  connectionRecord
	mu    sync.Mutex
	// cancelled is read again after a poll returns, so a cancel pressed while
	// the token request was in flight wins over its answer.
	cancelled atomic.Bool
}

// It never carries the device code.
type startedGrant struct {
	Expires         time.Time
	GrantID         string
	UserCode        string
	VerificationURI string
	Interval        time.Duration
}

// polledGrant is one poll's answer: the credential and the connection it signs
// in once Done.
type polledGrant struct {
	Record creds.Record
	Conn   connectionRecord
	Done   bool
}

func newGrantRegistry() *grantRegistry {
	return &grantRegistry{grants: make(map[string]*heldGrant), now: time.Now}
}

func (r *grantRegistry) start(ctx context.Context, rec *connectionRecord, req creds.GrantRequest) (startedGrant, error) {
	r.sweep()
	if !r.reserve() {
		return startedGrant{}, errTooManyGrants
	}
	g, err := creds.StartDeviceGrant(ctx, connectionFor(rec), rec.Kind.family(), req, connOptions(rec)...)
	id := newGrantID()
	r.mu.Lock()
	r.starting--
	if err == nil {
		r.grants[id] = &heldGrant{grant: g, conn: *rec}
	}
	r.mu.Unlock()
	if err != nil {
		return startedGrant{}, err
	}
	return startedGrant{
		GrantID: id, UserCode: g.UserCode, VerificationURI: g.VerificationURI,
		Interval: g.Interval, Expires: g.Expires,
	}, nil
}

// The poll that answers the credential releases the id, so exactly one caller saves it; an ended
// grant keeps answering its end until the sweep. A cancelled or unknown id is errGrantNotFound.
func (r *grantRegistry) poll(ctx context.Context, id string) (polledGrant, error) {
	r.sweep()
	h := r.lookup(id)
	if h == nil {
		return polledGrant{}, errGrantNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled.Load() {
		return polledGrant{}, errGrantNotFound
	}
	rec, done, err := h.grant.Poll(ctx)
	if err != nil {
		return polledGrant{}, err
	}
	if done {
		r.forget(id)
		if h.cancelled.Load() {
			return polledGrant{}, errGrantNotFound
		}
	}
	return polledGrant{Conn: h.conn, Record: rec, Done: done}, nil
}

// The grant leaves the registry at once and the library's Close ends it without a request,
// releasing its endpoint once a poll in flight returns; Close runs on its own goroutine so the
// cancel does not wait for that poll.
func (r *grantRegistry) cancel(id string) bool {
	h := r.lookup(id)
	if h == nil || h.cancelled.Swap(true) {
		return false
	}
	r.forget(id)
	go h.grant.Close()
	return true
}

// sweep forgets every grant past its expiry and grace and closes it, which ends
// it and releases its endpoint without a request.
func (r *grantRegistry) sweep() {
	now := r.now()
	var due []*heldGrant
	r.mu.Lock()
	for id, h := range r.grants {
		if !now.Before(h.grant.Expires.Add(grantSweepGrace)) {
			delete(r.grants, id)
			due = append(due, h)
		}
	}
	r.mu.Unlock()
	for _, h := range due {
		h.grant.Close()
	}
}

// reserve claims one start's place under maxHeldGrants, counting the starts
// still waiting on their device-code request.
func (r *grantRegistry) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.grants)+r.starting >= maxHeldGrants {
		return false
	}
	r.starting++
	return true
}

func (r *grantRegistry) lookup(id string) *heldGrant {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.grants[id]
}

func (r *grantRegistry) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.grants, id)
}

func newGrantID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
