// Package powers is marotte's half of Kiro Powers: the official catalogue, the
// `kiro-cli powers` install verbs, and the legacy `powers.mcpServers` block KAS
// reads for an installed Power's MCP servers. KAS owns the installed list
// itself (`_kiro/powers/list`); this package never re-derives it.
package powers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/runesafe/v2"
)

// RegistryURL is the catalogue the Kiro IDE and `kiro-cli powers install` read.
const RegistryURL = "https://prod.download.desktop.kiro.dev/powers/default_registry.json"

const (
	// maxRegistryBytes bounds the catalogue read; it was 155 KB at 149 entries.
	maxRegistryBytes = 4 << 20
	// maxServersBytes bounds one Power's mcp.json read from GitHub.
	maxServersBytes = 256 << 10
	// catalogTTL is how long a fetched catalogue is served before a refetch. A
	// failed refetch keeps serving the last good copy.
	catalogTTL = time.Hour
	// serversTTL bounds how stale an install confirmation's server list may be:
	// `kiro-cli powers install` fetches the Power as it is now.
	serversTTL = time.Minute
	// failureBackoff is how long a failed fetch is answered from the cache (or its
	// error) before the registry is asked again.
	failureBackoff = time.Minute
	fetchTimeout   = 15 * time.Second
	// maxFieldBytes bounds one display string; the longest description measured
	// was 515 bytes.
	maxFieldBytes = 1024
	// maxServerNames and maxServerNameBytes bound the install confirmation's server
	// list; a Power declaring more is reported as unknown rather than listed in part.
	maxServerNames     = 32
	maxServerNameBytes = 128
)

// nameRe is KAS's own Power name rule: lowercase, digits, dot and dash, no
// leading or trailing separator; ValidName adds the length, "--" and ".." checks.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// ValidName reports whether name is a Power name KAS accepts, which is also what
// keeps it safe as a `kiro-cli` argument and as a directory name.
func ValidName(name string) bool {
	return len(name) <= 64 && nameRe.MatchString(name) &&
		!strings.Contains(name, "--") && !strings.Contains(name, "..")
}

// Entry is one catalogue row, its strings sanitized for display.
type Entry struct {
	Name          string
	DisplayName   string
	Author        string
	Description   string
	Category      string
	PublisherTier string
	AuthType      string
	// rawBase is the GitHub raw URL of the Power's directory, empty when the
	// repository URL is not a github.com tree the server can resolve.
	rawBase string
}

type wireEntry struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName"`
	Author           string `json:"author"`
	Description      string `json:"description"`
	Category         string `json:"category"`
	PublisherTier    string `json:"publisherTier"`
	AuthType         string `json:"authType"`
	RepositoryURL    string `json:"repositoryUrl"`
	RepositoryBranch string `json:"repositoryBranch"`
	PathInRepo       string `json:"pathInRepo"`
}

// Catalog fetches and caches the official catalogue.
type Catalog struct {
	fetchedAt time.Time
	failedAt  time.Time
	failErr   error
	client    *http.Client
	servers   map[string]serverList
	url       string
	entries   []Entry
	// mu guards the fields and is never held across a fetch; fetchMu admits one
	// fetch at a time, and refreshing marks a stale copy's fetch in flight.
	mu         sync.Mutex
	fetchMu    sync.Mutex
	refreshing bool
}

type serverList struct {
	at    time.Time
	base  string
	names []string
}

// NewCatalog reads registryURL (RegistryURL in production) through client.
func NewCatalog(client *http.Client, registryURL string) *Catalog {
	return &Catalog{client: client, url: registryURL, servers: map[string]serverList{}}
}

// Entries returns the catalogue, fetching it when the cached copy is older
// than an hour. A stale copy is served at once while another caller refreshes it
// and for failureBackoff after a failed fetch, so an outage costs one fetch per
// backoff rather than one per caller.
func (c *Catalog) Entries(ctx context.Context) ([]Entry, error) {
	c.mu.Lock()
	if entries, done, err := c.cachedLocked(); done {
		c.mu.Unlock()
		return entries, err
	}
	stale := c.entries != nil
	if stale {
		c.refreshing = true
	}
	c.mu.Unlock()

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	if !stale {
		// A cold caller queued behind another cold fetch takes its outcome.
		c.mu.Lock()
		entries, done, err := c.cachedLocked()
		c.mu.Unlock()
		if done {
			return entries, err
		}
	}
	entries, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	if err != nil {
		// A caller that walked away is no evidence the registry is down.
		if ctx.Err() == nil {
			c.failedAt, c.failErr = time.Now(), err
		}
		if c.entries != nil {
			return c.entries, nil
		}
		return nil, err
	}
	c.entries, c.fetchedAt, c.failedAt, c.failErr = entries, time.Now(), time.Time{}, nil
	return entries, nil
}

// cachedLocked answers from the cache when no fetch is due: a fresh copy, a stale
// copy someone is refreshing or that failed within failureBackoff, or a cold
// catalogue's recent failure.
func (c *Catalog) cachedLocked() (entries []Entry, done bool, err error) {
	recentFailure := !c.failedAt.IsZero() && time.Since(c.failedAt) < failureBackoff
	if c.entries != nil {
		if time.Since(c.fetchedAt) < catalogTTL || c.refreshing || recentFailure {
			return c.entries, true, nil
		}
		return nil, false, nil
	}
	if recentFailure {
		return nil, true, c.failErr
	}
	return nil, false, nil
}

// Lookup returns the catalogue row named name.
func (c *Catalog) Lookup(ctx context.Context, name string) (Entry, bool, error) {
	entries, err := c.Entries(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	return find(entries, name)
}

// Revalidate returns the row named name from a catalogue fetched after the call
// began, because `kiro-cli powers install` reads the registry as it is now.
func (c *Catalog) Revalidate(ctx context.Context, name string) (Entry, bool, error) {
	return c.lookupSince(ctx, name, time.Now())
}

// Admit returns the row named name from a catalogue fetched within serversTTL, so
// an install right after its confirmation installs the entry that confirmation named.
func (c *Catalog) Admit(ctx context.Context, name string) (Entry, bool, error) {
	return c.lookupSince(ctx, name, time.Now().Add(-serversTTL))
}

func (c *Catalog) lookupSince(ctx context.Context, name string, since time.Time) (Entry, bool, error) {
	entries, err := c.entriesSince(ctx, since)
	if err != nil {
		return Entry{}, false, err
	}
	return find(entries, name)
}

// entriesSince fetches unless the cached copy was fetched at or after since; a
// failed fetch is an error, never the older copy.
func (c *Catalog) entriesSince(ctx context.Context, since time.Time) ([]Entry, error) {
	fresh := func() ([]Entry, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries != nil && !c.fetchedAt.Before(since) {
			return c.entries, true
		}
		return nil, false
	}
	if entries, ok := fresh(); ok {
		return entries, nil
	}
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	if entries, ok := fresh(); ok {
		return entries, nil
	}
	entries, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			c.failedAt, c.failErr = time.Now(), err
		}
		return nil, err
	}
	c.entries, c.fetchedAt, c.failedAt, c.failErr = entries, time.Now(), time.Time{}, nil
	return entries, nil
}

func find(entries []Entry, name string) (Entry, bool, error) {
	for i := range entries {
		if entries[i].Name == name {
			return entries[i], true, nil
		}
	}
	return Entry{}, false, nil
}

func (c *Catalog) fetch(ctx context.Context) ([]Entry, error) {
	body, err := c.get(ctx, c.url, maxRegistryBytes)
	if err != nil {
		return nil, fmt.Errorf("powers catalogue: %w", err)
	}
	var doc struct {
		Powers []wireEntry `json:"powers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("powers catalogue: decode: %w", err)
	}
	out := make([]Entry, 0, len(doc.Powers))
	for i := range doc.Powers {
		w := &doc.Powers[i]
		if !ValidName(w.Name) {
			continue
		}
		out = append(out, Entry{
			Name:          w.Name,
			DisplayName:   field(w.DisplayName),
			Author:        field(w.Author),
			Description:   field(w.Description),
			Category:      field(w.Category),
			PublisherTier: field(w.PublisherTier),
			AuthType:      field(w.AuthType),
			rawBase:       rawBase(w.RepositoryURL, w.RepositoryBranch, w.PathInRepo),
		})
	}
	return out, nil
}

// Servers names the MCP servers a catalogue Power declares in its mcp.json,
// cached per Power for serversTTL. known is false when the Power's files could not
// be read or declare servers in a shape this cannot list (an agent plugin's manifest).
func (c *Catalog) Servers(ctx context.Context, e *Entry) (names []string, known bool) {
	c.mu.Lock()
	cached, ok := c.servers[e.Name]
	c.mu.Unlock()
	if ok && cached.base == e.rawBase && time.Since(cached.at) < serversTTL {
		return cached.names, true
	}
	if e.rawBase == "" {
		return nil, false
	}
	body, err := c.get(ctx, e.rawBase+"/mcp.json", maxServersBytes)
	if err != nil {
		return nil, false
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	names, ok = serverNames(doc.MCPServers)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	c.servers[e.Name] = serverList{at: time.Now(), base: e.rawBase, names: names}
	c.mu.Unlock()
	return names, true
}

func (c *Catalog) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response over %d bytes", limit)
	}
	return body, nil
}

// rawBase maps `https://github.com/<owner>/<repo>/...` plus the branch and the
// path in the repository to the raw.githubusercontent.com directory URL. Every
// segment is path-escaped, so a catalogue value cannot reach another host.
func rawBase(repoURL, branch, pathInRepo string) string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || branch == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	segs := []string{parts[0], parts[1], branch}
	for p := range strings.SplitSeq(strings.Trim(pathInRepo, "/"), "/") {
		if p == "." || p == ".." {
			return ""
		}
		if p != "" {
			segs = append(segs, p)
		}
	}
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "https://raw.githubusercontent.com/" + strings.Join(segs, "/")
}

// serverNames sanitizes the declared names for a one-line confirmation; false when
// there are more than the confirmation lists.
func serverNames(m map[string]json.RawMessage) ([]string, bool) {
	if len(m) > maxServerNames {
		return nil, false
	}
	out := make([]string, 0, len(m))
	for _, raw := range sortedNames(m) {
		if name := strings.TrimSpace(runesafe.SanitizeSingleLineBounded(raw, maxServerNameBytes)); name != "" {
			out = append(out, name)
		}
	}
	return slices.Compact(out), true
}

func field(s string) string {
	return strings.TrimSpace(runesafe.SanitizeSingleLineBounded(s, maxFieldBytes))
}
