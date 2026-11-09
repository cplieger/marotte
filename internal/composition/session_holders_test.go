package composition

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

func TestSessionHolders_SlotsEachHolder(t *testing.T) {
	b := bootSeam(t, seamRoot(t), newSeamEngine(t), captureSeamLogs(t), seamOpts{noReconcile: true})

	h := sessionHolders(b.chats, b.h)

	if len(h.Records) != 1 || h.Records[0] != kirosession.Holder(b.chats) {
		t.Errorf("Records = %v, want exactly the chat store: only chat records may lift the empty-keep-list refusal", h.Records)
	}
	if len(h.Live) != 1 || h.Live[0] != kirosession.Holder(b.h) {
		t.Errorf("Live = %v, want exactly the runtime: a resident session must be kept", h.Live)
	}
}

// sessionFields reports, by path from typ's name, every persisted string or []string field whose name ends in
// SessionID or SessionIDs, descending through pointers, slices, arrays, map values and nested structs.
func sessionFields(typ reflect.Type) []string {
	var out []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(t.Elem(), path)
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			p := path + "." + f.Name
			if isSessionField(f) {
				out = append(out, p)
				continue
			}
			walk(f.Type, p)
		}
	}
	walk(typ, typ.Name())
	return out
}

func isSessionField(f reflect.StructField) bool {
	switch {
	case strings.HasSuffix(f.Name, "SessionID"):
		return f.Type.Kind() == reflect.String
	case strings.HasSuffix(f.Name, "SessionIDs"):
		return f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String
	}
	return false
}

func TestSessionFields_FindsAFieldBehindAPointerAndInAMapValue(t *testing.T) {
	type inner struct{ InnerSessionID string }
	type value struct{ ValSessionIDs []string }
	type probe struct {
		P      *inner
		M      map[string]value
		Hidden string `json:"-"`
	}

	got := sessionFields(reflect.TypeFor[probe]())

	want := []string{"probe.P.InnerSessionID", "probe.M.ValSessionIDs"}
	if !slices.Equal(got, want) {
		t.Errorf("sessionFields = %v, want %v", got, want)
	}
}

// kasParentRow is the row for KAS's run state, which no marotte struct holds.
const kasParentRow = "KAS workflow-state.json parentSessionId"

// heldRow seeds one record that names id, through the store that writes it in production.
type heldRow func(t *testing.T, f sweepFixture, id string)

// TestSessionHolders_HoldEveryPersistedSessionField fails on a session field of a stored record that no
// kirosession.Holder in sessionHolders answers for: the orphan sweep would reap what that record names.
func TestSessionHolders_HoldEveryPersistedSessionField(t *testing.T) {
	rows := map[string]heldRow{
		"Chat.ACPSessionID": func(t *testing.T, f sweepFixture, id string) {
			t.Helper()
			seedChat(t, f.chats(t), "c-held", id)
		},
		"Chat.PriorACPSessionIDs": func(t *testing.T, f sweepFixture, id string) {
			t.Helper()
			chats := f.chats(t)
			seedChat(t, chats, "c-held", id)
			seedChat(t, chats, "c-held", "sess_current")
		},
		kasParentRow: func(t *testing.T, f sweepFixture, id string) {
			t.Helper()
			f.runState(t, "bb", "wf_held", "failed", id)
		},
	}
	exempt := map[string]string{
		"Lease.ParentSessionID": "held through KAS's run state: every lease's run has state naming the same parent " +
			"(grantLease runs after _kiro/workflow/new)",
	}

	fields := slices.Concat(
		sessionFields(reflect.TypeFor[marotte.Chat]()),
		sessionFields(reflect.TypeFor[runlease.Lease]()),
	)
	for _, field := range fields {
		_, held := rows[field]
		if _, ok := exempt[field]; !held && !ok {
			t.Errorf("%s names a KAS session and no row holds it: make its record a kirosession.Holder in "+
				"sessionHolders, then add its row here", field)
		}
	}
	for name := range maps.Keys(rows) {
		if name != kasParentRow && !slices.Contains(fields, name) {
			t.Errorf("row %s names no stored field: the field was renamed or removed, so its row pins nothing", name)
		}
	}
	for name := range maps.Keys(exempt) {
		if !slices.Contains(fields, name) {
			t.Errorf("exemption %s names no stored field: the field was renamed or removed", name)
		}
	}

	for name, seed := range rows {
		t.Run(name, func(t *testing.T) {
			logs := captureSeamLogs(t)
			f := newSweepFixture(t)
			seed(t, f, "sess_held")
			f.session(t, "ba", "sess_held")

			f.bootAndSweep(t, logs)

			if !f.exists("ba", "sess_held") {
				t.Errorf("the session %s names was reaped", name)
			}
		})
	}
}
