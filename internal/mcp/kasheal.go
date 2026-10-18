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

// HealKASConfig re-renders KAS's config file when its `mcpServers` member does not decode to the
// value a save would write (a missing, null or non-object member included), and reports whether
// it wrote. Only that member is compared, by value, so a reindented file is not drift; every
// other key is kept. An unreadable file is left alone and returns ErrKASUnreadable. trigger
// names the caller in the drift WARN. It waits for the write turn only while ctx lasts.
func (s *Store) HealKASConfig(ctx context.Context, trigger string) (bool, error) {
	if err := s.acquireWrite(ctx); err != nil {
		return false, err
	}
	defer s.releaseWrite()
	return s.healKASLocked(ctx, trigger)
}

// healKASLocked is HealKASConfig on a write turn the caller already holds; s.servers is read
// without s.mu because every mutation holds the write turn.
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
	fileServers := doc[kasServerKey]
	if sameJSONValue(fileServers, wanted) {
		return false, nil
	}
	pending := s.isPendingWaitRender(fileServers, policy)
	drift, err := diffKASServers(objectOrEmpty(fileServers), objectOrEmpty(wanted))
	if err != nil {
		return false, err
	}
	delete(doc, kasServerKey)
	if err := s.writeKASConfigWith(ctx, doc, s.servers, policy); err != nil {
		return false, err
	}
	if pending {
		slog.Info("mcp: kas config file carried the new wait setting", "path", s.kasPath, "trigger", trigger)
		return true, nil
	}
	slog.Warn("mcp: kas config file had drifted from the store; re-rendered it",
		"path", s.kasPath, "trigger", trigger, "member", jsonKind(fileServers),
		"missing", driftNames(drift.missing), "unexpected", driftNames(drift.unexpected),
		"changed", driftNames(drift.changed))
	return true, nil
}

// isPendingWaitRender reports whether file is exactly marotte's last render under a wait setting
// that has changed since: the setting reaches the file lazily, so that difference is marotte's
// own pending write, not another writer's drift.
func (s *Store) isPendingWaitRender(file json.RawMessage, policy kasRenderPolicy) bool {
	last := s.renderedState()
	if !last.known || last.waitForReady == policy.waitAll {
		return false
	}
	prev, err := renderServersKey(s.servers, kasRenderPolicy{waitAll: last.waitForReady})
	return err == nil && sameJSONValue(file, prev)
}

// sameJSONValue reports whether two JSON texts decode to the same value, so key order and
// whitespace do not count and a null, an array or an extra field does. A missing (nil) text
// matches nothing.
func sameJSONValue(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// jsonKind names the JSON type of a member for the drift WARN from its first byte, which is
// enough because readKASDoc already parsed it; a member the file lacks (nil) is "absent".
func jsonKind(raw json.RawMessage) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "absent"
}

// kasDrift names the servers whose declaration in the file differs from the render. Names only:
// a declaration carries secrets.
type kasDrift struct {
	missing, unexpected, changed []string
}

// diffKASServers names the servers the drift WARN reports, comparing each by decoded value.
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
