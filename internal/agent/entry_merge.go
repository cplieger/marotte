package agent

// The replay merge over the turn log: KAS's account of a chat folded into the record.
// The record's order is the SPINE, nothing is sorted and no timestamp is read: a
// projected entry the record lacks hangs off its nearest paired KAS-order predecessor,
// which is what lets a mid-turn steer stay where it was read. SwapMerged owns the two
// gates that decide whether a merge runs at all: the log's own reconcile predicate,
// and the provenance the projection opened on.

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

// RecordTurn is one turn as the log holds it: its entries in seq order, the turn_open
// at index 0. The turn id is Entries[0].Turn, and RecordTurnsOf is what builds the
// shape from a log read.
//
// Reverted says a turn_revert's window holds this turn. Such a turn is emitted unchanged
// and is never a pairing candidate, so no replay can resurrect one: the flag rides the
// spine the merge already walks rather than being recomputed here, because the rule has
// one implementation and it is the index's scan.
type RecordTurn struct {
	Entries  []marotte.Entry
	Reverted bool
}

// MergedTurn is one turn of the merged log: its entries in the order they will be
// written, seq renumbered from 0 and the turn_open's n set to the turn's ordinal among
// the merged SURVIVING turns. Reverted carries the record's own flag, which is what
// keeps a hidden turn out of that numbering and out of the reconciled records.
type MergedTurn struct {
	Entries  []marotte.Entry
	Reverted bool
}

// synthesizedCloserSuffix names the turn_close the merge appends to a turn neither
// side closed. DERIVED from the turn id rather than minted at random, because the
// merge must be idempotent: a random id would make a second merge of the same replay
// report a change and rewrite the log again.
const synthesizedCloserSuffix = ":close"

// RecordTurnsOf groups one log read into the spine the merge walks: one RecordTurn per
// turn, turns in the read's own first-appearance order and entries in seq order within a
// turn. reverted is the set AllWithReverted answers beside the entries, and each turn is
// stamped from it. A turn with no turn_open is skipped with one Warn.
//
// The order is the READ's, which is file order, and never turn_open.n: two turns can
// carry one n once a revert has reused an ordinal (§2.3), so an n sort is ambiguous, and
// it interleaves reverted and surviving turns in the file the rewrite writes. This
// output is groupByTurn's input, so sorting here decides that order whatever the log
// does.
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

// turnOpenOf reads a turn's turn_open payload, reporting false when the turn holds
// none or its payload does not decode.
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

// MergeEntries folds a replay's turns into the record's, and reports whether anything
// moved. Pure: no store, no clock, no id generator.
//
// `sid` is the session the replay came from, and it scopes turn pairing's rule one: a
// stamp is valid only for the session that minted it, so a record turn whose turn_bind
// names another session offers rule one nothing and falls to rule two.
func MergeEntries(record []RecordTurn, projected []translate.ProjectedTurn, sid string) (merged []MergedTurn, changed bool) {
	recPartner, projPartner := pairTurns(record, projected, sid)
	// An unpaired projected turn is inserted immediately after the nearest PAIRED
	// record turn preceding it in KAS order, or at the HEAD when none precedes it.
	// Consecutive ones hang off that one predecessor in KAS order. Only a paired record
	// turn has a KAS position, so an unpaired record turn is never the anchor — which is
	// what puts a resumed session's whole history ahead of the prompt that opened it.
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

// Swap is one merge swap's inputs: the two store halves, the pairing scope, the two
// accounts of the transcript, and the provenance the projection opened on. A struct
// rather than six parameters, so the call site names every value it passes.
type Swap struct {
	// Log is the chat's entry log, whose atomic rewrite is the swap's one write to it.
	Log *chat.EntryLog
	// Header is the record beside that log: the two gates read it and a moved watermark
	// writes it.
	Header chat.EntryHeader
	// SessionID is the session the replay came from, turn pairing's rule-one scope.
	SessionID string
	// Record is the log's own account of the transcript, grouped by RecordTurnsOf.
	Record []RecordTurn
	// Snapshot is the newest turn_revert's entry id when the projection opened, empty
	// when the log held none.
	Snapshot string
	// Projected is the replay's account, from EntryProjection.Turns.
	Projected []translate.ProjectedTurn
}

// SwapMerged runs the merge's two gates and, on passing both, makes the merged
// transcript the log.
//
// Gate 1 is the log's OWN reconcile predicate rather than a header flag: the records the
// log holds are what say something was lost — a synthesized unterminated closer, an
// empty-text steer, a session this record has never bound — so the evidence and the
// transcript can no longer disagree.
//
// Gate 2 is the log's own provenance rather than a header counter: a projection built
// from a replay that predates a rewind may not hand the reverted turns back, and a
// revert is a RECORD, so the gate is the newest turn_revert's id read off the log.
//
// A projection that fails either gate leaves the log, the header and the descriptor
// untouched and reports no change: silently when the log holds no evidence, which is the
// common resume, and with one Warn naming both revert ids when one landed under it.
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
	// Read off the predicate that just decided gate 1, so the records this swap writes
	// clear exactly the signals it answered rather than a set derived a second time.
	turns, session := s.Log.ReconcileTargets()
	merged, changed := MergeEntries(s.Record, s.Projected, s.SessionID)
	if !changed {
		// The merge looked and had nothing to add, so the records that STOP the signal
		// are this swap's whole write. Without them the log still holds the evidence,
		// so the next resume asks the same question and re-runs a whole-file merge that
		// changes nothing, for the life of the chat. Nothing is broadcast: a reconciled
		// entry draws nothing and the carrier a session form may mint holds no drawn
		// entry either, so no client's transcript moved.
		return false, clearReconcileSignals(ctx, s.Log, turns, session)
	}
	// The records that stop a signal this merge could NOT settle ride the rewrite, so
	// one atomic write carries the merged transcript and the verdict on it.
	insertReconciled(merged, header.ACPSessionID)
	var entries []marotte.Entry
	for i := range merged {
		entries = append(entries, merged[i].Entries...)
	}
	// Rewrite re-caches turn_count and last_turn_outcome from the merged log in the one
	// operation, and stamps nothing else: the records above are what say what it looked
	// at, and the reverts already in the log are what gate the next projection.
	if err := s.Log.Rewrite(ctx, entries); err != nil {
		return false, err
	}
	if err := recordWatermark(ctx, s.Header, header.CompactionWatermark, entries); err != nil {
		return true, err
	}
	return true, nil
}

// clearReconcileSignals writes one reconciled record per signal the merge looked at,
// which is what makes a swap that added nothing FINAL. Per signal rather than one
// record for the log, because each condition is cleared by naming the thing it is about:
// a turn for the closer and the empty steer, the session for the history this record
// has never adopted.
//
// A failure is returned rather than logged: the whole point of the write is that the
// next resume does not re-ask, so a swallowed error is the loop it exists to close.
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

// insertReconciled files the records that STOP a reconcile signal into the turns this
// merge just built, as members of the entry list Rewrite is handed. One write rather
// than a rewrite plus an append: an append is a second write that can fail with the
// signal still armed, and the next resume would then re-run the whole merge.
//
// The clearers are computed over the MERGED entries, never over the targets gate 1
// read. The union rewrites a placeholder `unterminated` closer into KAS's own account
// and fills an empty steer's text, so a signal the gate saw can be SETTLED by this
// merge's own output — and a record filed for it would say "looked, nothing to add"
// about a turn this merge added to.
//
// A REVERTED turn is skipped in both places: a record filed for one would state that a
// turn no reader can see was looked at, and the session form's envelope must be a turn the
// surviving view holds or the clearer is invisible to the predicate it exists to stop.
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
	// The session form is lane-less, so its envelope is the newest SURVIVING turn of the
	// rewritten order: the same turn AppendReconciled's own form takes, and the same one
	// the revert's carrier is. A log with no survivor cannot be reached here — a merge
	// that changed something always leaves one — so a missing envelope is a no-op rather
	// than a mint this pure function has no id generator for.
	if session != "" && !sessionKnown {
		if last, ok := lastSurviving(turns); ok {
			appendReconciled(last, marotte.EntryReconciled{Session: session})
		}
	}
}

// lastSurviving is the newest merged turn the surviving view holds, which is where a
// lane-less record belongs.
func lastSurviving(turns []MergedTurn) (*MergedTurn, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		if !turns[i].Reverted {
			return &turns[i], true
		}
	}
	return nil, false
}

// mergedClearers reads the merged list's own answers to §2.6's clauses: the turns a
// reconciled record already names, and whether the header's session is one this record
// has adopted — bound by a turn_bind the replay carried, or already recorded.
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

// clearerAdopts reads one entry's part of mergedClearers' answer: a reconciled record
// adds the turn it names to recorded, and either kind reports whether it adopts the
// header's session. An undecodable payload answers nothing.
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

// holdsEvidenceOfLoss is conditions (ii) and (iii) asked of ONE merged turn: a closer
// still stating that nothing closed it, or a steer whose words KAS holds and this
// record does not.
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

// appendReconciled files one record at the END of a merged turn, one seq past the entry
// it follows. The seq is load-bearing although Rewrite renumbers: groupByTurn sorts a
// group BY seq, so a zero-seq record would sort level with the turn_open and land ahead
// of the body it is a verdict on.
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

// recordWatermark moves the header's compaction watermark to the merged log's NEWEST
// compaction, which is rule 3's other half: an inserted compaction is one the record
// lost to a crash before its seal, or one a sub-execution wrote into the parent's log
// with no live frame, and the context bar counts up to that id.
//
// Derived from the merged log rather than reported by the merge, so a paired compaction
// costs nothing: it already carries the id the watermark names, and taking the newest
// one is also what stops an older inserted compaction moving the watermark backwards.
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
	// prompt is the record id of the prompt this turn ran: on the record side the
	// turn_bind's kas_message_id when the bind is in scope, on the projected side the
	// turn_open's own prompt id. Rule one's only key, and exact — KAS mints one record
	// id per prompt, so several candidates cannot exist.
	prompt string
	// content is the id of the turn's first text, thinking or tool_call entry, with a
	// subagent invocation keyed on its delegate uuid rather than its entry id. Rule
	// two's key, and it needs no session scope: a content id is a uuid KAS minted in
	// exactly one session, so a match implies the session.
	content string
}

// pairTurns applies the two turn-pairing rules in order and answers both directions of
// the pairing. Rule one first, so a bind-keyed pair takes its partner before rule two
// can claim either side.
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
		// A reverted turn gets the ZERO keys, which is the whole of its exclusion from
		// pairing: pairOnKey already skips an empty key and firstByKey indexes non-empty
		// ones only, so no projected turn can pair with a turn the rewind hid and none
		// can resurrect one.
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

// projectedTurnKeys is each projected turn's two keys. A projected turn carries no
// bind, so rule one's key is its turn_open's own prompt id.
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

// pairOnKey pairs on one key, leaving every turn either side already paired alone. A
// duplicate key on the projected side pairs NOTHING beyond the first: two turns keyed
// the same are indistinguishable, and inserting a turn is the recoverable answer.
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

// boundPromptID is the record id a turn's turn_bind names, and only when the bind was
// minted in the replayed session. That scope is what keeps the empty-turn retry
// straight: the empty turn and the retry both carry one prompt id in two sessions, and
// a replay of the second may pair only the retry.
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

// firstContentKey is rule two's key: the first text, thinking or tool_call entry's id,
// with a say's `#k` segment suffix removed so a record turn whose first segment the
// store split still keys as the say, and a subagent invocation keyed on the delegate
// uuid because the two sides' entry ids differ by KAS's `-sub-agent-start` suffix.
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

// invocationKey is a subagent invocation's pairing key, the delegate uuid its payload
// carries. Empty for an ordinary tool call, which pairs by entry id.
func invocationKey(e *marotte.Entry) string {
	var call marotte.EntryToolCall
	if json.Unmarshal(e.Payload, &call) != nil {
		return ""
	}
	return call.AgentSubtaskID
}

// keptTurn is a record turn no rule paired: emitted at its record position, untouched.
// It still passes through a copy, because renumber writes into the slice it is handed
// and the caller's record must not move under it.
func keptTurn(rec *RecordTurn) MergedTurn {
	return MergedTurn{Entries: slices.Clone(rec.Entries), Reverted: rec.Reverted}
}

// insertedTurn is a projected turn no rule paired, inserted whole. It keeps the turn id
// the projection's own generator minted, which is the server's, so every entry of it
// already carries it.
//
// Rule 3's two stamps apply to every entry of such a turn, because none of them has a
// record twin: a steer becomes `dropped, restart`, and the synthesized closer of
// section 5.3 lands on a turn whose replay ended without a turn_end.
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

// insertedEntry is one projected entry with no record twin, filed into `turn`.
//
// A projected-only steer is stamped `dropped, reason: restart`, never read and never
// not-known, and the record's ABSENCE is the proof: marotte persists a steer at its
// injection frame, synchronously, so a steer the record lacks is one this process never
// saw injected — while KAS persisted its text at send time and mints an empty steering
// buffer on a load, so the words come back through the replay although the model never
// read them as a steer.
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

// synthesizedCloser is the turn_close a turn neither side closed gets, appended last.
// The placeholder stop reason is what tells the next merge this closer is not a live
// observation, so KAS's own account of the turn may still replace it.
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

// setPayload re-marshals one entry's payload in place. A payload this package builds
// cannot fail to marshal, so a failure leaves the entry as it was and says so rather
// than dropping a row out of the transcript.
func setPayload(e *marotte.Entry, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("entry merge: could not marshal a merged payload",
			"turn", e.Turn, "entry", e.ID, "kind", e.Kind, "error", err)
		return
	}
	e.Payload = raw
}

// recordEntryIndex is the record turn's entries, keyed the four ways entry pairing
// asks about them.
type recordEntryIndex struct {
	byID map[string]int
	// bySay holds every segment of one say in seq order, because a projected entry with
	// id S pairs with EVERY record entry whose id is S or S#k: the record's segment
	// boundaries are its own and are never merged.
	bySay map[string][]int
	// byInvocation is the delegate uuid of each subagent invocation, the one tool_call
	// whose entry ids differ between the two sides.
	byInvocation map[string]int
	// turnOpen and turnClose are the two entries that pair BY KIND, one of each per
	// turn: both are server-minted on the record side and generator-minted on the
	// projected side, so an id rule could never pair them.
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

// add files one record entry under every key that can claim it. FIRST wins on every
// key: a duplicate id, a second turn_open and a second invocation of one delegate are
// all shapes the store should not hold, and claiming the earliest keeps the merge's
// output in record order.
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

// entryPairing is one projected entry's answer from the record index: the record
// entries it pairs with, or none.
type entryPairing struct {
	// group is every record index this projected entry claims, in seq order. One index
	// for every kind but a say, whose segments are all claimed by the one projected
	// entry that covers them.
	group []int
}

// mergeTurn folds one projected turn into its paired record turn. The record's turn id
// is kept and every emitted entry carries it, inserted or unioned.
func mergeTurn(rec *RecordTurn, proj *translate.ProjectedTurn) (MergedTurn, bool) {
	turn := rec.Entries[0].Turn
	idx := indexRecordEntries(rec.Entries)
	pairing := pairEntries(rec.Entries, proj.Entries, idx)

	// The walk is sequential over KAS order, so two or more consecutive projected-only
	// entries hanging off one paired predecessor are emitted in KAS order, each after
	// the previous one, rather than both at the same slot.
	head := max(idx.turnOpen, 0)
	queued := make(map[int][]int, len(proj.Entries))
	unioned := make(map[int]int, len(rec.Entries))
	after := head
	for j := range proj.Entries {
		if g := pairing[j].group; len(g) > 0 {
			for _, i := range g {
				unioned[i] = j
			}
			// A say's emitted position is its LAST segment, so a projected-only entry
			// following the say lands after the whole of it rather than inside it.
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

// pairEntries answers each projected entry's record twin, applying the four entry
// pairing rules and claiming each record entry at most once.
func pairEntries(rec, proj []marotte.Entry, idx recordEntryIndex) []entryPairing {
	c := entryClaims{
		claimed: make(map[int]bool, len(rec)),
		pairing: make([]entryPairing, len(proj)),
	}
	// resultTwin carries a paired invocation's result across, because the two sides'
	// result ids differ exactly as their call ids do and a result pairs THROUGH its
	// call. A result follows its call in KAS order, so the entry is always in by then.
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

// entryClaims accumulates the pairing, claiming each record entry at most once.
type entryClaims struct {
	claimed map[int]bool
	pairing []entryPairing
}

// claim records that projected entry j pairs with every record index in group, and
// does nothing at all when any of them is missing or already claimed: a partial group
// would pair a say against some of its own segments and leave the rest to be inserted
// as duplicates.
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

// indexOr is a map lookup answering -1 for a miss, so a caller can hand the result
// straight to claim.
func indexOr(m map[string]int, key string) int {
	if i, ok := m[key]; ok {
		return i
	}
	return -1
}

// warnUnpairedCompactions reports a projected compaction left unpaired while the record
// holds an unpaired one of its own. Both sides mint the id from the summary bytes, so
// that state means the two hold DIFFERENT summary bytes for one compaction — the
// auto-compaction corner the design leaves open — and the reader sees two compaction
// rows rather than one silently dropped.
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

// sayExcess is the text a paired say's LAST segment gains, and "" for every other
// entry and for every segment but the last.
//
// A projected text longer than the record's segments concatenated means the record
// missed deltas at the tail, which is where a crash loses them; a record longer than or
// equal to the projection's means nothing moves. Merging the segments themselves is
// what the design forbids, because that is what would move the steer this whole design
// exists to place.
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
	// The premise is that the projection saw the same bytes plus a tail; where it did
	// not, taking a tail by byte offset would splice unrelated text onto the record's
	// last segment, so the record's own text stands.
	if !strings.HasPrefix(whole, held) {
		slog.Debug("entry merge: a replayed say diverges from the record's segments, record kept",
			"turn", proj.Turn, "say", proj.ID, "record_bytes", len(held), "replayed_bytes", len(whole))
		return ""
	}
	return whole[len(held):]
}

// sayTextOf is a text or thinking entry's coalesced text. Both payloads carry one
// field under one name, so one decode serves both.
func sayTextOf(e *marotte.Entry) string {
	var say marotte.EntryText
	if json.Unmarshal(e.Payload, &say) != nil {
		return ""
	}
	return say.Text
}

// unionEntry merges one record entry with the projected entry that paired with it.
//
// The RECORD is the base and its position, id and turn are kept, so record-owned is the
// structural default: a field added to a payload later survives with no edit here. One
// rule with two halves decides the rest — a fact KAS observed wins where the record's
// copy is empty, and a fact only this process could observe is the record's.
func unionEntry(rec, proj *marotte.Entry, excess string) marotte.Entry {
	out := *rec
	// The record's lane is a live observation of the create frame and the projection's
	// is a replay of the same frame, so the record wins; a disagreement is worth a line
	// because the replayed invocation update carries no stamp at all, which is what
	// makes the order matter on exactly that entry.
	if out.Lane == "" {
		out.Lane = proj.Lane
	} else if proj.Lane != "" && proj.Lane != out.Lane {
		slog.Warn("entry merge: the replay files an entry in another lane than the record",
			"turn", out.Turn, "entry", out.ID, "record_lane", out.Lane, "replayed_lane", proj.Lane)
	}
	// Metadata; nothing reads it for order.
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
	// turn_open, tool_call and every record-only kind: the projection adds nothing the
	// record does not already state, so the payload passes through as it was written.
	return out
}

// unionSay appends the excess a longer projected say carried onto the record's LAST
// segment. Every other segment, and every say the projection did not extend, keeps the
// record's bytes verbatim.
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
	// The record's `aborted` is a close-time inference rather than an observation: KAS's
	// own word for a tool a crash interrupted is `failed` with a stated reason, and for
	// a cancel `failed` too, so a terminal status from the replay replaces it.
	if r.Status == marotte.ToolAborted && p.Status.Terminal() {
		r.Status = p.Status
	}
	// A settled record call has the fuller value, so each of these is filled only where
	// the record states nothing.
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
	// duration_ms, terminal_id and output_spans are the record's whatever it holds: KAS
	// stamps a call and its result with identical timestamps, so a projected duration is
	// noise, and the other two are live observations the replay does not carry.
	setPayload(out, r)
}

// unionSteer fills a steer entry the record wrote from a cleared id alone.
//
// state, origin, reason and resends are the RECORD's without exception. KAS's log does
// not say whether the model consumed a steer and the live frame does, so the projection's
// empty state must never overwrite a record `read` or `dropped`; reason has one writer,
// the insertion rule, so a steer with a record twin was seen by a live process and its
// record reason stands; and resends is a fact the sender recorded, which KAS's replay
// never carries, so the projection's absence must not clear it.
func unionSteer(out, proj *marotte.Entry) {
	var r, p marotte.EntrySteer
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	// The cleared-id case: KAS persisted the note's text at send time and this process
	// received a clear carrying none, which is the divergence the text-less entry states.
	r.Text = cmp.Or(r.Text, p.Text)
	r.Severity = cmp.Or(r.Severity, p.Severity)
	setPayload(out, r)
}

// unionCompaction fills a summary the record lost. The two ids paired on the same
// bytes, so the sides agree wherever both are present.
func unionCompaction(out, proj *marotte.Entry) {
	var r, p marotte.EntryCompaction
	if json.Unmarshal(out.Payload, &r) != nil || json.Unmarshal(proj.Payload, &p) != nil {
		return
	}
	r.Summary = cmp.Or(r.Summary, p.Summary)
	setPayload(out, r)
}

// unionTurnClose replaces the store's PLACEHOLDER closer with KAS's own account, and
// leaves a closer a process wrote alone.
//
// The five conclusion fields travel as a unit, because a per-field union yields a
// completed turn carrying a cancellation sentence. model, refusal, changed_files and
// code_references stay the record's whichever closer wins: the replay carries no model
// id and no refusal meta, so taking them from the projection would erase them.
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
	r.Truncated = p.Truncated
	r.Credits = p.Credits
	r.ElapsedMs = p.ElapsedMs
	setPayload(out, r)
}

// sameEntry reports whether the merge left one record entry alone.
//
// Seq is excluded because renumber answers for it, and the payload is compared through
// the encoder rather than byte for byte: the log persists with one marshal of the same
// type, so an untouched payload is byte-identical, while a payload the union re-marshalled
// is canonical bytes of the same document.
func sameEntry(rec, out *marotte.Entry) bool {
	return rec.ID == out.ID && rec.Turn == out.Turn && rec.Lane == out.Lane &&
		rec.Kind == out.Kind && rec.Ts == out.Ts && sameRawJSON(rec.Payload, out.Payload)
}

// renumber assigns each turn's seq from 0 and each turn_open's n from 1 by turn order,
// and reports whether anything moved. A renumber is itself a change: turn_count reads
// the newest n, and a client detects a missed frame as a hole in seq.
//
// The n runs over the SURVIVING turns only and a reverted turn keeps whatever n it had:
// under §2.3 a hidden turn's ordinal is a coordinate no reader reads, while every reader
// of an n reads it off the surviving view, so numbering a hidden turn into that sequence
// would put it in a surviving turn's slot. seq is per turn and is assigned to every one
// of them, hidden or not, because the rewrite writes each group contiguously from 0.
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

// renumberOpen gives a turn_open the ordinal n and reports whether it moved; any other
// kind, or an undecodable payload, is left alone.
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

// sameRawJSON reports whether two raw JSON values encode the same document as the
// ENCODER would write them: the log persists compact wire bytes and a replay's copy
// can differ in whitespace and HTML escaping, so json.Marshal on a RawMessage, which
// compacts and escapes in one idempotent call, is the comparison. A marshal error
// falls back to a byte comparison, which errs toward writing.
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
