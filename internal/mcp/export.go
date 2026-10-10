package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ACP export + secret masking helpers. Kept in a leaf file so store.go
// stays focused on persistence and life-cycle.

// When maskSecrets is true, every env/header value is replaced by secretMask (safe to send to the
// browser). When false, values are preserved (for kiro-cli / pre-warm).
func copyServer(s *Server, maskSecrets bool) *Server {
	if s == nil {
		return nil
	}
	c := *s
	c.Args = append([]string(nil), s.Args...)
	c.DisabledTools = append([]string(nil), s.DisabledTools...)
	if maskSecrets {
		c.Env = make([]keyPair, len(s.Env))
		for i, kv := range s.Env {
			c.Env[i] = keyPair{Name: kv.Name, Value: secretMask}
		}
		c.Headers = make([]keyPair, len(s.Headers))
		for i, kv := range s.Headers {
			c.Headers[i] = keyPair{Name: kv.Name, Value: secretMask}
		}
	} else {
		c.Env = copyPairs(s.Env)
		c.Headers = copyPairs(s.Headers)
	}
	return &c
}

// maskedCopy returns a deep copy of s with every env/header value
// replaced by secretMask. Safe to send to the browser.
func maskedCopy(s *Server) *Server { return copyServer(s, true) }

func rawCopy(s *Server) *Server { return copyServer(s, false) }

func copyPairs(in []keyPair) []keyPair {
	if in == nil {
		return nil
	}
	out := make([]keyPair, len(in))
	copy(out, in)
	return out
}

// preserveNilSlice keeps the existing value when the update omitted the field
// entirely (nil patch) and otherwise takes the patch, so an explicit empty
// slice is a CLEAR. Both tool lists need that distinction; Store.update states
// what a dropped disabled_tools would cost.
func preserveNilSlice(patch, existing []string) []string {
	if patch == nil {
		return append([]string(nil), existing...)
	}
	return append([]string(nil), patch...)
}

// mergeSecrets returns a new slice that mirrors `patch` in order and key-set, but substitutes the
// previously-stored value wherever the client sent secretMask. Preserves the user's intended
// ordering while keeping secrets round-trip safe.
func mergeSecrets(patch, existing []keyPair) []keyPair {
	out := make([]keyPair, len(patch))
	index := make(map[string]string, len(existing))
	for _, kv := range existing {
		index[kv.Name] = kv.Value
	}
	for i, kv := range patch {
		out[i] = kv
		if kv.Value == secretMask {
			if prev, ok := index[kv.Name]; ok {
				out[i].Value = prev
			} else {
				out[i].Value = ""
			}
		}
	}
	return out
}

// sameSpec reports whether two records describe the same CONNECTION, so a
// re-paste of a configured server is a no-op rather than a conflict. It ignores
// store-owned fields, secret VALUES (a pasted README carries a placeholder) and
// the user's own policy (Enabled, Prewarm, DisabledTools). Env and header NAMES
// compare as sets because they are records on KAS's wire; args stay ordered.
func sameSpec(a, b *Server) bool {
	if a.Transport != b.Transport ||
		strings.TrimSpace(a.Command) != strings.TrimSpace(b.Command) ||
		strings.TrimSpace(a.URL) != strings.TrimSpace(b.URL) ||
		a.OAuthClientID != b.OAuthClientID ||
		a.OAuthClientMetadataURL != b.OAuthClientMetadataURL ||
		a.OAuthRedirectURI != b.OAuthRedirectURI {
		return false
	}
	if !slices.Equal(a.Args, b.Args) {
		return false
	}
	if !slices.Equal(sortedPairNames(a.Env, false), sortedPairNames(b.Env, false)) {
		return false
	}
	return slices.Equal(sortedPairNames(a.Headers, true), sortedPairNames(b.Headers, true))
}

// sortedPairNames returns the pair names in sorted order, lowercased when the
// field dedupes case-insensitively (headers do, env does not — the same split
// validateKeyPairs makes).
func sortedPairNames(pairs []keyPair, fold bool) []string {
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		if fold {
			out = append(out, strings.ToLower(kv.Name))
			continue
		}
		out = append(out, kv.Name)
	}
	slices.Sort(out)
	return out
}

// guardOriginChange refuses to re-attach a preserved secret to a new origin: mergeSecrets keys
// masked headers by NAME, so a PUT changing `url` would hand the old origin's bearer to the new
// one. It refuses rather than silently dropping the value; scheme+host is the comparison, so a
// same-origin path edit passes.
func guardOriginChange(in, existing *Server) error {
	if !changesOrigin(existing.URL, in.URL) {
		return nil
	}
	for _, kv := range in.Headers {
		if kv.Value != secretMask {
			continue
		}
		// Exact-name lookup mirrors mergeSecrets: a name it would not match
		// preserves nothing, so there is nothing to refuse.
		if idx := slices.IndexFunc(existing.Headers, func(p keyPair) bool {
			return p.Name == kv.Name && p.Value != ""
		}); idx >= 0 {
			return fmt.Errorf(
				"url points at a new origin, so the stored %q header was not carried over: re-enter its value for %s",
				kv.Name, originLabel(in.URL),
			)
		}
	}
	return nil
}

// An unparseable or empty value counts as different: the conservative answer is the one that
// refuses to hand a credential over.
func changesOrigin(prev, next string) bool {
	return originLabel(prev) != originLabel(next)
}

// originLabel renders a URL's scheme+host lowercased, or the whole trimmed
// string when it does not parse (so two identical unparseable values still
// compare equal and an edit elsewhere in the record is not blocked).
func originLabel(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(trimmed)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// errImportDuplicate names two entries of one paste that would become the same
// server. A JSON object cannot hold a duplicate key, but two keys differing only
// in case survive decoding and collide on the store's case-insensitive name rule.
var errImportDuplicate = errors.New("names the same server twice")

// There is no ACP wire builder here any more, and no ACPServers.
//
// marotte sent its server set INLINE on session/new and session/load. It
// now renders KAS's own config file instead (kasfile.go) and sends
// nothing — KAS merges `client > file-based`, so an inline entry would
// win over the file and make every file edit look like a no-op.

// EnabledNames returns the set of enabled server names.
func (s *Store) EnabledNames(_ context.Context) map[string]struct{} {
	return s.namesWhere(func(sv *Server) bool { return sv.Enabled })
}

// ConfiguredNames returns every server name this store holds regardless of
// its enabled flag: the set of definitions marotte owns.
func (s *Store) ConfiguredNames(_ context.Context) map[string]struct{} {
	return s.namesWhere(func(*Server) bool { return true })
}

// namesWhere collects the names of the stored servers matching keep. One helper
// for the two name sets so they cannot drift in how they read the store.
func (s *Store) namesWhere(keep func(*Server) bool) map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.servers))
	for _, sv := range s.servers {
		if keep(sv) {
			out[sv.Name] = struct{}{}
		}
	}
	return out
}
