package agent

// The run-attention ledger: per leased run, how it stands (workflow.Health) and which of its state entries its
// launching chat is owed; per launching chat, an outbox that wakes the chat through the notify door once per state
// entry and re-delivers, on a backoff, a wake whose carrying turn did not settle. In memory only: a restart re-derives
// every wake from the boot pass's reads.

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

const (
	// attentionCoalesce is how long an outbox waits after its first owed item, so a burst (a boot pass over many runs,
	// the seats of one parallel throttling together) is one wake.
	attentionCoalesce = 5 * time.Second
	// attentionBackoffBase doubles per failed delivery up to attentionBackoffCap, which then holds while an item is
	// owed: waiting longer does not raise the next turn's odds after a throttle, so the cap bounds the cost.
	attentionBackoffBase = 60 * time.Second
	attentionBackoffCap  = 960 * time.Second
	// attentionRenudge is when an unanswered question is re-nudged: 186 of 197 parent answers came within it.
	attentionRenudge = 10 * time.Minute
	// attentionUnknownAfter outlasts a boot pass's reads and an engine restart's failed ones.
	attentionUnknownAfter = 10 * time.Minute
	attentionTickEvery    = 60 * time.Second
	// attentionWarnAt is the re-delivery, 31 minutes after the first failure, whose own failure is logged at Warn.
	attentionWarnAt     = 5
	attentionMaxLines   = 20
	attentionLineBytes  = 300
	attentionQuoteBytes = 120
)

const wakeLead = "[marotte] marotte (automated, not the user): "

// The two ends of an attention entry that are no turn's outcome (effAttentionClosed).
const (
	attnLost      marotte.TurnOutcome = "lost"
	attnDiscarded marotte.TurnOutcome = "discarded"
)

// attentionSettles reports an outcome after which the chat was told: the agent read the wake, or the person acted on
// the chat. Every other end keeps the carried items owed.
func attentionSettles(o marotte.TurnOutcome) bool {
	switch o {
	case marotte.TurnOutcomeCompleted, marotte.TurnOutcomeCancelled, marotte.TurnOutcomeRefused, attnDiscarded:
		return true
	}
	return false
}

// attentionInput is the ask cell's newest read of a run.
type attentionInput struct {
	reply      workflow.Inspect
	workflowID string
	asks       []workflow.Ask
	// seq orders the snapshots of every cell (runAsks.attnSeq), so an older one handed over late is dropped; zero for a
	// run with no cell.
	seq        uint64
	reachable  bool
	readFailed bool
}

// attnLease is what the lease store and the run log hold of a leased run.
type attnLease struct {
	stoppedAt time.Time
	steps     attnSteps
	chat      marotte.ChatID
	name      string
	healTry   int
}

// attnEntry is where an attention entry stands on its chat's run list.
type attnEntry uint8

const (
	// attnEntryWaiting: pending or paused on the list; a notify from marotte replaces its text.
	attnEntryWaiting attnEntry = iota
	// attnEntryUnread: kiro-cli holds it as a steer the agent has not read; a notify adds nothing.
	attnEntryUnread
	// attnEntryCarried: a turn took it, as its own prompt or as a steer it read; that turn's close is its end.
	attnEntryCarried
	// attnEntryGone: the list neither holds it nor has a turn carrying it, so no close will come for it.
	attnEntryGone
)

// attentionHost is the run host as the ledger reads and delivers through it; every call runs with no ledger lock held.
type attentionHost interface {
	attentionLease(workflowID string) (attnLease, bool)
	attentionCell(workflowID string) (attentionInput, bool)
	attentionEntry(chat marotte.ChatID, entryID string) attnEntry
	attentionNotify(ctx context.Context, chat marotte.ChatID, text string) (command.NotifyReceipt, error)
	attentionGo(work func()) bool
	// attentionAfter runs fn after d unless the runtime drains first.
	attentionAfter(d time.Duration, fn func()) *time.Timer
}

// attnGrade is a step's newest unfinished grade. n is its path's newest turn ordinal when it was graded (attnTurn).
type attnGrade struct {
	at         time.Time
	kind       workflow.UnfinishedKind
	turnID     string
	n          int
	told       bool
	superseded bool
}

type attnRun struct {
	healingSince time.Time
	stoppedAt    time.Time
	grades       map[string]*attnGrade
	// seen is each state entry's first sight in this process, by its base key.
	seen map[string]time.Time
	// delivered is, per slot, the key of the state entry a settled wake carried.
	delivered map[string]string
	steps     attnSteps
	in        attentionInput
	chat      marotte.ChatID
	name      string
	health    workflow.RunHealth
	healTry   int
}

// attnItem is one state entry a launching chat is owed.
type attnItem struct {
	dueAt time.Time
	since time.Time
	step  *workflow.StepHealth
	key   string
	slot  string
	state workflow.HealthState
}

type attnCarry struct {
	workflowID, slot, key string
	grade                 bool
}

// attnDelivery is one wake on a chat's run list and the state entries its text carries.
type attnDelivery struct {
	// goneAt is when a sweep first found the entry gone (attnEntryGone).
	goneAt  time.Time
	entryID string
	carried []attnCarry
	frozen  []uint64
}

// attnFrozen is the line of an ended run's unfinished step, which no recompute can reach.
type attnFrozen struct {
	dueAt time.Time
	line  string
	id    uint64
}

type attnOutbox struct {
	nextAt    time.Time
	notBefore time.Time
	inflight  map[string]*attnDelivery
	// early holds closes that arrived, while a Notify was out, for an entry the ledger does not hold yet.
	early map[string]marotte.TurnOutcome
	// owed is every item the chat was owed at the last classification, by run and key: a new one resets the backoff.
	owed map[string]bool
	// replacing is the waiting entry the Notify out is replacing; replaceClosed says its close came meanwhile, so
	// the receipt naming it is not a delivery any close will end.
	replacing string
	frozen    []attnFrozen
	attempt   int
	// readSeq counts the chat's reads of an unread wake (read), so a hold decided on an older probe is not kept.
	readSeq       uint64
	sending       bool
	replaceClosed bool
	// followUp: an owed item waits while kiro-cli holds a wake unread; a read or the wake's end lifts it.
	followUp bool
	warned   bool
}

// runAttention's mutex is a leaf: no other lock is taken and nothing is called out while it is held.
type runAttention struct {
	host      attentionHost
	now       func() time.Time
	runs      map[string]*attnRun
	boxes     map[marotte.ChatID]*attnOutbox
	timer     *time.Timer
	timerAt   time.Time
	frozenSeq uint64
	mu        sync.Mutex
}

func newRunAttention() *runAttention {
	return &runAttention{now: time.Now, runs: map[string]*attnRun{}, boxes: map[marotte.ChatID]*attnOutbox{}}
}

// loop ticks until done closes: the time-driven rules and any due delivery.
func (a *runAttention) loop(done <-chan struct{}) {
	t := time.NewTicker(attentionTickEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			a.tick()
		}
	}
}

// tick re-evaluates every run from the facts marotte holds, no engine call, ends the wakes whose entries are gone,
// and delivers every due outbox.
func (a *runAttention) tick() {
	if a == nil || a.host == nil {
		return
	}
	a.mu.Lock()
	ids := slices.Sorted(maps.Keys(a.runs))
	a.mu.Unlock()
	for _, id := range ids {
		a.recompute(id)
	}
	a.sweep()
	a.deliverDue()
}

// sweep ends, as lost, every wake in flight whose entry two probes at least attentionCoalesce apart found gone: no
// close will come for it, and while it stays in flight the items it carries are never sent again. The gap lets a
// close already on its way land first.
func (a *runAttention) sweep() {
	a.mu.Lock()
	probes := map[marotte.ChatID][]string{}
	for chat, b := range a.boxes {
		for id := range b.inflight {
			probes[chat] = append(probes[chat], id)
		}
	}
	a.mu.Unlock()
	for chat, ids := range probes {
		gone := map[string]bool{}
		for _, id := range ids {
			gone[id] = a.host.attentionEntry(chat, id) == attnEntryGone
		}
		a.mu.Lock()
		now, b := a.now(), a.boxLocked(chat)
		for id, isGone := range gone {
			d := b.inflight[id]
			switch {
			case d == nil:
			case !isGone:
				d.goneAt = time.Time{}
			case d.goneAt.IsZero():
				d.goneAt = now
			case now.Sub(d.goneAt) >= attentionCoalesce:
				slog.Warn("run attention: a wake left its chat's list with no end; it is re-delivered", "chat_id", chat,
					"entry", id)
				a.closedLocked(chat, b, id, attnLost)
			}
		}
		a.scheduleLocked(chat, now)
		a.mu.Unlock()
	}
}

// observe classifies a run from in, the cell's newest read, with the lease and run log facts.
func (a *runAttention) observe(in *attentionInput) {
	if a == nil || a.host == nil {
		return
	}
	l, held := a.host.attentionLease(in.workflowID)
	if !held {
		a.forget(in.workflowID)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.runLocked(in.workflowID)
	if in.seq != 0 && in.seq < r.in.seq {
		return
	}
	r.in, r.chat, r.name, r.stoppedAt, r.steps, r.healTry = *in, l.chat, l.name, l.stoppedAt, l.steps, l.healTry
	if in.reply.OK && in.reply.State != nil && in.reply.State.Status != string(marotte.RunStatusPaused) {
		r.healingSince = time.Time{}
	}
	a.classifyLocked(r)
}

// recompute classifies a run again from the cell's held read.
func (a *runAttention) recompute(workflowID string) {
	if a == nil || a.host == nil {
		return
	}
	in, ok := a.host.attentionCell(workflowID)
	if !ok {
		in = attentionInput{workflowID: workflowID}
	}
	a.observe(&in)
}

// healing marks a run marotte's heal or restart sweep is resuming, try being the resume's number.
func (a *runAttention) healing(workflowID string, try int) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.runLocked(workflowID)
	if r.healingSince.IsZero() {
		r.healingSince = a.now()
	}
	r.healTry = try
	a.classifyLocked(r)
}

// healed clears the mark: the resume did not take the run.
func (a *runAttention) healed(workflowID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runs[workflowID]
	if !ok || r.healingSince.IsZero() {
		return
	}
	r.healingSince = time.Time{}
	a.classifyLocked(r)
}

// attnGradeOf is a step's unfinished grade as the grade's caller hands it over: the lease node_complete held (chat and
// name, the only record left of an ended run), whether the run is known to have ended, and newest, the path's newest
// turn in the run log, open or closed, once the graded one closed.
type attnGradeOf struct {
	grade    *attnGrade
	chat     marotte.ChatID
	name     string
	nodePath string
	newest   attnTurn
	ended    bool
}

// grade records a step's unfinished grade. An ended run's grade is a frozen line at once.
func (a *runAttention) grade(workflowID string, in *attnGradeOf) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g := in.grade
	g.at = a.now()
	if in.ended {
		if in.chat != "" {
			a.freezeLocked(in.chat, frozenLine(workflowID, in.name, nodeOfPath(in.nodePath), g, g.at))
		}
		return
	}
	r := a.runLocked(workflowID)
	if r.grades == nil {
		r.grades = map[string]*attnGrade{}
	}
	// A newer turn that opened before the grade came supersedes it at once; else a later turn is one past its ordinal.
	g.n, g.superseded = in.newest.n, in.newest.id != "" && g.turnID != "" && in.newest.id != g.turnID
	r.grades[in.nodePath] = g
	a.classifyLocked(r)
}

// nodeOfPath is the node id a step PathKey ends with.
func nodeOfPath(path string) string {
	if parts, err := keyenc.Split(path); err == nil && len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

// forget drops a run whose lease ended. Its still-owed unfinished steps become frozen lines with the finished-run
// advice, since a step in the run's last node is graded at or after its end.
func (a *runAttention) forget(workflowID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runs[workflowID]
	if !ok {
		return
	}
	delete(a.runs, workflowID)
	if r.chat == "" {
		return
	}
	b := a.boxLocked(r.chat)
	maps.DeleteFunc(b.owed, func(k string, _ bool) bool { return strings.HasPrefix(k, workflowID+"\x01") })
	for _, it := range itemsOf(r, a.now()) {
		if it.state != workflow.HealthUnfinishedStep || it.step == nil {
			continue
		}
		if g := r.grades[it.slot]; g != nil {
			a.freezeLocked(r.chat, frozenLine(workflowID, r.name, it.step.NodeID, g, a.now()))
		}
	}
}

func (a *runAttention) runLocked(workflowID string) *attnRun {
	r, ok := a.runs[workflowID]
	if !ok {
		r = &attnRun{in: attentionInput{workflowID: workflowID}, seen: map[string]time.Time{}, delivered: map[string]string{}}
		a.runs[workflowID] = r
	}
	return r
}

// classifyLocked runs workflow.Health over what the ledger holds of r, dates each state entry's first sight, clears
// the delivered key of every slot that went healthy, and schedules the chat's outbox. A grade is superseded once the
// run log holds a turn on its path, open or already closed, opened after the graded one.
func (a *runAttention) classifyLocked(r *attnRun) {
	now := a.now()
	f := workflow.HealthFacts{
		AutoResumeStoppedAt: r.stoppedAt, HealingSince: r.healingSince, HealTry: r.healTry,
		Asks: r.in.asks, Reachable: r.in.reachable, ReadFailed: r.in.readFailed, Launched: r.chat != "",
		Steps: map[string]workflow.StepFacts{},
	}
	if s := r.in.reply.State; s != nil {
		f.InterruptedPause = resumablePause(s.PauseReason, nil)
	}
	maps.Copy(f.Steps, r.steps.live)
	for path, g := range r.grades {
		sf := f.Steps[path]
		sf.Grade = &workflow.StepGrade{
			At: g.at, Kind: g.kind, TurnID: g.turnID, Told: g.told,
			Superseded: g.superseded || r.steps.newest[path].n > g.n,
		}
		f.Steps[path] = sf
	}
	r.health = workflow.Health(r.in.reply, &f, now)
	touched := map[string]bool{}
	slots := map[string]bool{}
	for _, s := range slotsOf(&r.health) {
		base := baseKey(s.slot, s.state, s.disc)
		touched[base] = true
		slots[s.slot] = true
		if _, ok := r.seen[base]; !ok {
			r.seen[base] = now
		}
	}
	maps.DeleteFunc(r.seen, func(k string, _ time.Time) bool { return !touched[k] })
	maps.DeleteFunc(r.delivered, func(slot, _ string) bool { return !slots[slot] })
	if r.chat != "" {
		a.noteOwedLocked(r, now)
		a.scheduleLocked(r.chat, now)
	}
}

// noteOwedLocked records r's owed items on its chat's outbox; an item owed for the first time puts a re-delivery
// backoff back at its first wait.
func (a *runAttention) noteOwedLocked(r *attnRun, now time.Time) {
	b := a.boxLocked(r.chat)
	prefix := r.in.workflowID + "\x01"
	owedNow := map[string]bool{}
	for _, it := range itemsOf(r, now) {
		k := prefix + it.key
		owedNow[k] = true
		if !b.owed[k] && !b.nextAt.IsZero() {
			b.attempt, b.nextAt = 1, earlier(b.nextAt, now.Add(attentionBackoffBase))
		}
	}
	maps.DeleteFunc(b.owed, func(k string, _ bool) bool { return strings.HasPrefix(k, prefix) && !owedNow[k] })
	maps.Copy(b.owed, owedNow)
}

// attnSlot is one non-healthy slot of a run: a step's, or the run's own ("").
type attnSlot struct {
	step  *workflow.StepHealth
	slot  string
	disc  string
	state workflow.HealthState
}

func slotsOf(h *workflow.RunHealth) []attnSlot {
	var out []attnSlot
	if h.Lead < 0 && h.State != workflow.HealthHealthy {
		out = append(out, attnSlot{slot: "", state: h.State, disc: h.Key})
	}
	for i := range h.Steps {
		if st := &h.Steps[i]; st.State != workflow.HealthHealthy {
			out = append(out, attnSlot{step: st, slot: st.PathKey, state: st.State, disc: st.Key})
		}
	}
	return out
}

func baseKey(slot string, state workflow.HealthState, disc string) string {
	return slot + "\x00" + string(state) + "\x00" + disc
}

// itemsOf is what r's launching chat is owed now: no item while the run is in an owner state; for an unreadable run
// its own slot alone, after attentionUnknownAfter; else each step's waking state and the run's own engine pause.
func itemsOf(r *attnRun, now time.Time) []attnItem {
	h := &r.health
	if r.chat == "" || h.Owner {
		return nil
	}
	var out []attnItem
	for _, s := range slotsOf(h) {
		if h.State == workflow.HealthUnknown && s.step != nil {
			continue
		}
		if it, owed := itemOf(r, &s, now); owed {
			out = append(out, it)
		}
	}
	return out
}

// itemOf is slot s's item, owed when its state wakes, it is due (a state that wakes at entry is due at once) and its
// key was not delivered to the slot.
func itemOf(r *attnRun, s *attnSlot, now time.Time) (attnItem, bool) {
	base := baseKey(s.slot, s.state, s.disc)
	seen := r.seen[base]
	it := attnItem{key: base, slot: s.slot, state: s.state, step: s.step, since: seen, dueAt: seen.Add(attentionCoalesce)}
	if s.disc == "" {
		it.key += stamp(seen)
	}
	switch {
	case s.step != nil && !s.step.Since.IsZero():
		it.since = s.step.Since
	case s.step == nil && !r.health.Since.IsZero():
		it.since = r.health.Since
	}
	timed := true
	switch s.state {
	case workflow.HealthParkedSilently, workflow.HealthEnginePaused, workflow.HealthUnfinishedStep:
		timed = false
	case workflow.HealthHung:
		it.dueAt = s.step.LastAt.Add(workflow.DefaultHungAfter)
	case workflow.HealthWaitingOnParent:
		it.key, it.dueAt = base+"\x00renudge", seen.Add(attentionRenudge)
	case workflow.HealthUnknown:
		it.dueAt = seen.Add(attentionUnknownAfter)
	default:
		return attnItem{}, false
	}
	if r.delivered[s.slot] == it.key || (timed && now.Before(it.dueAt)) {
		return attnItem{}, false
	}
	return it, true
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (a *runAttention) boxLocked(chat marotte.ChatID) *attnOutbox {
	b, ok := a.boxes[chat]
	if !ok {
		b = &attnOutbox{inflight: map[string]*attnDelivery{}, early: map[string]marotte.TurnOutcome{}, owed: map[string]bool{}}
		a.boxes[chat] = b
	}
	return b
}

// runOwed is one run's owed items no other wake in flight carries.
type runOwed struct {
	r     *attnRun
	id    string
	items []attnItem
}

// unsentLocked is what chat is owed that no wake in flight carries, except the waiting wake replace names, whose text
// a new wake replaces.
func (a *runAttention) unsentLocked(chat marotte.ChatID, b *attnOutbox, replace string, now time.Time) ([]runOwed, []attnFrozen) {
	carried, held := carriedBy(b, replace)
	var runs []runOwed
	for _, id := range slices.Sorted(maps.Keys(a.runs)) {
		r := a.runs[id]
		if r.chat != chat {
			continue
		}
		items := slices.DeleteFunc(itemsOf(r, now), func(it attnItem) bool { return carried[id+"\x01"+it.key] })
		if len(items) > 0 {
			runs = append(runs, runOwed{r: r, id: id, items: items})
		}
	}
	var frozen []attnFrozen
	if b != nil {
		frozen = slices.DeleteFunc(slices.Clone(b.frozen), func(f attnFrozen) bool { return held[f.id] })
	}
	return runs, frozen
}

// carriedBy is the run items, by run and key, and the frozen lines the outbox's wakes in flight carry, except.
func carriedBy(b *attnOutbox, except string) (carried map[string]bool, held map[uint64]bool) {
	carried, held = map[string]bool{}, map[uint64]bool{}
	if b == nil {
		return carried, held
	}
	for id, d := range b.inflight {
		if id == except {
			continue
		}
		for _, c := range d.carried {
			carried[c.workflowID+"\x01"+c.key] = true
		}
		for _, f := range d.frozen {
			held[f] = true
		}
	}
	return carried, held
}

// dueLocked is when chat's outbox delivers next, false for never: on its re-delivery backoff, else once its earliest
// owed item not carried by a wake in flight is due, never before notBefore.
func (a *runAttention) dueLocked(chat marotte.ChatID, now time.Time) (time.Time, bool) {
	b := a.boxes[chat]
	if b != nil && (b.sending || (b.followUp && len(b.inflight) > 0)) {
		return time.Time{}, false
	}
	runs, frozen := a.unsentLocked(chat, b, "", now)
	var due time.Time
	soonest := func(at time.Time) {
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	for _, ro := range runs {
		for _, it := range ro.items {
			soonest(it.dueAt)
		}
	}
	for _, f := range frozen {
		soonest(f.dueAt)
	}
	if due.IsZero() {
		return time.Time{}, false
	}
	if b != nil {
		if !b.nextAt.IsZero() {
			due = b.nextAt
		}
		due = later(due, b.notBefore)
	}
	return due, true
}

// scheduleLocked arms the ledger's one timer for chat's next delivery, when it is sooner than the one armed.
func (a *runAttention) scheduleLocked(chat marotte.ChatID, now time.Time) {
	due, ok := a.dueLocked(chat, now)
	if b := a.boxes[chat]; !ok && b != nil && !b.sending && len(b.inflight) == 0 {
		// Nothing is owed any more: a later item starts over at the first wait.
		b.attempt, b.nextAt, b.warned = 0, time.Time{}, false
	}
	if !ok || a.host == nil || (a.timer != nil && !a.timerAt.After(due)) {
		return
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timerAt = due
	a.timer = a.host.attentionAfter(max(due.Sub(now), 0), func() {
		a.mu.Lock()
		a.timer = nil
		a.mu.Unlock()
		a.deliverDue()
	})
}

// deliverDue starts the delivery of every outbox that is due, each on its own goroutine.
func (a *runAttention) deliverDue() {
	if a == nil || a.host == nil {
		return
	}
	a.mu.Lock()
	now := a.now()
	var due []marotte.ChatID
	for _, chat := range a.chatsLocked() {
		if at, ok := a.dueLocked(chat, now); ok && !now.Before(at) {
			a.boxLocked(chat).sending = true
			due = append(due, chat)
		} else if ok {
			a.scheduleLocked(chat, now)
		}
	}
	a.mu.Unlock()
	for _, chat := range due {
		if !a.host.attentionGo(func() { a.deliver(chat) }) {
			a.mu.Lock()
			a.boxLocked(chat).sending = false
			a.mu.Unlock()
		}
	}
}

// chatsLocked is every chat a run names or an outbox still owes, sorted.
func (a *runAttention) chatsLocked() []marotte.ChatID {
	chats := map[marotte.ChatID]bool{}
	for _, r := range a.runs {
		if r.chat != "" {
			chats[r.chat] = true
		}
	}
	for chat, b := range a.boxes {
		if len(b.frozen) > 0 || !b.nextAt.IsZero() {
			chats[chat] = true
		}
	}
	return slices.Sorted(maps.Keys(chats))
}

// deliver wakes chat with what it is owed, recomputed first: one Notify, which replaces the text of an in-flight wake
// still waiting on the list or else places a new one. While kiro-cli holds a wake unread it waits for the read
// (read) or the wake's end instead, since a notify then adds nothing.
func (a *runAttention) deliver(chat marotte.ChatID) {
	a.mu.Lock()
	var ids []string
	for id, r := range a.runs {
		if r.chat == chat {
			ids = append(ids, id)
		}
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.recompute(id)
	}
	a.mu.Lock()
	b := a.boxLocked(chat)
	inflight := slices.Sorted(maps.Keys(b.inflight))
	seq := b.readSeq
	a.mu.Unlock()
	replace, unread := "", false
	for _, id := range inflight {
		switch a.host.attentionEntry(chat, id) {
		case attnEntryWaiting:
			replace = cmp.Or(replace, id)
		case attnEntryUnread:
			unread = true
		case attnEntryCarried, attnEntryGone:
		}
	}
	a.mu.Lock()
	b = a.boxLocked(chat)
	if unread {
		b.sending = false
		a.holdLocked(chat, b, seq)
		a.mu.Unlock()
		return
	}
	if b.inflight[replace] == nil {
		replace = ""
	}
	text, d := a.buildLocked(chat, b, replace)
	if text == "" {
		b.sending = false
		if len(b.inflight) == 0 {
			b.attempt, b.nextAt, b.warned = 0, time.Time{}, false
		}
		a.mu.Unlock()
		slog.Debug("run attention: nothing is owed to the chat any more", "chat_id", chat)
		return
	}
	b.replacing, b.replaceClosed = replace, false
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), clientRequestBudget)
	rcpt, err := a.host.attentionNotify(ctx, chat, text)
	cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	b = a.boxLocked(chat)
	if err != nil {
		a.refusedLocked(chat, b, d, err)
	} else {
		a.placedLocked(chat, b, d, rcpt, seq)
	}
	b.sending, b.replacing, b.replaceClosed = false, "", false
	clear(b.early)
	a.scheduleLocked(chat, a.now())
}

// holdLocked has chat's owed items wait while kiro-cli holds a wake unread, unless the chat read one since seq.
func (a *runAttention) holdLocked(chat marotte.ChatID, b *attnOutbox, seq uint64) {
	if b.readSeq == seq {
		b.followUp = true
		return
	}
	a.scheduleLocked(chat, a.now())
}

// read is the chat's agent reading a wake kiro-cli held as a steer: what waited on it can go now.
func (a *runAttention) read(chat marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.boxLocked(chat)
	b.readSeq++
	b.followUp = false
	a.scheduleLocked(chat, a.now())
}

// placedLocked records a Notify's receipt: a new wake, a replaced one's new carried keys, or a hold while the earlier
// wake is unread. A receipt naming a replaced entry whose close already came records nothing: that close settled or
// backed off the earlier text, and the next build recomputes what is still owed.
func (a *runAttention) placedLocked(chat marotte.ChatID, b *attnOutbox, d *attnDelivery, rcpt command.NotifyReceipt, seq uint64) {
	if rcpt.MessageID == b.replacing && b.replaceClosed {
		slog.Debug("run attention: the wake a notify replaced ended first", "chat_id", chat, "entry", rcpt.MessageID)
		return
	}
	b.nextAt = time.Time{}
	switch have := b.inflight[rcpt.MessageID]; {
	case rcpt.Held:
		a.holdLocked(chat, b, seq)
		return
	case have != nil:
		have.carried, have.frozen = d.carried, d.frozen
	default:
		d.entryID = rcpt.MessageID
		b.inflight[d.entryID] = d
	}
	if o, ok := b.early[rcpt.MessageID]; ok {
		delete(b.early, rcpt.MessageID)
		a.closedLocked(chat, b, rcpt.MessageID, o)
	}
}

// refusedLocked acts on a Notify refusal: a chat that is gone drops what it is owed, and any other refusal keeps it
// owed on the backoff.
func (a *runAttention) refusedLocked(chat marotte.ChatID, b *attnOutbox, d *attnDelivery, err error) {
	runs := carriedRuns(d)
	switch command.ClassifyNotifyError(err) {
	case command.NotifyUnknownChat, command.NotifySessionClosed, command.NotifyBotClosed:
		slog.Info("run attention: the launching chat is closed or gone; its wake is dropped",
			"chat_id", chat, "workflow_ids", runs, "error", err)
		a.markDeliveredLocked(b, d)
		return
	case command.NotifyBadText:
		slog.Error("run attention: the notify door refused a wake's text; the wake is dropped", "chat_id", chat,
			"workflow_ids", runs, "error", err)
		a.markDeliveredLocked(b, d)
		return
	case command.NotifyFailed:
		slog.Error("run attention: a wake could not be placed", "chat_id", chat, "workflow_ids", runs, "error", err)
	case command.NotifyRetryLater:
		slog.Debug("run attention: the chat cannot take a wake yet", "chat_id", chat, "error", err)
	}
	a.backoffLocked(chat, b, "", runs)
}

// closed is the end of an attention entry on chat's run list (effAttentionClosed).
func (a *runAttention) closed(chat marotte.ChatID, entryID string, outcome marotte.TurnOutcome) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.boxLocked(chat)
	if _, ok := b.inflight[entryID]; !ok {
		// Only the Notify out can name an entry the ledger does not hold yet.
		if b.sending && len(b.early) < attentionMaxLines {
			b.early[entryID] = outcome
		}
		return
	}
	a.closedLocked(chat, b, entryID, outcome)
	a.scheduleLocked(chat, a.now())
}

func (a *runAttention) closedLocked(chat marotte.ChatID, b *attnOutbox, entryID string, outcome marotte.TurnOutcome) {
	d := b.inflight[entryID]
	delete(b.inflight, entryID)
	if entryID == b.replacing {
		b.replaceClosed = true
	}
	if !attentionSettles(outcome) {
		slog.Info("run attention: a wake's turn did not settle; it is re-delivered", "chat_id", chat, "entry", entryID,
			"outcome", string(outcome), "attempt", b.attempt+1)
		b.followUp = false
		a.backoffLocked(chat, b, entryID, carriedRuns(d))
		return
	}
	slog.Debug("run attention: a wake settled", "chat_id", chat, "entry", entryID, "outcome", string(outcome))
	a.markDeliveredLocked(b, d)
	b.attempt, b.nextAt, b.warned, b.followUp = 0, time.Time{}, false, false
	b.notBefore = a.now().Add(attentionCoalesce)
	for _, r := range a.runs {
		if r.chat == chat {
			a.classifyLocked(r)
		}
	}
}

// markDeliveredLocked records every state entry d carried as told, and drops the frozen lines it carried.
func (a *runAttention) markDeliveredLocked(b *attnOutbox, d *attnDelivery) {
	for _, c := range d.carried {
		r, ok := a.runs[c.workflowID]
		if !ok {
			continue
		}
		r.delivered[c.slot] = c.key
		if g := r.grades[c.slot]; c.grade && g != nil {
			g.told = true
		}
	}
	b.frozen = slices.DeleteFunc(b.frozen, func(f attnFrozen) bool { return slices.Contains(d.frozen, f.id) })
}

// backoffLocked schedules a re-delivery: 60 s after the first failure, doubling to 960 s, then every 960 s while an
// item is owed. b.attempt counts the failed sends, so the fifth re-delivery's failure is the sixth.
func (a *runAttention) backoffLocked(chat marotte.ChatID, b *attnOutbox, entryID string, runs []string) {
	b.attempt++
	wait := min(attentionBackoffBase<<min(b.attempt-1, 5), attentionBackoffCap)
	b.nextAt = a.now().Add(wait)
	if b.attempt == attentionWarnAt+1 && !b.warned {
		b.warned = true
		slog.Warn("a launching chat has not taken a run-attention wake for 31 minutes", "chat_id", chat,
			"entry", entryID, "workflow_ids", runs)
	}
}

func carriedRuns(d *attnDelivery) []string {
	var out []string
	for _, c := range d.carried {
		if !slices.Contains(out, c.workflowID) {
			out = append(out, c.workflowID)
		}
	}
	return out
}

func (a *runAttention) freezeLocked(chat marotte.ChatID, line string) {
	a.frozenSeq++
	now := a.now()
	b := a.boxLocked(chat)
	b.frozen = append(b.frozen, attnFrozen{id: a.frozenSeq, line: line, dueAt: now.Add(attentionCoalesce)})
	a.scheduleLocked(chat, now)
}

// buildLocked is chat's wake text and what it carries: one line per run with an owed item, each run's highest by the
// step precedence, then the frozen ended-run lines. Items another wake in flight carries are left out, except those
// of the waiting wake replace names, whose text this one replaces. "" when nothing is owed.
func (a *runAttention) buildLocked(chat marotte.ChatID, b *attnOutbox, replace string) (string, *attnDelivery) {
	now := a.now()
	runs, frozen := a.unsentLocked(chat, b, replace, now)
	d := &attnDelivery{}
	lines := make([]string, 0, len(runs)+len(frozen))
	for _, ro := range runs {
		items := ro.items
		slices.SortStableFunc(items, func(x, y attnItem) int { return cmp.Compare(x.state.Precedence(), y.state.Precedence()) })
		for _, it := range items {
			grade := it.state == workflow.HealthUnfinishedStep
			d.carried = append(d.carried, attnCarry{workflowID: ro.id, slot: it.slot, key: it.key, grade: grade})
		}
		lines = append(lines, runLine(ro.id, ro.r, &items[0], len(items)-1, now))
	}
	for _, f := range frozen {
		d.frozen = append(d.frozen, f.id)
		lines = append(lines, f.line)
	}
	if len(lines) == 0 {
		return "", d
	}
	return wakeText(lines), d
}

// wakeText is the wake: its opening line, which counts the runs, then at most attentionMaxLines lines.
func wakeText(lines []string) string {
	var sb strings.Builder
	sb.WriteString(wakeLead)
	n := len(lines)
	if n == 1 {
		sb.WriteString("1 of your workflow runs needs attention.")
	} else {
		sb.WriteString(strconv.Itoa(n) + " of your workflow runs need attention.")
	}
	for _, l := range lines[:min(n, attentionMaxLines)] {
		sb.WriteString("\n" + l)
	}
	if n > attentionMaxLines {
		sb.WriteString("\nand " + strconv.Itoa(n-attentionMaxLines) + " more")
	}
	return sb.String()
}

// runLine is a run's line for its highest owed item: "- <id> (<name>): <state> since <time>: <why>", at most
// attentionLineBytes. Untrusted values are made one line with no context mention, and the line passes the notify
// door's own text check: a quote that fails it is dropped, and a line that still fails is cut to its head.
func runLine(id string, r *attnRun, it *attnItem, more int, now time.Time) string {
	head := "- " + id
	if name := attnClean(r.name, 64); name != "" {
		head += " (" + name + ")"
	}
	head += ": " + strings.ReplaceAll(string(it.state), "_", " ") + " since " + attnTime(it.since, now)
	why := r.health.Why
	quote := ""
	if st := it.step; st != nil {
		why = "step " + attnClean(st.NodeID, 64) + ": " + st.Why
		if st.State == workflow.HealthParkedSilently {
			quote = attnClean(st.Quote, attentionQuoteBytes)
		}
	}
	why = attnClean(why, attentionLineBytes)
	if more > 0 {
		why += "; " + strconv.Itoa(more) + " more " + plural(more, "step needs", "steps need") + " attention"
	}
	line := head + ": " + why
	if quote != "" {
		if withQuote := line + `; its last words: "` + quote + `"`; len(withQuote) <= attentionLineBytes &&
			command.CheckNotifyText(withQuote) == nil {
			return withQuote
		}
	}
	if len(line) > attentionLineBytes {
		line = textsafe.CapBytes(line, attentionLineBytes-3) + "..."
	}
	if command.CheckNotifyText(line) != nil {
		return head
	}
	return line
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// attnClean makes s one line of at most n bytes with no context-mention token.
func attnClean(s string, n int) string {
	s = strings.ReplaceAll(textsafe.SanitizeSingleLineBounded(s, n), "#[[", "#[ [")
	return strings.TrimSpace(s)
}

// attnTime is a time as a line names it: UTC, with the date when it is not today's.
func attnTime(t, now time.Time) string {
	if t.IsZero() {
		t = now
	}
	t, now = t.UTC(), now.UTC()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04") + " UTC"
	}
	return t.Format("2006-01-02 15:04") + " UTC"
}

// frozenLine is an ended run's unfinished step's line, with the finished-run advice: update_workflow can no longer
// replace a finished run's steps.
func frozenLine(workflowID, name, node string, g *attnGrade, now time.Time) string {
	head := "- " + workflowID
	if n := attnClean(name, 64); n != "" {
		head += " (" + n + ")"
	}
	head += ": unfinished step since " + attnTime(g.at, now) + ": step " + attnClean(node, 64) + ": "
	var why string
	switch g.kind {
	case workflow.UnfinishedRefused:
		why = "was declined by the model; the run has finished, so change its prompt or model and start a new run with run_workflow"
	case workflow.UnfinishedModelCallLimit:
		why = "stopped at its model-call limit; the run has finished, so start a new run with run_workflow that re-runs its work"
	default:
		why = "ended on an empty model answer; the run has finished, so start a new run with run_workflow that re-runs its work"
	}
	line := head + why
	if len(line) > attentionLineBytes {
		return textsafe.CapBytes(line, attentionLineBytes-3) + "..."
	}
	return line
}
