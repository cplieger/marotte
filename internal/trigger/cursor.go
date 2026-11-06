package trigger

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// labelState is what a labeled cursor entry last observed of the rule's label.
type labelState string

const (
	labelYes labelState = "true"
	labelNo  labelState = "false"
	// labelUnknown comes only from a baseline row whose labels may be cut.
	labelUnknown labelState = "unknown"
)

type labelEntry struct {
	LastSeen time.Time  `json:"last_seen"`
	HasLabel labelState `json:"has_label"`
}

type labeledCursor struct {
	Items map[int]*labelEntry `json:"items,omitempty"`
	// TooMany marks a last complete walk that read more open items than the cap, which blocks the rule.
	TooMany bool `json:"too_many,omitempty"`
}

// openedCursor accounts for every item at or below Floor, and for each number in Seen above it.
type openedCursor struct {
	// Seen is sorted ascending.
	Seen         []int `json:"seen,omitempty"`
	Floor        int   `json:"floor"`
	PendingFloor int   `json:"pending_floor"`
}

// runHold is why a run is kept in the run map.
type runHold string

const (
	// holdPending is a run not concluded, or reading an unknown state.
	holdPending runHold = "pending"
	// holdOutside is a run concluded with a result outside the filter, kept because a re-run can conclude it again.
	holdOutside runHold = "outside"
	// holdAtForge is a run that matched while the queue was full; it is matched again once the queue has room.
	holdAtForge runHold = "at_forge"
)

type runEntry struct {
	CreatedAt time.Time `json:"created_at"`
	// UnknownSince is when the run was first read in an unknown state, zero while its state is known.
	UnknownSince time.Time `json:"unknown_since,omitzero"`
	State        string    `json:"state"`
	Hold         runHold   `json:"hold"`
	// Unlisted marks a pending run a complete walk reaching its creation did not return.
	Unlisted bool `json:"unlisted,omitempty"`
}

type runCursor struct {
	BaselineAt   time.Time `json:"baseline_at"`
	LastComplete time.Time `json:"last_complete"`
	// Runs holds each admitted run whose result is still to be decided, by run id.
	Runs map[int64]*runEntry `json:"runs,omitempty"`
	// Fired holds each queued run's creation, by run id; a run in it never matches again.
	Fired map[int64]time.Time `json:"fired,omitempty"`
}

// repoCursor is one repository's part of a rule's cursor. The part of the rule's kind is nil until its baseline
// lands.
type repoCursor struct {
	Labeled *labeledCursor `json:"labeled,omitempty"`
	Opened  *openedCursor  `json:"opened,omitempty"`
	Runs    *runCursor     `json:"runs,omitempty"`
}

func (c *repoCursor) empty() bool {
	return c.Labeled == nil && c.Opened == nil && c.Runs == nil
}

// cursor is a rule's observation cursor, keyed by RepoRef.Encode().
type cursor map[string]*repoCursor

// eventState is an event rule's state with its event parts typed; the rest of the file is carried as read.
type eventState struct {
	cursor     cursor
	queue      []queueEntry
	launched   []launchedEntry
	superseded []supersededEntry
	raw        launchrule.State
}

func decodeState(st *launchrule.State) (eventState, error) {
	es := eventState{raw: *st, cursor: cursor{}}
	if len(st.Cursor) > 0 {
		if err := json.Unmarshal(st.Cursor, &es.cursor); err != nil {
			return eventState{}, fmt.Errorf("cursor: %w", err)
		}
	}
	for i, raw := range st.Queue {
		var e queueEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return eventState{}, fmt.Errorf("queue entry %d: %w", i, err)
		}
		es.queue = append(es.queue, e)
	}
	if len(st.Launched) > 0 {
		if err := json.Unmarshal(st.Launched, &es.launched); err != nil {
			return eventState{}, fmt.Errorf("launched: %w", err)
		}
	}
	if len(st.Superseded) > 0 {
		if err := json.Unmarshal(st.Superseded, &es.superseded); err != nil {
			return eventState{}, fmt.Errorf("superseded: %w", err)
		}
	}
	return es, nil
}

func (es *eventState) encode() (launchrule.State, error) {
	st := es.raw
	st.Cursor, st.Queue, st.Launched, st.Superseded = nil, nil, nil, nil
	c := cursor{}
	for id, rc := range es.cursor {
		if rc != nil && !rc.empty() {
			c[id] = rc
		}
	}
	var err error
	if st.Cursor, err = marshalUnlessEmpty(c, len(c)); err != nil {
		return launchrule.State{}, err
	}
	for i := range es.queue {
		raw, qerr := json.Marshal(&es.queue[i])
		if qerr != nil {
			return launchrule.State{}, qerr
		}
		st.Queue = append(st.Queue, raw)
	}
	if st.Launched, err = marshalUnlessEmpty(es.launched, len(es.launched)); err != nil {
		return launchrule.State{}, err
	}
	if st.Superseded, err = marshalUnlessEmpty(es.superseded, len(es.superseded)); err != nil {
		return launchrule.State{}, err
	}
	return st, nil
}

// marshalUnlessEmpty encodes v, or nothing when it holds n == 0 elements, so the state file omits the part.
func marshalUnlessEmpty(v any, n int) (json.RawMessage, error) {
	if n == 0 {
		return nil, nil
	}
	return json.Marshal(v)
}

// knows reports whether key is queued, launched or superseded: such a key is never queued again.
func (es *eventState) knows(key string) bool {
	return slices.ContainsFunc(es.queue, func(e queueEntry) bool { return e.Key == key }) ||
		slices.ContainsFunc(es.launched, func(e launchedEntry) bool { return e.Key == key }) ||
		slices.ContainsFunc(es.superseded, func(e supersededEntry) bool { return e.Key == key })
}
