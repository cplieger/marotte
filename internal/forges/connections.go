// Package forges integrates remote git forges (GitHub, GitLab, Gitea/Codeberg).
// Every connection is served by the forgeapi library, its credential in the
// library's store and its record in Marotte's own file beside it. No forge CLI
// is run, and no other program's configuration is read.
package forges

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/filemode"
)

const (
	connectionsFileName = "forge-connections.json"
	connectionsFileMode = 0o600
	// connectionsMaxBytes bounds the read and the write; a record is a few
	// kilobytes even with a CA bundle and a client certificate in it.
	connectionsMaxBytes = 4 << 20
)

// enforceFileMode is filemode.EnforceFile, reassignable so a test can stand
// in for a filesystem that will not store 0600.
var enforceFileMode = filemode.EnforceFile

// errConnectionsUnverified marks a record file whose mode could not be
// verified as 0600. Its records are listed, never used, and never rewritten.
var errConnectionsUnverified = errors.New("forge connection records unusable")

// connectionRecord is one forge connection as Marotte persists it: what is
// needed to reach the instance and to reconcile the git credential helper.
// The credential itself is the library's store; probe state stays in memory.
type connectionRecord struct {
	ID             string `json:"id"`
	Kind           Kind   `json:"kind"`
	Host           string `json:"host"`
	WebBaseURL     string `json:"web_base_url,omitempty"`
	APIBaseURL     string `json:"api_base_url,omitempty"`
	CAPEM          string `json:"ca_pem,omitempty"`
	ClientCertPEM  string `json:"client_cert_pem,omitempty"`
	ClientKeyPEM   string `json:"client_key_pem,omitempty"`
	Proxy          string `json:"proxy,omitempty"`
	OAuthClientID  string `json:"oauth_client_id,omitempty"`
	HelperValue    string `json:"helper_value,omitempty"`
	RotationCursor string `json:"rotation_cursor,omitempty"`
	// OwnerScopes are the owners a present cycle reads beside the connection's
	// own pull requests.
	OwnerScopes      []string `json:"owner_scopes,omitempty"`
	PrivateAddresses bool     `json:"private_addresses,omitempty"`
	PlaintextHTTP    bool     `json:"plaintext_http,omitempty"`
}

// maxOwnerScopes bounds a connection's owner scopes, each one list request
// every present cycle. A launch value.
const maxOwnerScopes = 20

// errTooManyOwners refuses an owner list over maxOwnerScopes once deduplicated.
var errTooManyOwners = fmt.Errorf("forges: at most %d owners, each one more list request a cycle", maxOwnerScopes)

// ownerScopes is owners as a connection of kind signed in as login stores them:
// each in the form WithOwner states for the family, the first spelling of an
// owner kept and later ones dropped case-insensitively, and at most
// maxOwnerScopes. Outside GitLab the owner scope already reads the login, so the
// login is dropped too.
func ownerScopes(kind Kind, login string, owners []string) ([]string, error) {
	family := kind.family()
	seen := make(map[string]bool, len(owners)+1)
	if family != forgeapi.FamilyGitLab && login != "" {
		seen[strings.ToLower(login)] = true
	}
	out := make([]string, 0, len(owners))
	for i, o := range owners {
		if err := forgeapi.ValidateOwner(family, o); err != nil {
			return nil, fmt.Errorf("owners[%d]: %w", i, err)
		}
		if key := strings.ToLower(o); !seen[key] {
			seen[key] = true
			out = append(out, o)
		}
	}
	if len(out) > maxOwnerScopes {
		return nil, errTooManyOwners
	}
	return out, nil
}

// webBase is the instance's web base URL, https on the record's host when
// the record names none.
func (r *connectionRecord) webBase() string {
	return cmp.Or(r.WebBaseURL, "https://"+r.Host)
}

type connectionsDoc struct {
	Connections []connectionRecord `json:"connections"`
}

// Kind identifies a forge backend.
type Kind string

// KindGitHub and the following constants define the valid Kind values.
const (
	KindGitHub   Kind = "github"
	KindGitLab   Kind = "gitlab"
	KindGitea    Kind = "gitea"    // also covers Codeberg (codeberg.org is a Gitea instance)
	KindCodeberg Kind = "codeberg" // synonym for KindGitea, host=codeberg.org
)

// kindMetaEntry holds the per-kind metadata used by the lookup methods.
type kindMetaEntry struct {
	DefaultHost string
}

// kindMeta holds every valid kind's metadata; a kind is valid exactly when it
// has an entry.
var kindMeta = map[Kind]kindMetaEntry{
	KindGitHub:   {DefaultHost: "github.com"},
	KindGitLab:   {DefaultHost: "gitlab.com"},
	KindCodeberg: {DefaultHost: "codeberg.org"},
	KindGitea:    {DefaultHost: ""},
}

// Valid reports whether k is a known forge kind.
func (k Kind) Valid() bool {
	_, ok := kindMeta[k]
	return ok
}

// DefaultHost returns the canonical hostname for the kind, or "" if
// no default exists (self-hosted Gitea/Forgejo).
func (k Kind) DefaultHost() string {
	return kindMeta[k].DefaultHost
}

// AllKinds returns every supported forge kind. Stable ordering for
// UI rendering.
func AllKinds() []Kind {
	return []Kind{KindGitHub, KindGitLab, KindCodeberg, KindGitea}
}

// ErrNotLoggedIn signals a forge with no usable connection.
var ErrNotLoggedIn = errors.New("forges: not logged in")

// ErrNotSupported signals the forge has no mechanism for the requested
// operation: not a failure to reach it, but an absent capability. The HTTP
// layer answers 501 so the client can hide the control instead of offering one
// that always fails.
var ErrNotSupported = errors.New("forges: operation not supported by this forge")

// family is the forgeapi family that serves k; codeberg is a Gitea instance.
func (k Kind) family() forgeapi.Family {
	switch k {
	case KindGitHub:
		return forgeapi.FamilyGitHub
	case KindGitLab:
		return forgeapi.FamilyGitLab
	case KindGitea, KindCodeberg:
		return forgeapi.FamilyGitea
	}
	return forgeapi.FamilyUnknown
}

// connectionStore is the connection record file. Every change is one
// read-modify-write under mu, so concurrent writers (a connect, the keeper's
// cursor write-back) cannot lose each other's update.
type connectionStore struct {
	path string
	mu   sync.Mutex
}

// load answers the records on disk. A missing file holds none, and so does an
// unparseable one, which is moved aside first. A file whose mode cannot be
// verified answers what it holds together with an errConnectionsUnverified
// verdict; any other read failure answers the failure alone.
func (s *connectionStore) load() ([]connectionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// update applies fn to the records and writes the result. It refuses to write
// when the load did not succeed, so a file it cannot read is never replaced.
func (s *connectionStore) update(ctx context.Context, fn func([]connectionRecord) []connectionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	return s.writeLocked(ctx, fn(recs))
}

func (s *connectionStore) loadLocked() ([]connectionRecord, error) {
	// The mode verdict on the name comes before any read: EnforceFile refuses a
	// symlink and cannot block on a FIFO, which a plain read would not.
	_, modeErr := enforceFileMode(s.path, connectionsFileMode)
	if errors.Is(modeErr, os.ErrNotExist) {
		return nil, nil
	}
	var verdict error
	if modeErr != nil {
		verdict = fmt.Errorf("%w: %s must be a regular file at mode %#o, and its mode could not be verified: %v",
			errConnectionsUnverified, s.path, connectionsFileMode, modeErr)
	}
	fh, _, err := atomicfile.OpenRegular(s.path)
	if err != nil {
		return nil, cmp.Or(verdict, fmt.Errorf("read %s: %w", s.path, err))
	}
	defer func() { _ = fh.Close() }()
	data, err := atomicfile.ReadBoundedFile(context.Background(), fh, connectionsMaxBytes)
	if err != nil {
		return nil, cmp.Or(verdict, fmt.Errorf("read %s: %w", s.path, err))
	}
	var doc connectionsDoc
	if uErr := json.Unmarshal(data, &doc); uErr != nil {
		if verdict != nil {
			return nil, verdict
		}
		return nil, s.moveAsideLocked(uErr)
	}
	return validRecords(doc.Connections), verdict
}

// validRecords drops a record whose kind is unknown or whose id is not the
// one its kind and host make, and every later record repeating an id.
func validRecords(recs []connectionRecord) []connectionRecord {
	out := make([]connectionRecord, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		dup := slices.ContainsFunc(out, func(o connectionRecord) bool { return o.ID == r.ID })
		if !r.Kind.Valid() || r.Host == "" || r.ID != MakeID(r.Kind, r.Host) || dup {
			slog.Warn("forges: dropping an invalid connection record", "id", r.ID, "kind", r.Kind, "host", r.Host)
			continue
		}
		out = append(out, *r)
	}
	return out
}

func (s *connectionStore) writeLocked(ctx context.Context, recs []connectionRecord) error {
	if recs == nil {
		recs = []connectionRecord{}
	}
	data, err := json.MarshalIndent(connectionsDoc{Connections: recs}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", connectionsFileName, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(connectionsFileMode), atomicfile.WithMaxBytes(connectionsMaxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", connectionsFileName, err)
	}
	return nil
}

// moveAsideLocked renames an unparseable record file to a timestamped name,
// the pattern internal/mcp's store uses, so a second corruption cannot
// destroy the first copy. A failed rename is returned: the file must not be
// overwritten in place.
func (s *connectionStore) moveAsideLocked(cause error) error {
	aside := fmt.Sprintf("%s.corrupt.%s.%d", s.path, time.Now().UTC().Format("20060102-150405"), os.Getpid())
	if err := os.Rename(s.path, aside); err != nil {
		return fmt.Errorf("%s is unparseable (%v) and could not be moved aside: %w", s.path, cause, err)
	}
	slog.Warn("forges: connection record file unparseable, moved aside; its connections must be reconnected",
		"from", s.path, "to", aside, "parse_error", cause)
	return nil
}
