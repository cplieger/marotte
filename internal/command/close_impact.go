package command

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// CloseWork is what closing one chat stops, as the server holds it, every item by the ids that name it.
type CloseWork struct {
	Turns  []CloseTurn
	Asks   []string
	Runs   []string
	Steers []string
	Queued []string
}

// CloseTurn is one open turn, or a prompt admitted with no turn yet (ID empty).
type CloseTurn struct {
	ID        string
	MessageID string
	// Admission is AdmissionID of the reservation it opened under, "" for a wire turn.
	Admission string
	// Resends are the unread steer keys the turn's prompt carries.
	Resends []string
}

// AdmissionID names a reservation to a close's confirmation.
func AdmissionID(token uint64) string { return "res:" + strconv.FormatUint(token, 10) }

// closeImpactReader reads what closing each chat stops. CloseImpact runs under Membership's lock after the chats'
// holds: it reads in-memory or cached state only and never calls kiro-cli.
type closeImpactReader interface {
	CloseImpact(ctx context.Context, chatIDs []marotte.ChatID) map[marotte.ChatID]CloseWork
}

// reasonCloseImpactChanged is the 409 a close or delete answers when its confirmation did not cover what it stops.
const reasonCloseImpactChanged = "close_impact_changed"

var (
	errCloseImpactChanged  = errors.New("what closing this tab stops changed; confirm again")
	errDeleteImpactChanged = errors.New("what deleting this chat stops changed; confirm again")
)

// The limits on a confirmation's impact; a page names at most what its strip can show.
const (
	maxImpactTurns     = 64
	maxImpactAsks      = 256
	maxImpactRuns      = 256
	maxImpactSteers    = 1000
	maxImpactQueued    = 1000
	maxImpactWorkIDLen = 128
)

// validCloseImpact reports whether a page's impact is well formed; nil is (unchecked).
func validCloseImpact(impact *marotte.CloseImpact) bool {
	if impact == nil {
		return true
	}
	if len(impact.Chats) > tabs.MaxOpenTabs {
		return false
	}
	refs := make(map[string]bool, len(impact.Chats))
	for i := range impact.Chats {
		w := &impact.Chats[i]
		if !validChatID(marotte.ChatID(w.Ref)) || refs[w.Ref] {
			return false
		}
		refs[w.Ref] = true
		if !validWorkIDs(w.Turns, maxImpactTurns) || !validWorkIDs(w.Asks, maxImpactAsks) ||
			!validWorkIDs(w.Runs, maxImpactRuns) || !validWorkIDs(w.Steers, maxImpactSteers) ||
			!validWorkIDs(w.Queued, maxImpactQueued) {
			return false
		}
	}
	return true
}

func validWorkIDs(list []string, limit int) bool {
	return len(list) <= limit && !slices.ContainsFunc(list, func(id string) bool { return !validWorkID(id) })
}

// validWorkID is 1 to 128 bytes of ASCII letters, digits and -_.: (the ':' admits AdmissionID).
func validWorkID(id string) bool {
	if id == "" || len(id) > maxImpactWorkIDLen {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// impactCheck is one close's comparison of what its confirmation named with what the close stops now.
type impactCheck struct {
	page   *marotte.CloseImpact
	server map[marotte.ChatID]CloseWork
	chats  []marotte.ChatID
}

// checkImpact reads what closing chats stops and compares it with page. stopsRuns is false for a close that leaves
// the chats' runs running (a delete's descendants), whose runs are then neither compared nor answered. ok is true
// for a nil page or an unwired reader (unchecked). Caller holds mu, after the chats' holds.
func (m *Membership) checkImpact(
	ctx context.Context, page *marotte.CloseImpact, chats []marotte.ChatID, stopsRuns bool,
) (impactCheck, bool) {
	if page == nil || m.impact == nil {
		return impactCheck{}, true
	}
	c := impactCheck{page: page, chats: chats, server: m.impact.CloseImpact(ctx, chats)}
	if !stopsRuns {
		for chatID, w := range c.server {
			w.Runs = nil
			c.server[chatID] = w
		}
	}
	ok := c.covered()
	return c, ok
}

// covered is the cover rule: every chat has an entry, and every item the server holds is named by one of its ids.
// A page that names more than the server holds is covered; amounts are never compared, so a new id is never hidden
// behind one that ended.
func (c *impactCheck) covered() bool {
	named := make(map[string]*marotte.ChatWork, len(c.page.Chats))
	for i := range c.page.Chats {
		named[c.page.Chats[i].Ref] = &c.page.Chats[i]
	}
	for _, chatID := range c.chats {
		page, ok := named[string(chatID)]
		server := c.server[chatID]
		if !ok || !workCovered(&server, page) {
			return false
		}
	}
	return true
}

func workCovered(server *CloseWork, page *marotte.ChatWork) bool {
	turns, queued, steers := idSet(page.Turns), idSet(page.Queued), idSet(page.Steers)
	for i := range server.Turns {
		if !turnCovered(&server.Turns[i], turns, queued, steers) {
			return false
		}
	}
	return subset(server.Asks, idSet(page.Asks)) && subset(server.Runs, idSet(page.Runs)) &&
		subset(server.Steers, steers) && subset(server.Queued, queued)
}

// turnCovered: a turn the page named by any of its ids, or a drain that took a queued row or unread steers the
// page named, which are the same work.
func turnCovered(t *CloseTurn, turns, queued, steers map[string]bool) bool {
	for _, id := range []string{t.ID, t.MessageID, t.Admission} {
		if id != "" && turns[id] {
			return true
		}
	}
	if t.ID == "" {
		return false
	}
	if t.MessageID != "" && queued[t.MessageID] {
		return true
	}
	return len(t.Resends) > 0 && subset(t.Resends, steers)
}

// refusal is the 409 a close or delete answers with the server's impact, after logging it at INFO.
func (c *impactCheck) refusal(err error, attrs ...any) error {
	ids := make([]string, len(c.chats))
	for i, chatID := range c.chats {
		ids[i] = string(chatID)
	}
	slog.Info("a close's confirmation went stale; asked again", append(attrs, "chat_ids", ids)...)
	return statusErrorImpact(http.StatusConflict, reasonCloseImpactChanged, c.wire(), err)
}

// wire is the server's impact as the page reads it: one id per turn (the turn's, or its admission's when no turn
// opened yet, since the page counts one reply per id), and every list present.
func (c *impactCheck) wire() *marotte.CloseImpact {
	out := &marotte.CloseImpact{Chats: make([]marotte.ChatWork, 0, len(c.chats))}
	for _, chatID := range c.chats {
		w := c.server[chatID]
		turns := make([]string, 0, len(w.Turns))
		for i := range w.Turns {
			turns = append(turns, cmp.Or(w.Turns[i].ID, w.Turns[i].Admission))
		}
		out.Chats = append(out.Chats, marotte.ChatWork{
			Ref: string(chatID), Turns: turns, Asks: nonNil(w.Asks), Runs: nonNil(w.Runs),
			Steers: nonNil(w.Steers), Queued: nonNil(w.Queued),
		})
	}
	return out
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func idSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, id := range list {
		out[id] = true
	}
	return out
}

func subset(list []string, of map[string]bool) bool {
	return !slices.ContainsFunc(list, func(id string) bool { return !of[id] })
}
