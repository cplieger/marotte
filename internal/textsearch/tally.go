package textsearch

// Tally is what a scan reports beside its matches. Embedded, so each reply
// spells the three facts once and wiregen flattens them into its TS type.
type Tally struct {
	// Scanned is how many units the scan READ, whole or in part (or whose index entry stood in).
	// A unit read and found out of scope, or vanished mid-walk, is scanned; one it meant to read
	// and could not is not, and Truncated says so.
	Scanned int `json:"scanned"`
	// Matched is how many rows Matches would hold had nothing cut it: the same
	// unit as Matches (hits, matching lines, chats). The list is cut iff
	// Matched > len(Matches). Truncated never means a cut.
	Matched int `json:"matched"`
	// Truncated is true when the scan did not read everything it was asked to:
	// a cap on files or chats, a file read partially or not at all, a dead context.
	Truncated bool `json:"truncated"`
}
