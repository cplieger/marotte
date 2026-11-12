package push

import (
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxPagesPerTag bounds one profile's page rows; the oldest report is evicted past it.
const maxPagesPerTag = 16

// pageRow is one page's last view report under its profile's tag.
type pageRow struct {
	// tab is the chat or run tab the page shows; nil for every other tab or none.
	tab        *marotte.TabRef
	reportedAt time.Time
	// visibleAt is when the page was last reported visible, or was visible up to; zero if never.
	visibleAt time.Time
	visible   bool
	focused   bool
}

// View records one page's report under tag. A closed report deletes the page's row. A report for a
// tag with no presence row is dropped: the tag's stream is what makes its pages live.
func (p *Presence) View(tag string, v marotte.PageView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row, ok := p.rows[tag]
	if !ok {
		return
	}
	if v.Closed {
		delete(row.pages, v.Page)
		return
	}
	now := p.now()
	prev := row.pages[v.Page]
	page := &pageRow{tab: v.Tab, reportedAt: now, visible: v.Visible, focused: v.Focused}
	switch {
	case v.Visible || (prev != nil && prev.visible):
		page.visibleAt = now
	case prev != nil:
		page.visibleAt = prev.visibleAt
	}
	if row.pages == nil {
		row.pages = make(map[string]*pageRow)
	}
	row.pages[v.Page] = page
	if len(row.pages) > maxPagesPerTag {
		evictOldestPage(row.pages)
	}
}

func evictOldestPage(pages map[string]*pageRow) {
	var oldest string
	for id, pg := range pages {
		if oldest == "" || pg.reportedAt.Before(pages[oldest].reportedAt) {
			oldest = id
		}
	}
	delete(pages, oldest)
}

// pageOnTab reports whether a live page of tag is visible on the tab (kind, ref).
func (p *Presence) pageOnTab(tag string, kind marotte.TabKind, ref string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pages, now := p.livePages(tag)
	for _, pg := range pages {
		if pg.tab != nil && pg.tab.Kind == kind && pg.tab.Ref == ref && pg.onScreen(now) {
			return true
		}
	}
	return false
}

// offerTarget picks the one page of tag a notice is offered to: the focused visible page, else the
// one visible most recently, else the one that reported most recently. ok is false when tag has no
// live page.
func (p *Presence) offerTarget(tag string) (page string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pages, now := p.livePages(tag)
	var best string
	var bestPage *pageRow
	for id, pg := range pages {
		if bestPage == nil || pg.offerBefore(id, bestPage, best, now) {
			best, bestPage = id, pg
		}
	}
	return best, bestPage != nil
}

// dropPage deletes page's row under tag: its offer went unanswered.
func (p *Presence) dropPage(tag, page string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if row, ok := p.rows[tag]; ok {
		delete(row.pages, page)
	}
}

// livePages returns tag's page rows after judging the tag, which clears a gone tag's rows, so a
// page whose stream left is never read as live. Caller holds mu.
func (p *Presence) livePages(tag string) (map[string]*pageRow, time.Time) {
	now := p.now()
	row, ok := p.rows[tag]
	if !ok {
		return nil, now
	}
	p.judge(row, now)
	return row.pages, now
}

// onScreen is the page's visibility as of now: a visible page re-reports every keepalive, so a
// visible row silent past the alive window belongs to a page that crashed or froze.
func (pg *pageRow) onScreen(now time.Time) bool {
	return pg.visible && now.Sub(pg.reportedAt) <= liveness.AliveWindow
}

// offerBefore orders two pages for offerTarget; ties go to the smaller page id so the pick is stable.
func (pg *pageRow) offerBefore(id string, other *pageRow, otherID string, now time.Time) bool {
	if a, b := pg.onScreen(now) && pg.focused, other.onScreen(now) && other.focused; a != b {
		return a
	}
	if !pg.visibleAt.Equal(other.visibleAt) {
		return pg.visibleAt.After(other.visibleAt)
	}
	if !pg.reportedAt.Equal(other.reportedAt) {
		return pg.reportedAt.After(other.reportedAt)
	}
	return id < otherID
}
