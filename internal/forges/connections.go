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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/logsafe"
)

const (
	connectionsFileName = "forge-connections.json"
	connectionsFileMode = 0o600
	// connectionsMaxBytes bounds the read and the write; a record is a few
	// kilobytes even with a CA bundle and a client certificate in it.
	connectionsMaxBytes = 4 << 20
	// maxSignInBytes bounds a stored sign-in id; rand.Text answers 26 bytes.
	maxSignInBytes = 64
)

// enforceFileMode is filemode.EnforceFile, reassignable so a test can stand
// in for a filesystem that will not store 0600.
var enforceFileMode = filemode.EnforceFile

// errConnectionsUnverified marks a record file whose mode could not be
// verified as 0600. Its records are listed, never used, and never rewritten.
var errConnectionsUnverified = errors.New("forge connection records unusable")

// connectionSpec is what a connection is: how to reach the instance, which
// credential slot is active and which sign-in wrote it.
type connectionSpec struct {
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
	RotationCursor string `json:"rotation_cursor,omitempty"`
	// SignIn is minted at each durable sign-in and names the account every
	// cache entry is read through.
	SignIn string `json:"sign_in"`
	// OwnerScopes are the owners a present cycle reads beside the connection's
	// own pull requests.
	OwnerScopes      []string `json:"owner_scopes,omitempty"`
	Slot             int      `json:"slot"`
	PrivateAddresses bool     `json:"private_addresses,omitempty"`
	PlaintextHTTP    bool     `json:"plaintext_http,omitempty"`
}

// connectionRecord is one connection as the records file holds it.
type connectionRecord struct {
	Standing standing `json:"standing,omitempty"`
	connectionSpec
}

// standing is the persisted half of a status, a closed vocabulary rather than a
// forgeapi code: forgeapi answers a plain 401 with KindUnauthorized and an empty
// code, so a code could not represent the commonest refusal.
type standing string

const (
	standingUsable    standing = ""
	standingRefused   standing = "refused"   // the forge refused the account, not the token: a Check revives it once fixed on the forge
	standingReconnect standing = "reconnect" // the token is dead: only a new sign-in revives it
)

// slotKey is the credential store key of one of a connection's two slots.
func slotKey(id string, slot int) string { return keyenc.Join(id, strconv.Itoa(slot)) }

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
	kept, _, err := foldOwners(kind, login, owners, true)
	return kept, err
}

// ownersKept is owners less every entry ownerScopes would refuse or drop, as
// written, and the refused entries.
func ownersKept(kind Kind, login string, owners []string) (kept, refused []string) {
	kept, refused, _ = foldOwners(kind, login, owners, false)
	return kept, refused
}

// foldOwners is the one owner rule: strict refuses the first invalid owner, or
// a list past the cap, else an invalid owner and every one past the cap is set
// aside.
func foldOwners(kind Kind, login string, owners []string, strict bool) (kept, refused []string, err error) {
	family := kind.family()
	seen := make(map[string]bool, len(owners)+1)
	if family != forgeapi.FamilyGitLab && login != "" {
		seen[strings.ToLower(login)] = true
	}
	kept = make([]string, 0, len(owners))
	over := false
	for i, o := range owners {
		if verr := forgeapi.ValidateOwner(family, o); verr != nil {
			if strict {
				return nil, nil, fmt.Errorf("owners[%d]: %w", i, verr)
			}
			refused = append(refused, o)
			continue
		}
		key := strings.ToLower(o)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(kept) == maxOwnerScopes {
			over = true
			refused = append(refused, o)
			continue
		}
		kept = append(kept, o)
	}
	if strict && over {
		return nil, nil, errTooManyOwners
	}
	return kept, refused, nil
}

// webBase is the instance's web base URL, https on the host when the spec
// names none.
func (s *connectionSpec) webBase() string {
	return cmp.Or(s.WebBaseURL, "https://"+s.Host)
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

type kindMetaEntry struct {
	DefaultHost string
}

// A kind is valid exactly when it has an entry.
var kindMeta = map[Kind]kindMetaEntry{
	KindGitHub:   {DefaultHost: "github.com"},
	KindGitLab:   {DefaultHost: "gitlab.com"},
	KindCodeberg: {DefaultHost: "codeberg.org"},
	KindGitea:    {DefaultHost: ""},
}

// valid reports whether k is a known forge kind.
func (k Kind) valid() bool {
	_, ok := kindMeta[k]
	return ok
}

// defaultHost returns the canonical hostname for the kind, or "" if
// no default exists (self-hosted Gitea/Forgejo).
func (k Kind) defaultHost() string {
	return kindMeta[k].DefaultHost
}

// allKinds returns every supported forge kind. Stable ordering for
// UI rendering.
func allKinds() []Kind {
	return []Kind{KindGitHub, KindGitLab, KindCodeberg, KindGitea}
}

// errNotLoggedIn signals a forge with no usable connection.
var errNotLoggedIn = errors.New("forges: not logged in")

// errNotSupported signals the forge has no mechanism for the requested
// operation: not a failure to reach it, but an absent capability. The HTTP
// layer answers 501 so the client can hide the control instead of offering one
// that always fails.
var errNotSupported = errors.New("forges: operation not supported by this forge")

// codeberg is a Gitea instance.
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

// recordsFile is the records file's path and its one write. Manager.commit is
// the only writer; writeFile is a test seam that fails a write while the file
// stays readable.
type recordsFile struct {
	writeFile func(ctx context.Context, path string, data []byte) error
	path      string
}

func newRecordsFile(configDir string) recordsFile {
	return recordsFile{path: filepath.Join(configDir, connectionsFileName), writeFile: writeRecordsFile}
}

func writeRecordsFile(ctx context.Context, path string, data []byte) error {
	_, err := atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(connectionsFileMode), atomicfile.WithMaxBytes(connectionsMaxBytes))
	return err
}

// loadedRecords is what load found: the admitted records, and whether the file
// was present, at its mode and parsed, the one case a sweep may trust.
type loadedRecords struct {
	recs     []connectionRecord
	verified bool
}

// load reads the file with the server's verdicts. A missing file holds none, and
// so does an unparseable one, which is moved aside first. A file whose mode
// cannot be verified answers what it holds together with an
// errConnectionsUnverified verdict; any other read failure answers the failure
// alone.
func (f recordsFile) load() (loadedRecords, error) {
	// The mode verdict on the name comes before any read: EnforceFile refuses a
	// symlink and cannot block on a FIFO, which a plain read would not.
	_, modeErr := enforceFileMode(f.path, connectionsFileMode)
	if errors.Is(modeErr, os.ErrNotExist) {
		return loadedRecords{}, nil
	}
	var verdict error
	if modeErr != nil {
		verdict = fmt.Errorf("%w: %s must be a regular file at mode %#o, and its mode could not be verified: %v",
			errConnectionsUnverified, f.path, connectionsFileMode, modeErr)
	}
	data, err := f.readBytes()
	if err != nil {
		return loadedRecords{}, cmp.Or(verdict, err)
	}
	var doc connectionsDoc
	if uErr := json.Unmarshal(data, &doc); uErr != nil {
		if verdict != nil {
			return loadedRecords{}, verdict
		}
		return loadedRecords{}, f.moveAside(uErr)
	}
	return loadedRecords{recs: validRecords(doc.Connections), verified: verdict == nil}, verdict
}

// read is the git helper's read: the admitted records with no mode verdict, no
// chmod and no move-aside, since the helper process only reads. A missing file
// holds none.
func (f recordsFile) read() ([]connectionRecord, error) {
	data, err := f.readBytes()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc connectionsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f.path, err)
	}
	kept, _ := admitRecords(doc.Connections)
	return kept, nil
}

func (f recordsFile) readBytes() ([]byte, error) {
	fh, _, err := atomicfile.OpenRegular(f.path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.path, err)
	}
	defer func() { _ = fh.Close() }()
	data, err := atomicfile.ReadBoundedFile(context.Background(), fh, connectionsMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.path, err)
	}
	return data, nil
}

func (f recordsFile) write(ctx context.Context, recs []connectionRecord) error {
	if recs == nil {
		recs = []connectionRecord{}
	}
	data, err := json.MarshalIndent(connectionsDoc{Connections: recs}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", connectionsFileName, err)
	}
	if err := f.writeFile(ctx, f.path, data); err != nil {
		return fmt.Errorf("write %s: %w", connectionsFileName, err)
	}
	return nil
}

// moveAside renames an unparseable record file to a timestamped name, the
// pattern internal/mcp's store uses, so a second corruption cannot destroy the
// first copy. A failed rename is returned: the file must not be overwritten in
// place.
func (f recordsFile) moveAside(cause error) error {
	aside := fmt.Sprintf("%s.corrupt.%s.%d", f.path, time.Now().UTC().Format("20060102-150405"), os.Getpid())
	if err := os.Rename(f.path, aside); err != nil {
		return fmt.Errorf("%s is unparseable (%v) and could not be moved aside: %w", f.path, cause, err)
	}
	slog.Warn("forges: connection record file unparseable, moved aside; its connections must be reconnected",
		"from", f.path, "to", aside, "parse_error", cause)
	return nil
}

// validRecords drops, with a Warn line naming why, every record admitRecords
// refuses.
func validRecords(recs []connectionRecord) []connectionRecord {
	kept, dropped := admitRecords(recs)
	for i := range dropped {
		r := &dropped[i]
		slog.Warn("forges: dropping an invalid connection record",
			"id", logsafe.Field(r.rec.ID), "kind", logsafe.Field(string(r.rec.Kind)), "host", logsafe.Field(r.rec.Host),
			"reason", r.why.Error())
	}
	return kept
}

type droppedRecord struct {
	why error
	rec connectionRecord
}

// admitRecords keeps every record validSpec accepts whose id no earlier kept
// record holds.
func admitRecords(recs []connectionRecord) (kept []connectionRecord, dropped []droppedRecord) {
	kept = make([]connectionRecord, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		err := validSpec(r)
		if err == nil && slices.ContainsFunc(kept, func(o connectionRecord) bool { return o.ID == r.ID }) {
			err = errors.New("the id repeats an earlier record's")
		}
		if err != nil {
			dropped = append(dropped, droppedRecord{why: err, rec: *r})
			continue
		}
		kept = append(kept, *r)
	}
	return kept, dropped
}

// validSpec is the persisted half of the sign-in boundary, sharing its rules, so
// a record is admitted at boot by the rules that admitted it at sign-in. The
// owner list is judged per row by Manager.Load, against the stored account.
func validSpec(r *connectionRecord) error {
	switch {
	case !r.Kind.valid():
		return errors.New("the kind is unknown")
	case !validHost(r.Host):
		return errHostInvalid
	case r.ID != MakeID(r.Kind, r.Host):
		return errors.New("the id is not the one its kind and host make")
	case r.SignIn == "" || len(r.SignIn) > maxSignInBytes:
		return errors.New("the sign-in id is absent or too long")
	case r.Slot != 0 && r.Slot != 1:
		return errors.New("the slot is neither 0 nor 1")
	case r.Standing != standingUsable && r.Standing != standingRefused && r.Standing != standingReconnect:
		return errors.New("the standing is unknown")
	}
	if r.WebBaseURL != "" {
		canon, err := canonicalWebBase(r.WebBaseURL, r.Host)
		if err != nil {
			return err
		}
		if canon != r.WebBaseURL {
			return errors.New("the web base URL is not in its canonical form")
		}
	}
	return nil
}
