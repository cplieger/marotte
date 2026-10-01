package marotte

import "time"

// SpecDocRole is which of a spec directory's documents a file is. The set is
// closed: bugfix.md is reported as requirements, and any other markdown file
// is other, labelled by the client from its filename stem.
type SpecDocRole string

// SpecDocRoleRequirements and the constants below are the SpecDocRole members.
const (
	SpecDocRoleRequirements SpecDocRole = "requirements"
	SpecDocRoleDesign       SpecDocRole = "design"
	SpecDocRoleTasks        SpecDocRole = "tasks"
	SpecDocRoleOther        SpecDocRole = "other"
)

// Spec is the answer to GET /api/specs/{dir}: one spec directory and its
// markdown documents in display order.
type Spec struct {
	// Dir is the workspace-relative directory, ".kiro/specs/<name>" or
	// "<repo>/.kiro/specs/<name>", and is the spec tab's Ref.
	Dir string `json:"dir"`
	// Name is the directory's last segment.
	Name string `json:"name"`
	// UpdatedAt is the newest mtime across the documents.
	UpdatedAt time.Time `json:"updated_at"`
	// Docs is an ordered list, never a fixed trio.
	Docs []SpecDoc `json:"docs"`
	// Approvals is the human sign-off per phase, keyed by SpecDocRole. Absent
	// for a phase nobody approved; a MAP rather than a slice because a phase is
	// a key and every reader looks one up by it — the ETag digests the entries
	// in a fixed order and the client indexes by the segment's role.
	Approvals map[string]SpecApproval `json:"approvals,omitempty"`
}

// SpecApproval records that a human approved one phase of a spec, against the
// exact text they approved.
//
// It RECORDS, it does not ENFORCE. There is no phase-order gate, nothing refuses
// to run tasks because design is unapproved, and no control is disabled by one:
// the agent writes these documents through its own file tools, so this server
// cannot enforce an order without owning that access. What the record preserves
// is which exact version was signed off, which is what makes Stale meaningful.
type SpecApproval struct {
	// Hash is the sha256 hex of the document as it was when approved.
	Hash string `json:"hash"`
	// At is when the approval was recorded.
	At time.Time `json:"at"`
	// User is who approved, and is EMPTY today: marotte is single-operator and
	// knows no identity to record. Recorded as the empty string rather than
	// invented, so a future identity does not have to be told apart from a
	// fabricated one.
	User string `json:"user"`
	// Stale reports that the document has MOVED since it was approved, derived
	// at read time against the doc's live hash and never stored. A phase whose
	// document has since disappeared is stale too — there is nothing left for
	// the approval to describe.
	Stale bool `json:"stale"`
}

// SpecDoc is one markdown document of a spec. The task fields are present on
// the tasks document alone.
type SpecDoc struct {
	// Progress counts the tasks document's required leaves by status.
	Progress *SpecProgress `json:"progress,omitempty"`
	// Truncated is set when the tasks tree was cut at the node cap.
	Truncated *SpecTruncated `json:"truncated,omitempty"`
	// File is the filename within the spec directory.
	File string `json:"file"`
	// Role classifies the file by name.
	Role SpecDocRole `json:"role"`
	// Hash is the sha256 of the bytes returned, hex-encoded.
	Hash string `json:"hash"`
	// Content is the whole file, empty when TooLarge.
	Content string `json:"content"`
	// Tasks is the parsed task tree of the tasks document.
	Tasks []SpecTaskNode `json:"tasks,omitempty"`
	// UnreadableLines counts lines shaped like a task that Kiro's parser refuses.
	UnreadableLines int `json:"unreadable_lines,omitempty"`
	// TooLarge reports a file over the editor's read cap; Content is then empty.
	TooLarge bool `json:"too_large,omitempty"`
}

// SpecTaskNode is one task line of tasks.md as Kiro's own parser reads it.
type SpecTaskNode struct {
	// ID is "L<line>", the node's address within the document.
	ID string `json:"id"`
	// Number is the dotted task number, empty when the line carries none.
	Number string `json:"number"`
	// Text is the task text verbatim; it is the id Kiro resolves a task by.
	Text string `json:"text"`
	// Status is the box: pending, in_progress or completed.
	Status PlanStatus `json:"status"`
	// Hash is the sha256 of Text, hex-encoded.
	Hash string `json:"hash"`
	// Detail is the task's content lines, de-indented and cut at the first heading.
	Detail string `json:"detail"`
	// Children is always present on the wire, empty for a leaf.
	Children []SpecTaskNode `json:"children"`
	// Wave is the parallel batch the tasks document's dependency graph puts
	// this task in, nil when it names none. A POINTER because 0 is a real
	// wave id: an absent wave must not read as the first one.
	Wave *int `json:"wave,omitempty"`
	// Line is the 1-based line number of the task line.
	Line int `json:"line"`
	// Indent is the raw character count of the line's leading whitespace.
	Indent int `json:"indent"`
	// Queued reports a "[~]" box, which is pending with queued set.
	Queued bool `json:"queued"`
	// Optional reports a "*" after the box.
	Optional bool `json:"optional"`
	// TruncatedChildren reports that this node's children were cut at the cap.
	TruncatedChildren bool `json:"truncated_children,omitempty"`
}

// SpecProgress counts a tasks document's REQUIRED leaves. Queued is a subset
// of Pending, and Pending + InProgress + Completed == Total.
type SpecProgress struct {
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Queued     int `json:"queued"`
	Total      int `json:"total"`
}

// SpecTruncated reports a task tree cut at the node cap: Returned nodes of
// Total in the file.
type SpecTruncated struct {
	Returned int `json:"returned"`
	Total    int `json:"total"`
}
