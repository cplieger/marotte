package agent

import (
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestCatalog_AnEmptyListIsNotAnEmptyCatalog(t *testing.T) {
	// session/load omits the catalog routinely and modes have no repair channel, so empty must not overwrite.
	seededModes := []marotte.SessionMode{{ID: "spec", Name: "Spec"}}
	seededModels := []marotte.SessionModel{{ID: "m1", Name: "One"}}
	c := &catalog{}
	c.setModes(seededModes)
	c.SetModels(seededModels)

	for _, modes := range [][]marotte.SessionMode{nil, {}} {
		if c.setModes(modes) {
			t.Errorf("SetModes(%v) reported a change, want false", modes)
		}
	}
	for _, models := range [][]marotte.SessionModel{nil, {}} {
		if c.SetModels(models) {
			t.Errorf("SetModels(%v) reported a change, want false", models)
		}
	}

	modes, models, _ := c.modesModelsStamped()
	if !slices.Equal(modes, seededModes) {
		t.Errorf("modes = %v, want the seeded %v", modes, seededModes)
	}
	if !slices.Equal(models, seededModels) {
		t.Errorf("models = %v, want the seeded %v", models, seededModels)
	}
}

func TestCatalog_ReportsAChangeOnlyWhenSomethingChanged(t *testing.T) {
	// The chat store persists and broadcasts only on change, so a repeated frame answers false.
	modes := []marotte.SessionMode{{ID: "spec", Name: "Spec"}}
	c := &catalog{}

	if !c.setModes(modes) {
		t.Error("the first SetModes reported no change, want true")
	}
	if c.setModes(slices.Clone(modes)) {
		t.Error("an identical SetModes reported a change, want false")
	}
	if !c.setModes([]marotte.SessionMode{{ID: "spec", Name: "Specification"}}) {
		t.Error("a renamed mode reported no change, want true: the NAME is what the picker renders")
	}
}

func TestCatalog_ReturnsACopy(t *testing.T) {
	// A reader must not reach the holder's slice; SessionMode holds only strings.
	c := &catalog{}
	c.setModes([]marotte.SessionMode{{ID: "spec", Name: "Spec"}})

	got, _, _ := c.modesModelsStamped()
	got[0].Name = "mutated by the caller"

	if again, _, _ := c.modesModelsStamped(); again[0].Name != "Spec" {
		t.Errorf("modes[0].Name = %q after a caller mutated its copy, want %q",
			again[0].Name, "Spec")
	}
}

func TestCatalog_SeedingIsNotSharedWithTheCaller(t *testing.T) {
	// The holder must not alias the slice it was handed.
	modes := []marotte.SessionMode{{ID: "spec", Name: "Spec"}}
	c := &catalog{}
	c.setModes(modes)

	modes[0].Name = "mutated by the writer"

	if held, _, _ := c.modesModelsStamped(); held[0].Name != "Spec" {
		t.Errorf("modes[0].Name = %q after the writer mutated its own slice, want %q",
			held[0].Name, "Spec")
	}
}

func TestCatalog_DefaultEffortFor(t *testing.T) {
	c := &catalog{}
	c.SetModels([]marotte.SessionModel{
		{ID: "m1", DefaultEffortLevel: "high"},
		{ID: "m2"},
	})

	tests := map[string]string{
		"m1": "high",
		"m2": "",
		"m9": "",
	}
	for model, want := range tests {
		if got := c.defaultEffortFor(model); got != want {
			t.Errorf("DefaultEffortFor(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestCatalog_ConcurrentReadersAndWriters(t *testing.T) {
	// One holder, many bridges publishing while /api/config-template reads.
	c := &catalog{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			c.setModes([]marotte.SessionMode{{ID: "m", Name: string(rune('a' + i))}})
			c.SetModels([]marotte.SessionModel{{ID: "m", Name: string(rune('a' + i))}})
		})
		wg.Go(func() {
			_, _, _ = c.modesModelsStamped()
			_ = c.defaultEffortFor("m")
		})
	}
	wg.Wait()

	if modes, models, _ := c.modesModelsStamped(); len(modes) != 1 || len(models) != 1 {
		t.Errorf("modes=%v models=%v, want one entry each", modes, models)
	}
}
