package server

import (
	"net/http"
	"strconv"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// Read-only: every tab mutation rides POST /api/command (invariant 1).
type tabReader interface {
	// Snapshot returns the set in order, the Recently closed list and the version both
	// reflect, captured in ONE critical section. That pairing is the contract, not an
	// implementation detail — see handleTabs.
	Snapshot() ([]marotte.TabSubject, []marotte.RecentClose, uint64)
}

// handleTabs serves GET /api/tabs -> {"tabs": [TabSubject], "recent": [RecentClose],
// "version": N}, read-only. The set, the list and the version come from ONE Snapshot call:
// read separately, a mutation landing between them stamps old tabs with a newer version, and
// the client discards the event the snapshot omitted. The version rules are on
// marotte.TabsChangedPayload.Version.
func (s *Server) handleTabs(w http.ResponseWriter, _ *http.Request) {
	if s.tabs == nil {
		// Unwired: the empty collection at version 0, not 404, so a client can still boot.
		httpserver.WriteJSON(w, marotte.TabList{Tabs: []marotte.TabSubject{}, Recent: []marotte.RecentClose{}})
		return
	}
	open, recent, version := s.tabs.Snapshot()
	// [] rather than null: the client decodes arrays on the boot path.
	if open == nil {
		open = []marotte.TabSubject{}
	}
	if recent == nil {
		recent = []marotte.RecentClose{}
	}
	out := marotte.TabList{Tabs: open, Recent: recent, Version: version}
	if s.agent != nil {
		// The store's collection version IS the `tabs` subject's; no second counter.
		out.Subject = &marotte.SubjectStamp{
			Kind:    string(subject.KindTabs),
			Version: strconv.FormatUint(version, 10),
			Epoch:   s.agent.Epoch(),
		}
	}
	httpserver.WriteJSON(w, out)
}
