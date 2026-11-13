package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// One beat: presence itself moves at that granularity. A var so a test can shrink the wait rather than the window.
var deferPoll = liveness.Keepalive

// maxTabMarkers bounds the acked-marker copy; only a leak of tab closes reaches it, and an evicted marker costs at
// most one push of a later notice with nothing new to read.
const maxTabMarkers = 1024

// ledger holds one record per undelivered notice and profile tag; an ended record is deleted at once. Its clock is
// the presence table's, so the verdict and the TTL move together. Lock order: ledger, then presence;
// Service.mu is never taken under either.
type ledger struct {
	records map[recordKey]*noticeRecord
	// markers copies each tab's newest acked marker, so a notice raised after the ack covering it ends at raise.
	markers map[tabKey]tabMarker
	timer   *time.Timer
	seq     uint64
	mu      sync.Mutex
}

type recordKey struct{ tag, id string }

type tabKey struct {
	kind marotte.TabKind
	ref  string
}

type tabMarker struct {
	marker uint64
	seq    uint64
}

type recordState uint8

const (
	stateHeld recordState = iota
	stateOffered
)

type noticeRecord struct {
	expiresAt time.Time
	offeredAt time.Time
	tab       tabKey
	key       pushDebounceKey
	chatID    marotte.ChatID
	page      string
	// notice is the frame's payload, the whole notice with its id; payload is the push body, fitted to the cap.
	notice   marotte.NotificationPayload
	payload  []byte
	position uint64
	seq      uint64
	state    recordState
	hasTab   bool
	pushable bool
}

func (r *noticeRecord) ask() bool { return r.notice.Ask != "" }

type placement uint8

const (
	placeNone placement = iota
	placeHeld
	placeOffered
	placePushNow
)

// SetFrames sets the publisher of the page's notification frame, before the first Raise. Nil publishes nothing.
func (s *Service) SetFrames(publish func(marotte.ChatID, marotte.NotificationPayload)) {
	s.frames = publish
}

// Raise records n about chatID, whose tab's read position was position when it was raised: it offers the notice to
// one page per present profile, holds it for a profile that shows its tab or has no page, and returns the push
// fan-out to the profiles already gone, for the caller to run outside every lock. n is not modified.
func (s *Service) Raise(
	_ context.Context, chatID marotte.ChatID, n *marotte.NotificationPayload, position uint64,
) (deliver func(context.Context)) {
	notice := *n
	notice.ID, notice.ShowIn = newNoticeID(), nil
	slog.Debug("push: raise", "kind", string(notice.Kind))
	payload, ok := pushPayload(&notice)
	if !ok {
		return noDelivery
	}
	kind, ref, hasTab := notice.Tab()
	proto := noticeRecord{
		notice: notice, chatID: chatID, payload: payload, tab: tabKey{kind: kind, ref: ref}, hasTab: hasTab,
		key: debounceKey(notice.Kind, notice.PushSubject), position: position,
	}
	// A read notice must not stamp the debounce, and Service.mu is never taken under the ledger lock: the
	// marker is read before preflightSend and again under the placement hold, for an ack landing between.
	s.ledger.mu.Lock()
	read := s.ledger.readAt(&proto)
	s.ledger.mu.Unlock()
	if read {
		s.readAtRaise(&proto, s.peekSend(notice.Kind, notice.PushSubject))
		return noDelivery
	}
	v := s.preflightSend(notice.Kind, notice.PushSubject)
	proto.pushable = v.pushable
	s.ledger.mu.Lock()
	if s.ledger.readAt(&proto) {
		s.ledger.mu.Unlock()
		s.readAtRaise(&proto, v)
		return noDelivery
	}
	tags := s.noticeTags(v.subs)
	showIn, now := s.placeTags(tags, &proto, &v)
	s.armBeatLocked()
	s.ledger.mu.Unlock()
	if len(showIn) > 0 {
		s.publish(chatID, &notice, showIn)
	}
	if len(now) == 0 {
		return noDelivery
	}
	return func(ctx context.Context) { s.fanOut(ctx, now, payload, notice.Kind) }
}

func noDelivery(context.Context) {}

// readAtRaise counts a notice ended at raise as suppressed on each present profile it was decided for.
func (s *Service) readAtRaise(proto *noticeRecord, v pushVerdict) {
	s.countSuppressed(proto.notice.Kind, s.presentCount(s.noticeTags(v.subs)))
	slog.Debug("push: notice already read at raise", "kind", string(proto.notice.Kind))
}

// placeTags decides rec for each tag, records it where it waits, and returns the pages offered it and the
// subscriptions to push now. Caller holds the ledger lock.
func (s *Service) placeTags(
	tags []string, proto *noticeRecord, v *pushVerdict,
) (showIn []string, now []marotte.PushSubscription) {
	at := s.now()
	for _, tag := range tags {
		sub, subscribed := v.subs[tag]
		superseding := !proto.ask() && s.ledger.hasUndelivered(tag, proto.key)
		p, page := s.place(tag, proto, subscribed, v.debounced, superseding)
		if p == placeNone {
			continue
		}
		if !proto.ask() {
			s.ledger.supersede(tag, proto.key)
		}
		switch p {
		case placePushNow:
			now = append(now, sub)
			continue
		case placeOffered:
			showIn = append(showIn, page)
		}
		s.ledger.insert(tag, proto, p, page, at)
		s.countSuppressed(proto.notice.Kind, 1)
		slog.Debug("push: suppressed, profile present", "kind", string(proto.notice.Kind), "tag", tag)
	}
	return showIn, now
}

func (s *Service) presentCount(tags []string) int {
	n := 0
	for _, tag := range tags {
		if s.presence != nil && !s.presence.Gone(tag) {
			n++
		}
	}
	return n
}

// newNoticeID is 16 random bytes, base64url: 22 characters inside the [A-Za-z0-9_-]{1,64} grammar the routes check.
func newNoticeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// pushPayload is n as the service worker receives it, trimmed against the MARSHALED size: the JSON envelope and
// escaping count toward pushBodyCap, and push() drops an over-cap payload rather than truncating it.
func pushPayload(n *marotte.NotificationPayload) ([]byte, bool) {
	fit, truncated := fitToCap(n)
	if truncated {
		slog.Warn("push: payload too large, truncating", "bytes", len(n.Title)+len(n.Body), "cap", pushBodyCap)
	}
	payload, err := json.Marshal(fit)
	if err != nil {
		slog.Error("push: marshal payload", "error", err)
		return nil, false
	}
	return payload, true
}

// noticeTags is every tag a notice is decided for: those with a page row and those with a subscription, sorted so
// a frame's page order is stable.
func (s *Service) noticeTags(subs map[string]marotte.PushSubscription) []string {
	tags := make([]string, 0, len(subs))
	for tag := range subs {
		tags = append(tags, tag)
	}
	if s.presence != nil {
		tags = append(tags, s.presence.tagsWithPages()...)
	}
	slices.Sort(tags)
	return slices.Compact(tags)
}

// place decides one tag's arm for rec. A page showing the notice's tab holds it (that page acks within
// milliseconds); another live page is offered it; else the push channel decides. A debounced non-ask on a present
// tag is held only when it supersedes an undelivered record of its key, which then pushes the newest payload once.
func (s *Service) place(
	tag string, rec *noticeRecord, subscribed, debounced, superseding bool,
) (p placement, page string) {
	gone := s.presence == nil || s.presence.Gone(tag)
	if !gone {
		if rec.hasTab && s.presence.pageOnTab(tag, rec.tab.kind, rec.tab.ref) {
			return placeHeld, ""
		}
		if page, ok := s.presence.offerTarget(tag); ok {
			return placeOffered, page
		}
	}
	switch {
	case !subscribed || !rec.pushable:
		return placeNone, ""
	case gone && !debounced:
		return placePushNow, ""
	case gone:
		return placeNone, ""
	case !debounced || rec.ask() || superseding:
		return placeHeld, ""
	default:
		return placeNone, ""
	}
}

// readAt reports whether rec announces nothing the reader has not acked: a non-ask notice whose tab's stored
// marker is at or past its position. An ask and a position of 0 are never read by an ack. Caller holds mu.
func (l *ledger) readAt(rec *noticeRecord) bool {
	if rec.ask() || rec.position == 0 || !rec.hasTab {
		return false
	}
	m, ok := l.markers[rec.tab]
	return ok && m.marker >= rec.position
}

func (l *ledger) hasUndelivered(tag string, key pushDebounceKey) bool {
	for k, rec := range l.records {
		if k.tag == tag && rec.key == key && !rec.ask() {
			return true
		}
	}
	return false
}

// supersede ends tag's undelivered non-ask records of key: a newer notice of that kind and subject replaces them.
func (l *ledger) supersede(tag string, key pushDebounceKey) {
	for k, rec := range l.records {
		if k.tag == tag && rec.key == key && !rec.ask() {
			delete(l.records, k)
		}
	}
}

func (l *ledger) insert(tag string, proto *noticeRecord, p placement, page string, at time.Time) {
	if l.records == nil {
		l.records = make(map[recordKey]*noticeRecord)
	}
	l.seq++
	rec := *proto
	rec.seq, rec.expiresAt = l.seq, at.Add(ttlDuration(rec.notice.Kind))
	rec.state, rec.page, rec.offeredAt = stateHeld, "", time.Time{}
	if p == placeOffered {
		rec.state, rec.page, rec.offeredAt = stateOffered, page, at
	}
	l.records[recordKey{tag: tag, id: rec.notice.ID}] = &rec
}

func (s *Service) countSuppressed(kind marotte.PushKind, n int) {
	if counter, ok := s.suppressed[kind]; ok && n > 0 {
		counter.Add(uint64(n))
	}
}

func (s *Service) publish(chatID marotte.ChatID, n *marotte.NotificationPayload, showIn []string) {
	if s.frames == nil {
		return
	}
	frame := *n
	frame.ShowIn = showIn
	s.frames(chatID, frame)
}

func (s *Service) now() time.Time {
	if s.presence != nil {
		return s.presence.now()
	}
	return time.Now()
}

// armBeatLocked starts the beat when a record waits and none is armed. Caller holds the ledger lock.
func (s *Service) armBeatLocked() {
	if len(s.ledger.records) > 0 && s.ledger.timer == nil && s.lifetime.Err() == nil {
		s.ledger.timer = time.AfterFunc(deferPoll, s.beat)
	}
}

// pushGroup is one tag's due records of one kind and subject: the profile gets one push, the newest payload.
type pushGroup struct {
	sub     marotte.PushSubscription
	kind    marotte.PushKind
	payload []byte
	seq     uint64
}

type groupKey struct {
	tag string
	key pushDebounceKey
}

// beatPass is what one beat decided: the offers to publish and the pushes to send once the lock is released.
type beatPass struct {
	subs    map[string]marotte.PushSubscription
	groups  map[groupKey]*pushGroup
	offers  map[string][]string
	notices map[string]*noticeRecord
}

// beat re-decides every record: past its TTL it expires; on a tag now gone it is pushed; an offer unanswered for
// one alive window drops that page and is decided again for its tag. Re-arms itself while a record waits; the
// service lifetime ending stops the sends through the fan-out's context.
func (s *Service) beat() {
	if s.lifetime.Err() != nil {
		return
	}
	pass := beatPass{
		subs: s.subsByTag(), groups: make(map[groupKey]*pushGroup),
		offers: make(map[string][]string), notices: make(map[string]*noticeRecord),
	}
	s.ledger.mu.Lock()
	now := s.now()
	for k, rec := range s.ledger.records {
		s.redecide(&pass, k, rec, now)
	}
	s.ledger.timer = nil
	s.armBeatLocked()
	s.ledger.mu.Unlock()
	for id, pages := range pass.offers {
		slices.Sort(pages)
		s.publish(pass.notices[id].chatID, &pass.notices[id].notice, pages)
	}
	for gk, g := range pass.groups {
		slog.Debug("push: held notice released, profile gone", "kind", string(g.kind), "tag", gk.tag)
		s.fanOut(s.lifetime, []marotte.PushSubscription{g.sub}, g.payload, g.kind)
	}
}

// redecide is one record's beat. Caller holds the ledger lock.
func (s *Service) redecide(pass *beatPass, k recordKey, rec *noticeRecord, now time.Time) {
	if now.After(rec.expiresAt) {
		delete(s.ledger.records, k)
		slog.Debug("push: held notice expired unsent", "kind", string(rec.notice.Kind), "tag", k.tag)
		return
	}
	gone := s.presence.Gone(k.tag)
	timedOut := rec.state == stateOffered && now.Sub(rec.offeredAt) >= liveness.AliveWindow
	if !gone && !timedOut {
		return
	}
	if !gone {
		s.presence.dropPage(k.tag, rec.page)
	}
	sub, subscribed := pass.subs[k.tag]
	switch p, page := s.place(k.tag, rec, subscribed, false, false); p {
	case placeHeld:
		rec.state, rec.page = stateHeld, ""
	case placeOffered:
		rec.state, rec.page, rec.offeredAt = stateOffered, page, now
		pass.offers[rec.notice.ID] = append(pass.offers[rec.notice.ID], page)
		pass.notices[rec.notice.ID] = rec
	case placePushNow:
		delete(s.ledger.records, k)
		gk := groupKey{tag: k.tag, key: rec.key}
		if g, ok := pass.groups[gk]; !ok || rec.seq > g.seq {
			pass.groups[gk] = &pushGroup{sub: sub, payload: rec.payload, kind: rec.notice.Kind, seq: rec.seq}
		}
	case placeNone:
		delete(s.ledger.records, k)
	}
}

// subsByTag snapshots the subscriptions by their profile's tag.
func (s *Service) subsByTag() map[string]marotte.PushSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]marotte.PushSubscription, len(s.subs))
	for _, sub := range s.subs {
		out[tagOf(sub.Endpoint)] = sub
	}
	return out
}

// Shown ends the notice id on tag: a page of that profile showed it.
func (s *Service) Shown(tag, id string) {
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	delete(s.ledger.records, recordKey{tag: tag, id: id})
}

// Declined answers an offer of id on tag that the page's own gates refused: the device's gates are not the
// profile's push, so the record waits for the push when the notice is pushable and tag subscribed, else expires.
func (s *Service) Declined(tag, id string) {
	_, subscribed := s.subsByTag()[tag]
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	k := recordKey{tag: tag, id: id}
	rec, ok := s.ledger.records[k]
	switch {
	case !ok || rec.state != stateOffered:
	case rec.pushable && subscribed:
		rec.state, rec.page = stateHeld, ""
	default:
		delete(s.ledger.records, k)
	}
}

// Seen records marker as the tab's (kind, ref) newest ack and ends, on every tag, each non-ask notice about that
// tab raised at a position from 1 to marker.
func (s *Service) Seen(kind marotte.TabKind, ref string, marker uint64) {
	tab := tabKey{kind: kind, ref: ref}
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	l := &s.ledger
	if marker > l.markers[tab].marker {
		if l.markers == nil {
			l.markers = make(map[tabKey]tabMarker)
		}
		l.seq++
		l.markers[tab] = tabMarker{marker: marker, seq: l.seq}
		if len(l.markers) > maxTabMarkers {
			l.evictOldestMarker()
		}
	}
	for k, rec := range l.records {
		if !rec.ask() && rec.hasTab && rec.tab == tab && rec.position > 0 && rec.position <= marker {
			delete(l.records, k)
		}
	}
}

func (l *ledger) evictOldestMarker() {
	var oldest tabKey
	var at uint64
	for tab, m := range l.markers {
		if at == 0 || m.seq < at {
			oldest, at = tab, m.seq
		}
	}
	delete(l.markers, oldest)
}

// Settled ends, on every tag, each notice about subject announcing ask: the ask was answered on some surface, so a
// nudge about it has nothing left to say. Another ask's notice and a notice announcing no ask stay.
func (s *Service) Settled(subject marotte.PushSubject, ask string) {
	if ask == "" {
		return
	}
	want := debounceKey("", subject).subject
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	for k, rec := range s.ledger.records {
		if rec.notice.Ask == ask && rec.key.subject == want {
			delete(s.ledger.records, k)
		}
	}
}

// TabClosed forgets the tab's acked marker. Its notices stay: a held push for a closed chat still goes at gone.
func (s *Service) TabClosed(kind marotte.TabKind, ref string) {
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	delete(s.ledger.markers, tabKey{kind: kind, ref: ref})
}

// View records one page's view report under its profile's tag.
func (s *Service) View(tag string, v marotte.PageView) {
	if s.presence != nil {
		s.presence.View(tag, v)
	}
}

// A beat already running sees the cancelled lifetime and sends nothing.
func (l *ledger) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}
