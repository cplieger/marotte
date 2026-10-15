package modeltext

import "testing"

// TestHidden pins the hidden tags in every case, and that [internal] and [experimental] models
// stay SHOWN in the picker.
func TestHidden(t *testing.T) {
	cases := []struct {
		name, in string
		want     bool
	}{
		{"deprecated_lowercase", "a model [deprecated]", true},
		{"deprecated_titlecase", "a model [Deprecated]", true},
		{"deprecated_uppercase", "A MODEL [DEPRECATED]", true},
		{"legacy", "old thing [legacy]", true},
		{"untagged", "a perfectly good model", false},
		{"prose_is_not_a_tag", "this model is deprecated in prose", false},
		{"empty", "", false},
		{"internal_is_not_hidden", "a model [Internal]", false},
		{"experimental_is_not_hidden", "a model [Experimental]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Hidden(tc.in); got != tc.want {
				t.Errorf("Hidden(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
