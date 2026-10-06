package agent

// The replay merge: KAS's account of a chat folded into the record. The record's order is
// the spine; nothing is sorted and no timestamp read. A projected entry the record lacks
// hangs off its nearest paired KAS-order predecessor, so a mid-turn steer stays put.
// SwapMerged owns the two gates.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// RecordTurn is one turn as the log holds it: entries in seq order, turn_open at index 0
// (RecordTurnsOf builds it). A Reverted turn is emitted unchanged and never paired, so no
// replay can resurrect it.
type RecordTurn struct {
	Entries  []marotte.Entry
	Reverted bool
}

// MergedTurn is one merged turn: entries in write order, seq renumbered from 0, turn_open's
// n the turn's ordinal among surviving turns. Reverted keeps a hidden turn out of that numbering.
type MergedTurn struct {
	Entries  []marotte.Entry
	Reverted bool
}

// synthesizedCloserSuffix names the turn_close appended to a turn neither side closed.
// Derived from the turn id, not random, so a second merge of the same replay is a no-op.
const synthesizedCloserSuffix = ":close"

// RecordTurnsOf groups one log read into the merge spine, turns in the read's first-appearance
// order, entries by seq, each stamped from reverted. A turn without turn_open is skipped with
// a Warn. Never sorted by turn_open.n: a revert can reuse an ordinal.
func RecordTurnsOf(entries []marotte.Entry, reverted map[string]struct{}) []RecordTurn {
	byTurn := make(map[string][]marotte.Entry)
	var order []string
	for _, e := range entries {
		if _, seen := byTurn[e.Turn]; !seen {
			order = append(order, e.Turn)
		}
		byTurn[e.Turn] = append(byTurn[e.Turn], e)
	}
	turns := make([]RecordTurn, 0, len(order))
	for _, id := range order {
		group := byTurn[id]
		slices.SortStableFunc(group, func(a, b marotte.Entry) int {
			return cmp.Compare(a.Seq, b.Seq)
		})
		if _, ok := turnOpenOf(group); !ok {
			slog.Warn("entry merge: a log read holds a turn with no turn_open, skipped",
				"turn", id, "entries", len(group))
			continue
		}
		_, hidden := reverted[id]
		turns = append(turns, RecordTurn{Entries: group, Reverted: hidden})
	}
	return turns
}

// turnOpenOf reads a turn's turn_open payload, false when absent or undecodable.
func turnOpenOf(entries []marotte.Entry) (marotte.EntryTurnOpen, bool) {
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		if json.Unmarshal(entries[i].Payload, &open) != nil {
			return marotte.EntryTurnOpen{}, false
		}
		return open, true
	}
	return marotte.EntryTurnOpen{}, false
}

// MergeEntries folds a replay's turns into the record's and reports whether anything moved.
// Pure. sid scopes pairing rule one: a turn_bind naming another session falls to rule two.
func MergeEntries(record []RecordTurn, projected []translate.ProjectedTurn, sid string) (merged []MergedTurn, changed bool) {
	recPartner, projPartner := pairTurns(record, projected, sid)
	// An unpaired projected turn goes right after the nearest paired record turn before it in KAS
	// order, else at the head. Only a paired turn has a KAS position, which puts a resumed
	// session's history ahead of the prompt that opened it.
	const head = -1
	buckets := make(map[int][]int, len(projected))
	anchor := head
	for j := range projected {
		if rec, paired := projPartner[j]; paired {
			anchor = rec
			continue
		}
		buckets[anchor] = append(buckets[anchor], j)
	}
	out := make([]MergedTurn, 0, len(record)+len(projected))
	for _, j := range buckets[head] {
		out = append(out, insertedTurn(&projected[j]))
		changed = true
	}
	for i := range record {
		if j, paired := recPartner[i]; paired {
			turn, moved := mergeTurn(&record[i], &projected[j])
			out = append(out, turn)
			changed = changed || moved
		} else {
			out = append(out, keptTurn(&record[i]))
		}
		for _, j := range buckets[i] {
			out = append(out, insertedTurn(&projected[j]))
			changed = true
		}
	}
	return out, renumber(out) || changed
}

// Swap is one merge swap's inputs, as a struct so the call site names every value.
type Swap struct {
	// Log is the chat's entry log; its atomic rewrite is the swap's one write.
	Log *chat.EntryLog
	// Header is the record beside the log: the gates read it and a moved watermark writes it.
	Header chat.EntryHeader
	// SessionID is the session the replay came from, turn pairing's rule-one scope.
	SessionID string
	// Record is the log's own account of the transcript, grouped by RecordTurnsOf.
	Record []RecordTurn
	// Snapshot is the newest turn_revert's entry id when the projection opened, or empty.
	Snapshot string
	// Projected is the replay's account, from EntryProjection.Turns.
	Projected []translate.ProjectedTurn
}

// SwapMerged runs the merge's two gates and, on passing both, makes the merged transcript the
// log. Gate 1 is the log's own reconcile predicate (synthesized closer, empty-text steer,
// unbound session). Gate 2 is the newest turn_revert's id, so a replay predating a rewind
// cannot hand reverted turns back. A failed gate changes nothing; a revert under it Warns.
func SwapMerged(ctx context.Context, s *Swap) (bool, error) {
	header, err := s.Header.Read(ctx)
	if err != nil {
		return false, err
	}
	if !s.Log.NeedsReconcile() {
		slog.Debug("entry merge: projection discarded, the record holds no evidence of loss",
			"projected_turns", len(s.Projected))
		return false, nil
	}
	if newest, _ := s.Log.NewestRevert(); newest != s.Snapshot {
		slog.Warn("entry merge: projection discarded, a revert landed while the replay was in flight",
			"snapshot_revert", s.Snapshot, "newest_revert", newest)
		return false, nil
	}
	// Read off the predicate that decided gate 1, so the records clear exactly what it answered.
	turns, session := s.Log.ReconcileTargets()
	merged, changed := MergeEntries(s.Record, s.Projected, s.SessionID)
	if !changed {
		// Nothing to add: the clearing records are the whole write, or every resume re-runs the merge.
		// Nothing is broadcast; no client's transcript moved.
		return false, clearReconcileSignals(ctx, s.Log, turns, session)
	}
	// Signals this merge could not settle ride the same atomic rewrite.
	insertReconciled(merged, header.ACPSessionID)
	var entries []marotte.Entry
	for i := range merged {
		entries = append(entries, merged[i].Entries...)
	}
	// Rewrite re-caches turn_count and last_turn_outcome and stamps nothing else.
	if err := s.Log.Rewrite(ctx, entries); err != nil {
		return false, err
	}
	if err := recordWatermark(ctx, s.Header, header.CompactionWatermark, entries); err != nil {
		return true, err
	}
	return true, nil
}

// clearReconcileSignals writes one reconciled record per signal looked at, making a no-op swap
// final. A failure is returned: swallowing it recreates the loop.
func clearReconcileSignals(ctx context.Context, log *chat.EntryLog, turns []string, session string) error {
	for _, turn := range turns {
		if _, _, err := log.AppendReconciled(ctx, marotte.EntryReconciled{Turn: turn}); err != nil {
			return fmt.Errorf("entry merge: record that turn %s was reconciled: %w", turn, err)
		}
	}
	if session == "" {
		return nil
	}
	if _, _, err := log.AppendReconciled(ctx, marotte.EntryReconciled{Session: session}); err != nil {
		return fmt.Errorf("entry merge: record that session %s was adopted: %w", session, err)
	}
	return nil
}

// insertReconciled files the signal-stopping records into the merged turns, in the one Rewrite
// (an extra append could fail with the signal still armed). Clearers are computed over the
// MERGED entries, since the merge can settle a signal itself. Reverted turns are skipped.
func insertReconciled(turns []MergedTurn, session string) {
	if len(turns) == 0 {
		return
	}
	recorded, sessionKnown := mergedClearers(turns, session)
	for i := range turns {
		if turns[i].Reverted {
			continue
		}
		id := turns[i].Entries[0].Turn
		if _, done := recorded[id]; done {
			continue
		}
		if !holdsEvidenceOfLoss(turns[i].Entries) {
			continue
		}
		appendReconciled(&turns[i], marotte.EntryReconciled{Turn: id})
	}
	// The session form's envelope is the newest surviving turn; a change always leaves one, so a missing envelope is a no-op.
	if session != "" && !sessionKnown {
		if last, ok := lastSurviving(turns); ok {
			appendReconciled(last, marotte.EntryReconciled{Session: session})
		}
	}
}

// lastSurviving is the newest merged turn the surviving view holds.
func lastSurviving(turns []MergedTurn) (*MergedTurn, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		if !turns[i].Reverted {
			return &turns[i], true
		}
	}
	return nil, false
}

// mergedClearers reads the merged list's reconcile answers: turns a reconciled record names, and
// whether the header's session is adopted.
func mergedClearers(turns []MergedTurn, session string) (recorded map[string]struct{}, sessionKnown bool) {
	recorded = make(map[string]struct{})
	for i := range turns {
		for j := range turns[i].Entries {
			if clearerAdopts(&turns[i].Entries[j], session, recorded) {
				sessionKnown = true
			}
		}
	}
	return recorded, sessionKnown
}

// clearerAdopts reads one entry's part of mergedClearers' answer; an undecodable payload answers nothing.
func clearerAdopts(e *marotte.Entry, session string, recorded map[string]struct{}) bool {
	switch e.Kind {
	case marotte.EntryKindReconciled:
		var rec marotte.EntryReconciled
		if json.Unmarshal(e.Payload, &rec) != nil {
			return false
		}
		if rec.Turn != "" {
			recorded[rec.Turn] = struct{}{}
		}
		return rec.Session != "" && rec.Session == session
	case marotte.EntryKindTurnBind:
		var bind marotte.EntryTurnBind
		if json.Unmarshal(e.Payload, &bind) != nil {
			return false
		}
		return bind.SessionID == session
	}
	return false
}

// holdsEvidenceOfLoss asks conditions (ii) and (iii) of one merged turn: an unterminated closer or a steer missing its words.
func holdsEvidenceOfLoss(entries []marotte.Entry) bool {
	for i := range entries {
		switch entries[i].Kind {
		case marotte.EntryKindTurnClose:
			var closer marotte.EntryTurnClose
			if json.Unmarshal(entries[i].Payload, &closer) == nil &&
				closer.StopReasonRaw == string(marotte.StopReasonUnterminated) {
				return true
			}
		case marotte.EntryKindSteer:
			var steer marotte.EntrySteer
			if json.Unmarshal(entries[i].Payload, &steer) == nil && steer.Text == "" {
				return true
			}
		}
	}
	return false
}

// appendReconciled files a record at the end of a merged turn, one seq past its predecessor.
// The seq matters despite renumbering: groupByTurn sorts by seq.
func appendReconciled(turn *MergedTurn, rec marotte.EntryReconciled) {
	last := turn.Entries[len(turn.Entries)-1]
	e := marotte.Entry{
		ID:   chat.ReconciledEntryID(rec),
		Turn: last.Turn,
		Seq:  last.Seq + 1,
		Kind: marotte.EntryKindReconciled,
	}
	setPayload(&e, rec)
	turn.Entries = append(turn.Entries, e)
}

// recordWatermark moves the header's compaction watermark to the merged log's newest
// compaction (rule 3), never backwards.
func recordWatermark(ctx context.Context, h chat.EntryHeader, held string, entries []marotte.Entry) error {
	newest := ""
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindCompaction {
			newest = entries[i].ID
		}
	}
	if newest == "" || newest == held {
		return nil
	}
	_, err := h.Update(ctx, func(c *marotte.Chat) bool {
		c.CompactionWatermark = newest
		return true
	})
	return err
}

// turnKeys are the two keys one turn offers turn pairing.
type turnKeys struct {
	// prompt is rule one's key: the in-scope turn_bind's kas_message_id on the record side, the
	// turn_open's prompt id on the projected side. Exact: KAS mints one id per prompt.
	prompt string
	// content is rule two's key: the first text, thinking or tool_call id, a subagent invocation
	// keyed on its delegate uuid. Unscoped: a KAS uuid belongs to one session.
	content string
}

// pairTurns applies the two pairing rules in order and answers both directions.
func pairTurns(record []RecordTurn, projected []translate.ProjectedTurn, sid string) (recPartner, projPartner map[int]int) {
	recKeys := recordTurnKeys(record, sid)
	projKeys := projectedTurnKeys(projected)
	recPartner, projPartner = make(map[int]int), make(map[int]int)
	pairOnKey(recKeys, projKeys, recPartner, projPartner, func(k turnKeys) string { return k.prompt })
	pairOnKey(recKeys, projKeys, recPartner, projPartner, func(k turnKeys) string { return k.content })
	return recPartner, projPartner
}

// recordTurnKeys is each record turn's two keys.
func recordTurnKeys(record []RecordTurn, sid string) []turnKeys {
	keys := make([]turnKeys, len(record))
	for i := range record {
		// A reverted turn gets zero keys, which excludes it from pairing.
		if record[i].Reverted {
			continue
		}
		keys[i] = turnKeys{
			prompt:  boundPromptID(record[i].Entries, sid),
			content: firstContentKey(record[i].Entries),
		}
	}
	return keys
}

// projectedTurnKeys is each projected turn's two keys; rule one's is its turn_open prompt id.
func projectedTurnKeys(projected []translate.ProjectedTurn) []turnKeys {
	keys := make([]turnKeys, len(projected))
	for j := range projected {
		open, _ := turnOpenOf(projected[j].Entries)
		if open.Prompt != nil {
			keys[j].prompt = open.Prompt.ID
		}
		keys[j].content = firstContentKey(projected[j].Entries)
	}
	return keys
}

// pairOnKey pairs on one key, skipping already-paired turns. A duplicate projected key pairs
// only the first; inserting is the recoverable answer.
func pairOnKey(recKeys, projKeys []turnKeys, recPartner, projPartner map[int]int, key func(turnKeys) string) {
	byKey := firstByKey(projKeys, key)
	for i := range recKeys {
		k := key(recKeys[i])
		if k == "" {
			continue
		}
		if _, taken := recPartner[i]; taken {
			continue
		}
		j, ok := byKey[k]
		if !ok {
			continue
		}
		if _, taken := projPartner[j]; taken {
			continue
		}
		recPartner[i], projPartner[j] = j, i
	}
}

// firstByKey indexes the FIRST projected turn per non-empty key, and drops a repeat.
func firstByKey(projKeys []turnKeys, key func(turnKeys) string) map[string]int {
	byKey := make(map[string]int, len(projKeys))
	for j := range projKeys {
		k := key(projKeys[j])
		if k == "" {
			continue
		}
		if _, dup := byKey[k]; dup {
			continue
		}
		byKey[k] = j
	}
	return byKey
}

// boundPromptID is the id a turn's turn_bind names, only when minted in the replayed session,
// so an empty turn and its retry pair correctly.
func boundPromptID(entries []marotte.Entry, sid string) string {
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnBind {
			continue
		}
		var bind marotte.EntryTurnBind
		if json.Unmarshal(entries[i].Payload, &bind) != nil {
			continue
		}
		if bind.SessionID != sid {
			continue
		}
		return bind.KASMessageID
	}
	return ""
}

// firstContentKey is rule two's key: the first content id with a say's `#k` suffix removed, or
// a subagent's delegate uuid (KAS suffixes `-sub-agent-start` on one side).
func firstContentKey(entries []marotte.Entry) string {
	for i := range entries {
		switch entries[i].Kind {
		case marotte.EntryKindText, marotte.EntryKindThinking:
			return marotte.SayIDOf(entries[i].ID)
		case marotte.EntryKindToolCall:
			if key := invocationKey(&entries[i]); key != "" {
				return key
			}
			return entries[i].ID
		}
	}
	return ""
}

// invocationKey is a subagent invocation's delegate uuid; empty for an ordinary tool call.
func invocationKey(e *marotte.Entry) string {
	var call marotte.EntryToolCall
	if json.Unmarshal(e.Payload, &call) != nil {
		return ""
	}
	return call.AgentSubtaskID
}

// keptTurn is an unpaired record turn, emitted as a copy: renumber writes into its slice.
func keptTurn(rec *RecordTurn) MergedTurn {
	return MergedTurn{Entries: slices.Clone(rec.Entries), Reverted: rec.Reverted}
}

// insertedTurn is an unpaired projected turn, inserted whole with its generator-minted id.
// Rule 3's stamps apply to every entry; the synthesized closer lands when the replay had no turn_end.
func insertedTurn(proj *translate.ProjectedTurn) MergedTurn {
	turn := proj.Entries[0].Turn
	entries := make([]marotte.Entry, 0, len(proj.Entries)+1)
	for i := range proj.Entries {
		entries = append(entries, insertedEntry(&proj.Entries[i], turn))
	}
	if !holdsKind(entries, marotte.EntryKindTurnClose) {
		entries = append(entries, synthesizedCloser(turn))
	}
	return MergedTurn{Entries: entries}
}

// insertedEntry is a projected entry with no record twin. A projected-only steer is stamped
// `dropped, restart`: marotte persists a steer at injection, so the record's absence proves
// it was never read as a steer.
func insertedEntry(proj *marotte.Entry, turn string) marotte.Entry {
	out := *proj
	out.Turn = turn
	if out.Kind != marotte.EntryKindSteer {
		return out
	}
	var steer marotte.EntrySteer
	if json.Unmarshal(out.Payload, &steer) != nil {
		return out
	}
	steer.State = marotte.SteerStateDropped
	steer.Reason = marotte.SteerReasonRestart
	setPayload(&out, steer)
	return out
}

// synthesizedCloser is the closer for a turn neither side closed; its placeholder stop reason
// lets a later merge replace it.
func synthesizedCloser(turn string) marotte.Entry {
	e := marotte.Entry{
		ID:   turn + synthesizedCloserSuffix,
		Turn: turn,
		Kind: marotte.EntryKindTurnClose,
	}
	setPayload(&e, marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
	})
	return e
}

// holdsKind reports whether a turn already holds an entry of that kind.
func holdsKind(entries []marotte.Entry, kind marotte.EntryKind) bool {
	return slices.ContainsFunc(entries, func(e marotte.Entry) bool { return e.Kind == kind })
}

// setPayload re-marshals an entry's payload in place; a failure leaves the entry and logs.
func setPayload(e *marotte.Entry, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("entry merge: could not marshal a merged payload",
			"turn", e.Turn, "entry", e.ID, "kind", e.Kind, "error", err)
		return
	}
	e.Payload = raw
}

// recordEntryIndex keys a record turn's entries the four ways entry pairing asks.
type recordEntryIndex struct {
	byID map[string]int
	// bySay holds every segment of a say in seq order: projected S pairs with every S and S#k,
	// and the record's segment boundaries are never merged.
	bySay map[string][]int
	// byInvocation is each subagent invocation's delegate uuid.
	byInvocation map[string]int
	// turnOpen and turnClose pair by kind: their ids are minted differently on each side.
	turnOpen  int
	turnClose int
}

func indexRecordEntries(entries []marotte.Entry) recordEntryIndex {
	idx := recordEntryIndex{
		byID:         make(map[string]int, len(entries)),
		bySay:        make(map[string][]int),
		byInvocation: make(map[string]int),
		turnOpen:     -1,
		turnClose:    -1,
	}
	for i := range entries {
		idx.add(&entries[i], i)
	}
	return idx
}

// add files a record entry under every key that can claim it; first wins, keeping record order.
func (idx *recordEntryIndex) add(e *marotte.Entry, i int) {
	if _, dup := idx.byID[e.ID]; !dup {
		idx.byID[e.ID] = i
	}
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		if idx.turnOpen < 0 {
			idx.turnOpen = i
		}
	case marotte.EntryKindTurnClose:
		if idx.turnClose < 0 {
			idx.turnClose = i
		}
	case marotte.EntryKindText, marotte.EntryKindThinking:
		say := marotte.SayIDOf(e.ID)
		idx.bySay[say] = append(idx.bySay[say], i)
	case marotte.EntryKindToolCall:
		if key := invocationKey(e); key != "" {
			if _, dup := idx.byInvocation[key]; !dup {
				idx.byInvocation[key] = i
			}
		}
	}
}

// entryPairing is one projected entry's record twins, or none.
type entryPairing struct {
	// group is every record index this entry claims, in seq order; several only for a say.
	group []int
}

// mergeTurn folds a projected turn into its paired record turn, keeping the record's turn id.
func mergeTurn(rec *RecordTurn, proj *translate.ProjectedTurn) (MergedTurn, bool) {
	turn := rec.Entries[0].Turn
	idx := indexRecordEntries(rec.Entries)
	pairing := pairEntries(rec.Entries, proj.Entries, idx)

	// Consecutive projected-only entries off one predecessor are emitted in KAS order.
	head := max(idx.turnOpen, 0)
	queued := make(map[int][]int, len(proj.Entries))
	unioned := make(map[int]int, len(rec.Entries))
	after := head
	for j := range proj.Entries {
		if g := pairing[j].group; len(g) > 0 {
			for _, i := range g {
				unioned[i] = j
			}
			// A say's position is its last segment, so a following entry lands after all of it.
			after = g[len(g)-1]
			continue
		}
		queued[after] = append(queued[after], j)
	}
	warnUnpairedCompactions(turn, rec.Entries, proj.Entries, unioned, pairing)

	changed := false
	out := make([]marotte.Entry, 0, len(rec.Entries)+len(proj.Entries)+1)
	for i := range rec.Entries {
		emitted := rec.Entries[i]
		if j, paired := unioned[i]; paired {
			emitted = unionEntry(&rec.Entries[i], &proj.Entries[j], sayExcess(rec.Entries, idx, i, &proj.Entries[j]))
			changed = changed || !sameEntry(&rec.Entries[i], &emitted)
		}
		out = append(out, emitted)
		for _, j := range queued[i] {
			out = append(out, insertedEntry(&proj.Entries[j], turn))
			changed = true
		}
	}
	if !holdsKind(out, marotte.EntryKindTurnClose) {
		out = append(out, synthesizedCloser(turn))
		changed = true
	}
	return MergedTurn{Entries: out, Reverted: rec.Reverted}, changed
}

// pairEntries answers each projected entry's record twin, claiming each record entry once.
func pairEntries(rec, proj []marotte.Entry, idx recordEntryIndex) []entryPairing {
	c := entryClaims{
		claimed: make(map[int]bool, len(rec)),
		pairing: make([]entryPairing, len(proj)),
	}
	// A result pairs through its call, whose twin is always known by then.
	resultTwin := make(map[string]int, len(proj))
	for j := range proj {
		e := &proj[j]
		switch e.Kind {
		case marotte.EntryKindTurnOpen:
			c.claim(j, idx.turnOpen)
		case marotte.EntryKindTurnClose:
			c.claim(j, idx.turnClose)
		case marotte.EntryKindText, marotte.EntryKindThinking:
			c.claim(j, idx.bySay[marotte.SayIDOf(e.ID)]...)
		case marotte.EntryKindToolCall:
			key := invocationKey(e)
			if key == "" {
				c.claim(j, indexOr(idx.byID, e.ID))
				break
			}
			i := indexOr(idx.byInvocation, key)
			c.claim(j, i)
			if i >= 0 {
				resultTwin[marotte.ToolResultID(e.ID)] = indexOr(idx.byID, marotte.ToolResultID(rec[i].ID))
			}
		case marotte.EntryKindToolResult:
			if i, through := resultTwin[e.ID]; through {
				c.claim(j, i)
				break
			}
			c.claim(j, indexOr(idx.byID, e.ID))
		default:
			c.claim(j, indexOr(idx.byID, e.ID))
		}
	}
	return c.pairing
}

// entryClaims accumulates the pairing, claiming each record entry once.
type entryClaims struct {
	claimed map[int]bool
	pairing []entryPairing
}

// claim pairs projected j with every index in group, or nothing if any is missing or taken:
// a partial group would duplicate a say's segments.
func (c *entryClaims) claim(j int, group ...int) {
	if len(group) == 0 {
		return
	}
	for _, i := range group {
		if i < 0 || c.claimed[i] {
			return
		}
	}
	for _, i := range group {
		c.claimed[i] = true
	}
	c.pairing[j].group = group
}

// indexOr is a map lookup answering -1 for a miss.
func indexOr(m map[string]int, key string) int {
	if i, ok := m[key]; ok {
		return i
	}
	return -1
}

// warnUnpairedCompactions warns when both sides hold an unpaired compaction: their summary
// bytes differ (the open auto-compaction corner), and the reader sees two rows.
func warnUnpairedCompactions(turn string, rec, proj []marotte.Entry, unioned map[int]int, pairing []entryPairing) {
	recID := ""
	for i := range rec {
		if _, paired := unioned[i]; paired || rec[i].Kind != marotte.EntryKindCompaction {
			continue
		}
		recID = rec[i].ID
		break
	}
	if recID == "" {
		return
	}
	for j := range proj {
		if proj[j].Kind != marotte.EntryKindCompaction || len(pairing[j].group) > 0 {
			continue
		}
		slog.Warn("entry merge: the record and the replay hold different summary bytes for one compaction",
			"turn", turn, "record_compaction", recID, "replayed_compaction", proj[j].ID)
		return
	}
}

// sayExcess is the tail a paired say's last segment gains when the projection is longer; ""
// otherwise. Segments are never merged.
func sayExcess(rec []marotte.Entry, idx recordEntryIndex, at int, proj *marotte.Entry) string {
	if proj.Kind != marotte.EntryKindText && proj.Kind != marotte.EntryKindThinking {
		return ""
	}
	group := idx.bySay[marotte.SayIDOf(proj.ID)]
	if len(group) == 0 || group[len(group)-1] != at {
		return ""
	}
	var concat strings.Builder
	for _, i := range group {
		concat.WriteString(sayTextOf(&rec[i]))
	}
	whole := sayTextOf(proj)
	held := concat.String()
	if len(whole) <= len(held) {
		return ""
	}
	// Not a prefix: splicing by byte offset would graft unrelated text, so the record stands.
	if !strings.HasPrefix(whole, held) {
		slog.Debug("entry merge: a replayed say diverges from the record's segments, record kept",
			"turn", proj.Turn, "say", proj.ID, "record_bytes", len(held), "replayed_bytes", len(whole))
		return ""
	}
	return whole[len(held):]
}

// sayTextOf is a text or thinking entry's text; both payloads share the field.
func sayTextOf(e *marotte.Entry) string {
	var say marotte.EntryText
	if json.Unmarshal(e.Payload, &say) != nil {
		return ""
	}
	return say.Text
}

// unionEntry merges a record entry with its projected twin. The record is the base, so new
// payload fields survive by default; a KAS-observed fact fills an empty record field, and a
// process-only fact stays the record's.
func unionEntry(rec, proj *marotte.Entry, excess string) marotte.Entry {
	out := *rec
	// The record's lane is the live observation and wins; a disagreement logs.
	if out.Lane == "" {
		out.Lane = proj.Lane
	} else if proj.Lane != "" && proj.Lane != out.Lane {
		slog.Warn("entry merge: the replay files an entry in another lane than the record",
			"turn", out.Turn, "entry", out.ID, "record_lane", out.Lane, "replayed_lane", proj.Lane)
	}
	out.Ts = cmp.Or(out.Ts, proj.Ts)
	switch out.Kind {
	case marotte.EntryKindText, marotte.EntryKindThinking:
		unionSay(&out, excess)
	case marotte.EntryKindToolResult:
		unionToolResult(&out, proj)
	case marotte.EntryKindSteer:
		unionSteer(&out, proj)
	case marotte.EntryKindCompaction:
		unionCompaction(&out, proj)
	case marotte.EntryKindTurnClose:
		unionTurnClose(&out, proj)
	}
	// Every other kind: the projection adds nothing.
	return out
}

// unionSay appends a longer projected say's excess onto the record's last segment.
func unionSay(out *marotte.Entry, excess string) {
	if excess == "" {
		return
	}
	text := sayTextOf(out) + excess
	if out.Kind == marotte.EntryKindThinking {
		setPayload(out, marotte.EntryThinking{Text: text})
		return
	}
	setPayload(out, marotte.EntryText{Text: text})
}

// unionToolResult merges the settled value of one tool call.
func unionToolResult(out, proj *marotte.Entry) {
	var r, p marotte.EntryToolResult
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	// The record's `aborted` is a close-time inference; KAS's terminal status replaces it.
	if r.Status == marotte.ToolAborted && p.Status.Terminal() {
		r.Status = p.Status
	}
	// Filled only where the record states nothing.
	r.Output = cmp.Or(r.Output, p.Output)
	r.WorkflowID = cmp.Or(r.WorkflowID, p.WorkflowID)
	if len(r.Diffs) == 0 {
		r.Diffs = p.Diffs
	}
	if r.Checkpoint == nil {
		r.Checkpoint = p.Checkpoint
	}
	if r.Disclosed == nil {
		r.Disclosed = p.Disclosed
	}
	if r.Offload == nil {
		r.Offload = p.Offload
	}
	if r.Interaction == nil {
		r.Interaction = p.Interaction
	}
	// duration_ms, terminal_id and output_spans stay the record's: KAS stamps call and result
	// identically, and the others are live-only.
	setPayload(out, r)
}

// unionSteer fills a steer the record wrote from a cleared id alone. state, origin and reason
// stay the record's: only the live frame knows whether the model read it.
func unionSteer(out, proj *marotte.Entry) {
	var r, p marotte.EntrySteer
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	// KAS persisted the text at send time; the clear this process saw carried none.
	r.Text = cmp.Or(r.Text, p.Text)
	r.Severity = cmp.Or(r.Severity, p.Severity)
	setPayload(out, r)
}

// unionCompaction fills a summary the record lost; both ids paired on the same bytes.
func unionCompaction(out, proj *marotte.Entry) {
	var r, p marotte.EntryCompaction
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	r.Summary = cmp.Or(r.Summary, p.Summary)
	setPayload(out, r)
}

// unionTurnClose replaces the store's placeholder closer with KAS's account and leaves a
// process-written closer alone. Conclusion fields travel as a unit, with the completion facts
// and refusal. model, changed_files and code_references stay the record's.
func unionTurnClose(out, proj *marotte.Entry) {
	var r, p marotte.EntryTurnClose
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	if r.StopReasonRaw != string(marotte.StopReasonUnterminated) {
		return
	}
	r.Outcome = p.Outcome
	r.StopReasonRaw = p.StopReasonRaw
	r.FailureReason = p.FailureReason
	r.FailureKind = p.FailureKind
	r.Truncated = p.Truncated
	r.Credits = p.Credits
	r.ElapsedMs = p.ElapsedMs
	r.EngineErrorClass = p.EngineErrorClass
	if r.Refusal == nil {
		r.Refusal = p.Refusal
	}
	// The placeholder saw no completion frames.
	r.Throughput = p.Throughput
	r.RequestIDs = p.RequestIDs
	r.Recoveries = p.Recoveries
	r.Steering = p.Steering
	setPayload(out, r)
}

// sameEntry reports whether the merge left a record entry alone; seq excluded, payloads compared through the encoder.
func sameEntry(rec, out *marotte.Entry) bool {
	return rec.ID == out.ID && rec.Turn == out.Turn && rec.Lane == out.Lane &&
		rec.Kind == out.Kind && rec.Ts == out.Ts && sameRawJSON(rec.Payload, out.Payload)
}

// renumber assigns seq from 0 per turn and turn_open n from 1 over surviving turns, reporting
// any move. A hidden turn keeps its n but gets seq like every turn.
func renumber(turns []MergedTurn) bool {
	moved := false
	surviving := uint64(0)
	for i := range turns {
		entries := turns[i].Entries
		if !turns[i].Reverted {
			surviving++
		}
		for seq := range entries {
			if entries[seq].Seq != uint64(seq) {
				entries[seq].Seq = uint64(seq)
				moved = true
			}
			if !turns[i].Reverted && renumberOpen(&entries[seq], surviving) {
				moved = true
			}
		}
	}
	return moved
}

// renumberOpen sets a turn_open's n, reporting a move; anything else is left alone.
func renumberOpen(e *marotte.Entry, n uint64) bool {
	if e.Kind != marotte.EntryKindTurnOpen {
		return false
	}
	var open marotte.EntryTurnOpen
	if json.Unmarshal(e.Payload, &open) != nil || open.N == n {
		return false
	}
	open.N = n
	setPayload(e, open)
	return true
}

// sameRawJSON compares two raw JSON values as the encoder writes them (json.Marshal compacts and
// escapes). A marshal error falls back to bytes, erring toward writing.
func sameRawJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	na, err := json.Marshal(a)
	if err != nil {
		return bytes.Equal(a, b)
	}
	nb, err := json.Marshal(b)
	if err != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(na, nb)
}
