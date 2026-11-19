package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/signin"
)

// A turn fails on the newest refusal answered while it was open, else on the engine's newest answer before it opened
// when that was a refusal; a served answer as the newest before it opened means the engine held a served credential.
func TestSignInAsks_ATurnReadsOnlyTheAsksItCouldHaveDependedOn(t *testing.T) {
	t.Parallel()
	const served, down, out = signin.Refusal(0), signin.RefusedUnreachable, signin.RefusedSignedOut
	cases := map[string]struct {
		before, during []signin.Refusal
		want           signin.Refusal
	}{
		"no answers":                      {want: 0},
		"refused, then the turn opens":    {before: []signin.Refusal{down}, want: down},
		"refused, served, then it opens":  {before: []signin.Refusal{down, served}, want: 0},
		"refused, it opens, then served":  {before: []signin.Refusal{down}, during: []signin.Refusal{served}, want: 0},
		"it opens, then refused":          {during: []signin.Refusal{down}, want: down},
		"it opens, refused, then served":  {during: []signin.Refusal{down, served}, want: down},
		"refused, it opens, refused anew": {before: []signin.Refusal{down}, during: []signin.Refusal{out}, want: out},
		"served, then the turn opens":     {before: []signin.Refusal{served}, want: 0},
		"it opens, then refused twice":    {during: []signin.Refusal{out, down}, want: down},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var b signInAsks
			for _, why := range c.before {
				b.answered(why)
			}
			mark := b.mark()
			for _, why := range c.during {
				b.answered(why)
			}
			if got := b.refusalFor(mark); got != c.want {
				t.Errorf("refusalFor = %v, want %v", got, c.want)
			}
		})
	}
}

// A runtime with no vault books no asks, and its turns fail on no refusal.
func TestSignInAsks_ANilBookAnswersNoRefusal(t *testing.T) {
	t.Parallel()
	var b *signInAsks
	b.answered(signin.RefusedUnreachable)
	if got := b.refusalFor(b.mark()); got != 0 {
		t.Errorf("refusalFor on a nil book = %v, want none", got)
	}
}
