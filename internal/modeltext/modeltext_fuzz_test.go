package modeltext

import "testing"

// FuzzHidden_tagAnywhereHides: a tag appended in any case hides the model, and a hidden
// description stays hidden inside any surrounding text.
func FuzzHidden_tagAnywhereHides(f *testing.F) {
	f.Add("[DEPRECATED] model")
	f.Add("café [Legacy]")
	f.Add("no tags here")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		if !Hidden(text + " [LeGaCy]") {
			t.Errorf("Hidden(%q + a [Legacy] tag) = false, want true", text)
		}
		if Hidden(text) && !Hidden("before "+text+" after") {
			t.Errorf("Hidden(%q) = true but false once surrounded by text", text)
		}
	})
}
