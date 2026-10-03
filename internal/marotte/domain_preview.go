package marotte

import "time"

// PreviewPreset is a named preview width an HTML page asks to be judged at.
type PreviewPreset string

// The three presets a page may name in <meta name="marotte-preview">.
const (
	PreviewPresetPhone   PreviewPreset = "phone"
	PreviewPresetTablet  PreviewPreset = "tablet"
	PreviewPresetDesktop PreviewPreset = "desktop"
)

// PreviewHintSource says which meta element a viewport hint was read from.
type PreviewHintSource string

// The two meta elements a hint can come from.
const (
	PreviewHintMeta     PreviewHintSource = "meta"
	PreviewHintViewport PreviewHintSource = "viewport"
)

// PreviewGrantRequest is POST /api/preview/grant's body: the absolute path of
// the HTML page to preview.
type PreviewGrantRequest struct {
	Path string `json:"path"`
}

// PreviewHint is the width a page asks to be previewed at. Exactly one of
// Preset and Width is set.
type PreviewHint struct {
	Preset PreviewPreset     `json:"preset,omitempty"`
	Source PreviewHintSource `json:"source"`
	Width  int               `json:"width,omitempty"`
}

// PreviewGrant is a capability to read one folder through /preview/. URL is the
// entry page and is used verbatim as the iframe src; Epoch changes when the
// server restarts, which invalidates every grant it minted. Hint is absent when
// the page states no size. Stamp is the folder's PreviewStamp.Stamp read before
// the grant was minted, so it is never newer than the document URL serves.
type PreviewGrant struct {
	ExpiresAt time.Time    `json:"expires_at"`
	Hint      *PreviewHint `json:"hint,omitempty"`
	URL       string       `json:"url"`
	Base      string       `json:"base"`
	Epoch     string       `json:"epoch"`
	Stamp     string       `json:"stamp"`
}

// PreviewStamp is GET /api/preview/stamp's reply: a digest of the previewed
// folder's tree, which changes when any file in it is written.
type PreviewStamp struct {
	Stamp     string `json:"stamp"`
	Epoch     string `json:"epoch"`
	Entries   int    `json:"entries"`
	Truncated bool   `json:"truncated"`
}
