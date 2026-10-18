package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/logsafe"
)

// kasDriftNamesCap bounds each name list in the drift WARN: the names come from a file anyone
// with shell access can write.
const kasDriftNamesCap = 20

// HealKASConfig re-renders KAS's config file when its `mcpServers` member says something other
// than the store renders, and reports whether it wrote. Only that member is compared, by value,
// so a reindented file is not drift; every other key is kept. An unreadable file is left alone
// and returns ErrKASUnreadable. trigger names the caller in the drift WARN. It waits for the
// write turn only while ctx lasts.
func (s *Store) HealKASConfig(ctx context.Context, trigger string) (bool, error) {
	if err := s.acquireWrite(ctx); err != nil {
		return false, err
	}
	defer s.releaseWrite()
	return s.healKASLocked(ctx, trigger)
}

// healKASLocked is HealKASConfig on a write turn the caller already holds; s.mu must not be held,
// since renderPolicy's resolver runs here.
func (s *Store) healKASLocked(ctx context.Context, trigger string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	doc, _, err := s.readKASDoc()
	if err != nil {
		return false, err
	}
	policy, _ := s.renderPolicy(ctx)
	wanted, err := renderServersKey(s.servers, policy)
	if err != nil {
		return false, err
	}
	drift, err := diffKASServers(objectOrEmpty(doc[kasServerKey]), objectOrEmpty(wanted))
	if err != nil {
		return false, err
	}
	if drift.empty() {
		return false, nil
	}
	delete(doc, kasServerKey)
	if err := s.writeKASConfigWith(ctx, doc, s.servers, policy); err != nil {
		return false, err
	}
	slog.Warn("mcp: kas config file had drifted from the store; re-rendered it",
		"path", s.kasPath, "trigger", trigger,
		"missing", driftNames(drift.missing), "unexpected", driftNames(drift.unexpected),
		"changed", driftNames(drift.changed))
	return true, nil
}

// kasDrift names the servers whose declaration in the file differs from the render. Names only:
// a declaration carries secrets.
type kasDrift struct {
	missing, unexpected, changed []string
}

func (d kasDrift) empty() bool {
	return len(d.missing) == 0 && len(d.unexpected) == 0 && len(d.changed) == 0
}

// diffKASServers compares the file's servers with the wanted ones by decoded value, so key order
// and whitespace are not drift and a field marotte never renders is.
func diffKASServers(file, wanted map[string]json.RawMessage) (kasDrift, error) {
	var d kasDrift
	for name, w := range wanted {
		f, ok := file[name]
		if !ok {
			d.missing = append(d.missing, name)
			continue
		}
		var wv, fv any
		if err := json.Unmarshal(w, &wv); err != nil {
			return kasDrift{}, fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
		}
		if json.Unmarshal(f, &fv) != nil || !reflect.DeepEqual(wv, fv) {
			d.changed = append(d.changed, name)
		}
	}
	for name := range file {
		if _, ok := wanted[name]; !ok {
			d.unexpected = append(d.unexpected, name)
		}
	}
	return d, nil
}

func driftNames(names []string) string {
	slices.Sort(names)
	if len(names) > kasDriftNamesCap {
		names = append(names[:kasDriftNamesCap:kasDriftNamesCap], "…")
	}
	return logsafe.Field(strings.Join(names, ","))
}
