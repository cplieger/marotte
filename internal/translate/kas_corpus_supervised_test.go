package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	supervisedPath     = "/work/draft.txt"
	supervisedOriginal = "The first draft stays.\n"
)

// permissionAsk is a request_permission's options and the files its turn review names.
type permissionAsk struct {
	Options []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
	Meta struct {
		Kiro struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"kiro"`
	} `json:"_meta"`
}

func (p *permissionAsk) optionOf(kind string) string {
	for _, o := range p.Options {
		if o.Kind == kind {
			return o.OptionID
		}
	}
	return ""
}

// answerAfter is the index of the first outbound reply to id at or after from, or -1.
func answerAfter(lines []kasLine, from int, id int64) int {
	for i := from; i < len(lines); i++ {
		l := lines[i]
		if l.Dir == "out" && l.Frame.Method == "" && l.Frame.ID != nil && *l.Frame.ID == id {
			return i
		}
	}
	return -1
}

// TestKASCorpus_ARefusedTurnReviewRestoresTheFile pins KAS's recorded answer to the floor's refusal of a supervised
// turn's review (Bots design §15 P6): the review names the file the turn changed, the reject_once answer carries the
// floor's note, and KAS then writes the file's original bytes back through the client, as it does for a person's
// Reject, and fails the write's tool call.
func TestKASCorpus_ARefusedTurnReviewRestoresTheFile(t *testing.T) {
	lines := loadKASLines(t, "supervised_refused")
	review := slices.IndexFunc(lines, func(l kasLine) bool {
		return l.Dir == "in" && l.Frame.Method == marotte.MethodRequestPermission && l.kiroMeta(t)["type"] == "turn_approval"
	})
	if review < 0 {
		t.Fatal("the capture holds no turn review")
	}
	var ask permissionAsk
	if err := json.Unmarshal(lines[review].Frame.Params, &ask); err != nil {
		t.Fatal(err)
	}
	if len(ask.Meta.Kiro.Files) != 1 || ask.Meta.Kiro.Files[0].Path != supervisedPath {
		t.Errorf("the review names files %+v, want %s alone", ask.Meta.Kiro.Files, supervisedPath)
	}
	reject := ask.optionOf("reject_once")
	if reject == "" || ask.optionOf("allow_once") == "" {
		t.Fatalf("the review offers %+v, want an allow_once and a reject_once", ask.Options)
	}

	at := answerAfter(lines, review, *lines[review].Frame.ID)
	if at < 0 {
		t.Fatal("the review has no answer")
	}
	var answer struct {
		Meta struct {
			Kiro struct {
				RejectionReason string `json:"rejectionReason"`
			} `json:"kiro"`
		} `json:"_meta"`
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(lines[at].Frame.Result, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Outcome.Outcome != "selected" || answer.Outcome.OptionID != reject || answer.Meta.Kiro.RejectionReason == "" {
		t.Fatalf("the review was answered %s, want reject_once %q with a rejection note", lines[at].Frame.Result, reject)
	}

	var changed bool
	for _, x := range fileExchanges(t, lines[:review]) {
		if x.method == marotte.MethodFSWrite && x.path == supervisedPath && x.content != supervisedOriginal && !x.refused {
			changed = true
		}
	}
	var last *fileExchange
	after := fileExchanges(t, lines[at:])
	for i := range after {
		if after[i].method == marotte.MethodFSWrite && after[i].path == supervisedPath {
			last = &after[i]
		}
	}
	restored := last != nil && last.content == supervisedOriginal && !last.refused
	if !changed {
		t.Error("the turn wrote no new content before its review, so the refusal had nothing to restore")
	}
	if !restored {
		t.Errorf("the last write of %s after the refusal is %+v, want the original %q answered", supervisedPath, last, supervisedOriginal)
	}

	var failed bool
	for _, l := range lines[at:] {
		if l.Dir != "in" || l.Frame.Method != marotte.MethodSessionUpdate {
			continue
		}
		var p struct {
			Update struct {
				SessionUpdate marotte.ACPUpdateKind `json:"sessionUpdate"`
				Status        string                `json:"status"`
			} `json:"update"`
		}
		if err := json.Unmarshal(l.Frame.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Update.SessionUpdate == marotte.ACPUpdateToolUpdate && p.Update.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Error("no tool call failed after the refusal: KAS kept the reviewed write")
	}
}
