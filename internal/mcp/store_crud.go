package mcp

// CRUD operations on the MCP server store.
//
// These methods change when the API surface evolves (new fields, new
// operations). Extracted from store.go to keep that file focused on
// construction, persistence, and internal helpers.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/mcp/prewarm"
)

// list returns a deep copy of every server with secrets masked. Safe to
// serve directly over the wire.
func (s *Store) list(ctx context.Context) []*Server {
	if ctx.Err() != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Server, 0, len(s.servers))
	for _, sv := range s.servers {
		out = append(out, maskedCopy(sv))
	}
	return out
}

// enabledRaw returns a deep copy of every enabled server with secrets
// intact, the input EnabledServers projects for prewarm. Not exposed
// over HTTP.
func (s *Store) enabledRaw(ctx context.Context) []*Server {
	if ctx.Err() != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Server, 0, len(s.servers))
	for _, sv := range s.servers {
		if !sv.Enabled {
			continue
		}
		out = append(out, rawCopy(sv))
	}
	return out
}

// get returns a masked copy of one server, or nil.
func (s *Store) get(_ context.Context, id serverID) *Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sv := range s.servers {
		if sv.ID == id {
			return maskedCopy(sv)
		}
	}
	return nil
}

// Create inserts a new server, assigning a fresh ID and timestamps.
// Validates shape before persisting. Returns a masked copy of the
// stored record.
func (s *Store) Create(ctx context.Context, in *Server) (*Server, error) {
	now := time.Now().UnixMilli()
	rec := &Server{
		ID:                     newID(),
		Transport:              in.Transport,
		Name:                   strings.TrimSpace(in.Name),
		Command:                in.Command,
		Args:                   append([]string(nil), in.Args...),
		Env:                    copyPairs(in.Env),
		URL:                    in.URL,
		Headers:                copyPairs(in.Headers),
		DisabledTools:          append([]string(nil), in.DisabledTools...),
		OAuthClientID:          strings.TrimSpace(in.OAuthClientID),
		OAuthClientMetadataURL: strings.TrimSpace(in.OAuthClientMetadataURL),
		OAuthRedirectURI:       strings.TrimSpace(in.OAuthRedirectURI),
		OAuthScopes:            trimmedScopes(in.OAuthScopes),
		OAuthMode:              in.OAuthMode,
		Prewarm:                in.Prewarm,
		Enabled:                in.Enabled,
		WaitForReady:           in.WaitForReady,
		TimeoutMS:              in.TimeoutMS,
		VersionNegotiation:     in.VersionNegotiation,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := validate(rec); err != nil {
		return nil, err
	}

	if err := s.acquireWrite(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	// A reinstall of an identical spec is a no-op, not a conflict. The 409
	// it replaces had exactly one workaround — delete, then re-add —
	// which threw away every API key the user had typed in. Returning
	// the existing record instead preserves them.
	if existing := s.findByNameLocked(rec.Name); existing != nil {
		if !sameSpec(existing, rec) {
			s.mu.Unlock()
			return nil, errNameConflict
		}
		out := maskedCopy(existing)
		s.mu.Unlock()
		slog.Info("mcp: server already configured with this spec; keeping the stored one",
			"id", existing.ID, "name", existing.Name)
		// A reinstall is how a user repairs a server missing from KAS's file.
		if _, err := s.healKASLocked(ctx, "reinstall"); err != nil {
			return nil, err
		}
		return out, nil
	}
	if err := s.heldNameErr(rec.Name); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.servers = append(s.servers, rec)
	if err := s.persist(ctx); err != nil {
		// Roll back the in-memory append so memory matches disk.
		s.servers = s.servers[:len(s.servers)-1]
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: server created", "id", rec.ID, "name", rec.Name,
		"transport", rec.Transport, "enabled", rec.Enabled)
	s.notifyChange()
	return maskedCopy(rec), nil
}

// trimmedScopes copies scopes with each entry trimmed; an empty list is absent.
func trimmedScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]string, len(scopes))
	for i, sc := range scopes {
		out[i] = strings.TrimSpace(sc)
	}
	return out
}

// importOutcome names what one entry of a pasted block did.
type importOutcome string

// importCreated and ImportUnchanged are the two outcomes of one import entry.
// There is no "updated": an entry naming a configured server either matches its
// spec (unchanged) or conflicts with it (the whole paste fails), because
// silently rewriting a server the user has since edited is not what pasting a
// README asked for.
const (
	importCreated   importOutcome = "created"
	importUnchanged importOutcome = "unchanged"
)

// importResult is one entry's outcome, in the order the block declared it.
type importResult struct {
	Name    string        `json:"name"`
	Outcome importOutcome `json:"outcome"`
}

// importServers creates every server of one pasted block, or none, and saves the block's identity provider (nil when
// it carries none) in the same write, so a paste lands whole or not at all: the block is one README's worth. A name
// configured with the SAME spec is `unchanged`; with a DIFFERENT spec the paste fails rather than silently
// overwriting.
func (s *Store) importServers(ctx context.Context, in []*Server, idp *identityProvider) ([]importResult, error) {
	// Before taking the lock: the checks are pure, and a block that cannot land
	// should not have made every other caller wait.
	if err := validateImport(in); err != nil {
		return nil, err
	}
	var nextIDP *identityProvider
	if idp != nil {
		var err error
		if nextIDP, err = normalizedIdentityProvider(idp); err != nil {
			return nil, err
		}
	}

	if err := s.acquireWrite(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	before, prevIDP, prevIDPRaw := s.servers, s.idp, s.idpUnreadable
	results, created, err := s.importAllLocked(in)
	if err != nil {
		s.servers = before
		s.mu.Unlock()
		return nil, err
	}
	idpChanged := nextIDP != nil && !s.sameIdentityProviderLocked(nextIDP)
	if created == 0 && !idpChanged {
		s.mu.Unlock()
		slog.Info("mcp: import matched the stored set; record unchanged", "servers", len(in))
		if _, err := s.healKASLocked(ctx, "import"); err != nil {
			return nil, err
		}
		return results, nil
	}
	if idpChanged {
		s.idp, s.idpUnreadable = nextIDP, nil
	}
	if err := s.persist(ctx); err != nil {
		s.servers, s.idp, s.idpUnreadable = before, prevIDP, prevIDPRaw
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: servers imported", "created", created, "entries", len(in))
	if idpChanged {
		slog.Info("mcp: organization identity provider saved", "issuer", logsafe.Field(nextIDP.Issuer))
	}
	if created > 0 {
		s.notifyChange()
	}
	return results, nil
}

// importAllLocked appends every entry of one block and counts the created ones; on an error the caller restores
// s.servers. Caller must hold the write lock.
func (s *Store) importAllLocked(in []*Server) ([]importResult, int, error) {
	now := time.Now().UnixMilli()
	results := make([]importResult, 0, len(in))
	created := 0
	for _, sv := range in {
		res, err := s.importOneLocked(sv, now)
		if err != nil {
			return nil, 0, err
		}
		if res.Outcome == importCreated {
			created++
		}
		results = append(results, res)
	}
	return results, created, nil
}

// validateImport validates and de-duplicates one pasted block.
func validateImport(in []*Server) error {
	if len(in) == 0 {
		return errors.New("no servers to connect")
	}
	if len(in) > maxImportServers {
		return fmt.Errorf("too many servers in one paste (%d, max %d)", len(in), maxImportServers)
	}
	seen := make(map[string]struct{}, len(in))
	for _, sv := range in {
		if err := validate(sv); err != nil {
			return fmt.Errorf("server %q: %w", sv.Name, err)
		}
		key := nameKey(sv.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%q %w", sv.Name, errImportDuplicate)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// importOneLocked appends one entry, or reports that the stored record already
// says the same thing. Caller must hold the write lock.
func (s *Store) importOneLocked(sv *Server, now int64) (importResult, error) {
	if existing := s.findByNameLocked(sv.Name); existing != nil {
		if !sameSpec(existing, sv) {
			return importResult{}, fmt.Errorf(
				"%w. %q is configured with a different command or url. Rename the entry or edit the existing integration",
				errNameConflict, existing.Name,
			)
		}
		return importResult{Name: existing.Name, Outcome: importUnchanged}, nil
	}
	if err := s.heldNameErr(sv.Name); err != nil {
		return importResult{}, err
	}
	rec := *sv
	rec.ID = newID()
	rec.Name = strings.TrimSpace(sv.Name)
	rec.Args = append([]string(nil), sv.Args...)
	rec.Env = copyPairs(sv.Env)
	rec.Headers = copyPairs(sv.Headers)
	rec.DisabledTools = append([]string(nil), sv.DisabledTools...)
	rec.OAuthScopes = slices.Clone(sv.OAuthScopes)
	rec.CreatedAt = now
	rec.UpdatedAt = now
	s.servers = append(s.servers, &rec)
	return importResult{Name: rec.Name, Outcome: importCreated}, nil
}

// update replaces one server by id. Fields whose secret value equals
// secretMask are preserved from the existing record, so a client can
// edit non-secret fields without re-submitting the secret. Returns a
// masked copy of the stored record.
func (s *Store) update(ctx context.Context, id serverID, in *Server) (*Server, error) {
	if err := s.acquireWrite(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, errNotFound
	}
	existing := s.servers[idx]
	// Before mergeSecrets, not after: it substitutes a stored value for every
	// masked row it can match, and once that has happened the record is
	// indistinguishable from one the user retyped. See guardOriginChange.
	if err := guardOriginChange(in, existing); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	rec := &Server{
		ID:        existing.ID,
		Transport: in.Transport,
		Name:      strings.TrimSpace(in.Name),
		Command:   in.Command,
		Args:      append([]string(nil), in.Args...),
		Env:       mergeSecrets(in.Env, existing.Env),
		URL:       in.URL,
		Headers:   mergeSecrets(in.Headers, existing.Headers),
		// An omitted list means "unchanged", not "empty": the edit modal's PUT
		// leaves disabled_tools out.
		DisabledTools:          preserveNilSlice(in.DisabledTools, existing.DisabledTools),
		OAuthClientID:          strings.TrimSpace(in.OAuthClientID),
		OAuthClientMetadataURL: strings.TrimSpace(in.OAuthClientMetadataURL),
		OAuthRedirectURI:       strings.TrimSpace(in.OAuthRedirectURI),
		OAuthScopes:            trimmedScopes(in.OAuthScopes),
		OAuthMode:              in.OAuthMode,
		Prewarm:                in.Prewarm,
		Enabled:                in.Enabled,
		WaitForReady:           in.WaitForReady,
		TimeoutMS:              in.TimeoutMS,
		VersionNegotiation:     in.VersionNegotiation,
		CreatedAt:              existing.CreatedAt,
		UpdatedAt:              time.Now().UnixMilli(),
	}
	// validate runs under s.mu because rec.Env and rec.Headers were just
	// resolved against `existing` via mergeSecrets, which only makes
	// sense with the current stored set. Cheap regex + length checks; no I/O.
	if err := validate(rec); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.hasNameLocked(rec.Name, id) {
		s.mu.Unlock()
		return nil, errNameConflict
	}
	if err := s.heldNameErr(rec.Name); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.servers[idx] = rec
	if err := s.persist(ctx); err != nil {
		s.servers[idx] = existing
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: server updated", "id", rec.ID, "name", rec.Name,
		"transport", rec.Transport, "enabled", rec.Enabled)
	s.notifyChange()
	return maskedCopy(rec), nil
}

// setEnabled flips the enabled flag for one server. Returns the updated
// masked copy. An unchanged flag writes no record but heals KAS's file.
func (s *Store) setEnabled(ctx context.Context, id serverID, enabled bool) (*Server, error) {
	if err := s.acquireWrite(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, errNotFound
	}
	if s.servers[idx].Enabled == enabled {
		out := maskedCopy(s.servers[idx])
		s.mu.Unlock()
		// The record is unchanged, but the toggle doubles as the UI's repair of a drifted KAS file.
		if _, err := s.healKASLocked(ctx, "toggle"); err != nil {
			return nil, err
		}
		return out, nil
	}
	// Snapshot the struct by value so rollback restores the pre-mutation
	// scalar fields. The Args/Env/Headers slices aren't mutated here, so
	// sharing the backing arrays is safe; a future mutation rewriting
	// those slices must deep-copy first.
	before := *s.servers[idx]
	s.servers[idx].Enabled = enabled
	s.servers[idx].UpdatedAt = time.Now().UnixMilli()
	if err := s.persist(ctx); err != nil {
		s.servers[idx] = &before
		s.mu.Unlock()
		return nil, err
	}
	out := maskedCopy(s.servers[idx])
	s.mu.Unlock()
	slog.Info("mcp: server enabled toggled", "id", logsafe.Field(string(id)), "enabled", enabled)
	s.notifyChange()
	return out, nil
}

// delete removes a server by id. No-op if not found.
func (s *Store) delete(ctx context.Context, id serverID) error {
	if err := s.acquireWrite(ctx); err != nil {
		return err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil
	}
	removed := s.servers[idx]
	s.servers = slices.Delete(s.servers, idx, idx+1)
	if err := s.persist(ctx); err != nil {
		// Roll back.
		s.servers = slices.Insert(s.servers, idx, removed)
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	// Log the STORED record's id, not the request parameter: identical
	// bytes, but the stored value is marotte-generated, which breaks the
	// user-input taint chain a raw path segment would carry into the log.
	slog.Info("mcp: server deleted", "id", removed.ID)
	s.notifyChange()
	return nil
}

// savedIdentityProvider is a copy of the organization identity provider; false when none is set.
func (s *Store) savedIdentityProvider() (identityProvider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.idp == nil {
		return identityProvider{}, false
	}
	return *s.idp, true
}

// setIdentityProvider saves the organization identity provider, or clears it when in is nil, and renders it into
// KAS's file, which KAS hot-reloads. An unchanged provider writes no record but heals KAS's file.
func (s *Store) setIdentityProvider(ctx context.Context, in *identityProvider) error {
	var next *identityProvider
	if in != nil {
		var err error
		if next, err = normalizedIdentityProvider(in); err != nil {
			return err
		}
	}
	if err := s.acquireWrite(ctx); err != nil {
		return err
	}
	defer s.releaseWrite()
	s.mu.Lock()
	if s.sameIdentityProviderLocked(next) {
		s.mu.Unlock()
		_, err := s.healKASLocked(ctx, "identity provider")
		return err
	}
	prev, prevRaw := s.idp, s.idpUnreadable
	s.idp, s.idpUnreadable = next, nil
	if err := s.persist(ctx); err != nil {
		s.idp, s.idpUnreadable = prev, prevRaw
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if next == nil {
		slog.Info("mcp: organization identity provider cleared")
		return nil
	}
	slog.Info("mcp: organization identity provider saved", "issuer", logsafe.Field(next.Issuer))
	return nil
}

// sameIdentityProviderLocked reports whether next is the provider the record already holds, nil meaning none.
func (s *Store) sameIdentityProviderLocked(next *identityProvider) bool {
	if s.idpUnreadable != nil {
		return false
	}
	if next == nil || s.idp == nil {
		return next == nil && s.idp == nil
	}
	return *next == *s.idp
}

// normalizedIdentityProvider is in with every field trimmed, or the save rule's refusal.
func normalizedIdentityProvider(in *identityProvider) (*identityProvider, error) {
	out := &identityProvider{
		Issuer:                strings.TrimSpace(in.Issuer),
		ClientID:              strings.TrimSpace(in.ClientID),
		Name:                  strings.TrimSpace(in.Name),
		AuthorizationEndpoint: strings.TrimSpace(in.AuthorizationEndpoint),
		TokenEndpoint:         strings.TrimSpace(in.TokenEndpoint),
	}
	if err := validateIdentityProvider(out); err != nil {
		return nil, err
	}
	return out, nil
}

// There is no SetKnownTools. A connected server's tool names arrive on the
// same _kiro/mcp/status notification as its prompts and resources, and
// they are RUNTIME state, not what the user configured. They live in
// the runtime's registry now and reach the UI through /api/mcp/status.

// EnabledServers returns the prewarm-relevant view of enabled servers.
// Satisfies prewarm.ServerLister so the Store can be passed directly
// to prewarm.NewRunner without an adapter.
func (s *Store) EnabledServers(ctx context.Context) []prewarm.ServerInfo {
	servers := s.enabledRaw(ctx)
	out := make([]prewarm.ServerInfo, len(servers))
	for i, srv := range servers {
		out[i] = prewarm.ServerInfo{
			ID:        string(srv.ID),
			Prewarm:   srv.Prewarm,
			Enabled:   srv.Enabled,
			Transport: string(srv.Transport),
			Command:   srv.Command,
			Args:      srv.Args,
		}
	}
	return out
}
