package marotte

// TabKind is what a tab SHOWS; with a subject's Ref it NAMES the open thing, so the id stays
// opaque. The set is EXHAUSTIVE and validated at the door (Valid, tabs.Store.Open): the client's
// per-kind factory is total, so an unknown kind would reach a switch with no case on every device.
// Here rather than in internal/tabs because TabSubject is a wire type.
type TabKind string

// The eleven tab kinds; each string is the wire value AND the client's TabKind union member, so a
// rename is a cross-language change. There is no "plan" kind (roles.ts's "plan" is a mode id).
// "subagent"'s Ref is the only composite one, `<chatID>/<agentSubtaskID>`, since nothing indexes a
// subtask to a chat; a chat id holds no slash, so the split is unambiguous. "spec"'s Ref is the
// workspace-relative spec directory; "web"'s is the page's absolute path.
const (
	TabKindChat     TabKind = "chat"
	TabKindEditor   TabKind = "editor"
	TabKindRun      TabKind = "run"
	TabKindSubagent TabKind = "subagent"
	TabKindSettings TabKind = "settings"
	TabKindGit      TabKind = "git"
	TabKindFiles    TabKind = "files"
	TabKindHistory  TabKind = "history"
	TabKindDocs     TabKind = "docs"
	TabKindSpec     TabKind = "spec"
	TabKindWeb      TabKind = "web"
)

// tabKinds is the authoritative set, and the bool answers the question a caller
// always asks next: is this kind a SINGLETON, the one tab of its type, whose Ref
// is therefore empty?
//
// ONE table rather than two, because two tables can disagree: a tenth kind added
// to the valid set and forgotten in the singleton set would be a kind that is
// accepted and then required to carry a ref it has no meaning for, which reaches
// a reader as "Settings opens twice" rather than as an error.
var tabKinds = map[TabKind]bool{
	TabKindChat:     false,
	TabKindEditor:   false,
	TabKindRun:      false,
	TabKindSubagent: false,
	TabKindSettings: true,
	TabKindGit:      true,
	TabKindFiles:    false,
	TabKindHistory:  true,
	TabKindDocs:     true,
	TabKindSpec:     false,
	TabKindWeb:      false,
}

// Valid reports whether k is one of the eleven kinds. Used at the command
// boundary and again inside the store, because a kind that reaches the persisted
// set is a kind every client has to render.
func (k TabKind) Valid() bool {
	_, ok := tabKinds[k]
	return ok
}

// Singleton reports whether k has exactly one tab — settings, git, history and
// docs — so its subject's Ref is empty and a second open of it returns the tab
// already open.
//
// An unknown kind reports false, so this answer is only meaningful for a kind
// Valid accepts. The one place that matters is tabs.Store.Open, which checks
// Valid first.
func (k TabKind) Singleton() bool { return tabKinds[k] }

// TabSubject is the SHARED fact about one open tab: what it shows, where it sits, and whether
// closing it tears the thing down; the only tab shape persisted and on the wire. The client derives
// its view spec from (Kind, Ref) with a total factory, so no behaviour lives here. No Order field:
// the slice position is the order. Field order is fieldalignment's.
type TabSubject struct {
	// ID is opaque and server-minted (tabs.Store mints it at open): Kind and Ref name the
	// subject, so nothing may branch on the id. Opacity also keeps the API path unambiguous
	// under a reverse proxy that normalizes %2F.
	ID string `json:"id"`
	// Kind and Ref are the subject's identity: at most one tab exists per
	// (Kind, Ref) pair, which is what makes an open idempotent.
	Kind TabKind `json:"kind"`
	// Ref is a chat id, an absolute path, a run id, or a subagent's `<chatID>/<agentSubtaskID>`
	// pair; empty for a singleton. The store treats it as opaque: validity is the command
	// boundary's check (ids.ValidChatID, the file-browser roots).
	Ref string `json:"ref"`
	// Parent is the tab this one hangs under, empty for a top-level tab. Only reparent_tab
	// reassigns it, and only to an open CHAT tab, so a chain cannot close on itself and no
	// cycle check is needed.
	Parent string `json:"parent"`
	// Pinned sorts a tab ahead of every unpinned one. The partition is applied
	// by the client when it renders (applyPinOrder); the stored slice keeps the
	// order it was given.
	Pinned bool `json:"pinned"`
	// Owns means closing this tab tears down what it shows: a run REVIEW from History and a
	// launcher-OWNED run share (Kind, Ref), and closing the owned one cancels the run. Set at
	// open, so the authority cannot change under a reader.
	Owns bool `json:"owns"`
}

// OpenTab is the argument to tabs.Store.Open: everything a subject needs that the store cannot
// mint. A struct because Ref and Parent are adjacent same-typed strings. No op_id: that is the
// command envelope's.
type OpenTab struct {
	// Kind is required and must be one of the eleven (see TabKind.Valid).
	Kind TabKind `json:"kind"`
	// Ref is required for every kind but a singleton, where it must be empty.
	Ref string `json:"ref,omitempty"`
	// Parent names an already-open tab. A parent that is not open PROMOTES the
	// new tab to top level rather than refusing it, which is what the client's
	// insertSpec does with an orphan for the same reason: a tab nobody can see
	// is worse than a tab in the wrong place.
	Parent string `json:"parent,omitempty"`
	// Owns is the authority flag described on TabSubject.Owns. The caller
	// decides it, because only the caller knows whether it launched the thing
	// this tab shows.
	Owns bool `json:"owns,omitempty"`
}

// TabList is the answer to GET /api/tabs: the open set in order plus the version it reflects,
// captured together by tabs.Store.list so a stale set cannot pair with a fresh version. Tabs is
// never omitted: an empty arrangement is a real state.
type TabList struct {
	// Subject is the `tabs` digest stamp with the hub epoch: the same Version
	// as below, spelled the way the client's version map reads it.
	Subject *SubjectStamp `json:"subject,omitempty"`
	Tabs    []TabSubject  `json:"tabs"`
	Version uint64        `json:"version"`
}
