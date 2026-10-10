package server

import (
	"net/http"
	"strconv"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/webhttp/v3"
)

// Read-only: every tab mutation rides POST /api/command (invariant 1).
type tabReader interface {
	// List returns the set in order plus the version it reflects, captured in ONE
	// critical section. That pairing is the contract, not an implementation
	// detail — see handleTabs.
	List() ([]marotte.TabSubject, uint64)
}

// handleTabs serves GET /api/tabs -> {"tabs": [TabSubject], "version": N}, read-only.
// The set and the version come from ONE Store.list call: read separately, a mutation landing
// between them stamps old tabs with a newer version, and the client discards the event the
// snapshot omitted. The version rules are on marotte.TabsChangedPayload.Version.
func (s *Server) handleTabs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if s.tabs == nil {
		// Unwired: the empty collection at version 0, not 404, so a client can still boot.
		webhttp.WriteJSON(w, marotte.TabList{Tabs: []marotte.TabSubject{}})
		return
	}
	open, version := s.tabs.List()
	if open == nil {
		// [] rather than null: the client decodes an array on the boot path.
		open = []marotte.TabSubject{}
	}
	out := marotte.TabList{Tabs: open, Version: version}
	if s.agent != nil {
		// The store's collection version IS the `tabs` subject's; no second counter.
		out.Subject = &marotte.SubjectStamp{
			Kind:    string(subject.KindTabs),
			Version: strconv.FormatUint(version, 10),
			Epoch:   s.agent.Epoch(),
		}
	}
	webhttp.WriteJSON(w, out)
}
