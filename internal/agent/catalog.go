package agent

import (
	"reflect"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// catalog is the workspace's mode and model catalog as KAS reported it, held once rather than
// per chat; a chat owns only its choice. Mode shadowing arrives already resolved; a client
// must not re-derive it.
type catalog struct {
	// versions holds the `catalog` counter, bumped under mu on any change; nil defaults to a private registry.
	versions *subject.Versions
	modes    []marotte.SessionMode
	models   []marotte.SessionModel
	mu       sync.Mutex
}

// Callers hold mu.
func (c *catalog) registry() *subject.Versions {
	if c.versions == nil {
		c.versions = &subject.Versions{}
	}
	return c.versions
}

// setModes replaces the mode vocabulary, reporting whether it changed. An empty list is
// ignored: session/load routinely omits it, and modes have no repair channel.
func (c *catalog) setModes(modes []marotte.SessionMode) bool {
	if len(modes) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Equal(c.modes, modes) {
		return false
	}
	c.modes = slices.Clone(modes)
	c.registry().BumpCounter(subject.KindCatalog, "")
	return true
}

// SetModels replaces the model catalog, reporting whether it changed; empty is ignored as in SetModes.
func (c *catalog) SetModels(models []marotte.SessionModel) bool {
	if len(models) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// DeepEqual: EffortTiers makes SessionModel incomparable.
	if reflect.DeepEqual(c.models, models) {
		return false
	}
	c.models = slices.Clone(models)
	c.registry().BumpCounter(subject.KindCatalog, "")
	return true
}

// modesModelsStamped returns both lists with the `catalog` stamp (counter read first) under one lock.
func (c *catalog) modesModelsStamped() (modes []marotte.SessionMode, models []marotte.SessionModel, stamp *marotte.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	version, _ := c.registry().Current(subject.KindCatalog, "")
	return slices.Clone(c.modes), slices.Clone(c.models), marotte.NewSubjectStamp(string(subject.KindCatalog), "", version)
}

// tierKnowledge is how much the catalog knows of a model's effort tiers.
type tierKnowledge int

const (
	// tiersUnknown: the catalog does not list the model, as before any session reports it.
	tiersUnknown tierKnowledge = iota
	// tiersNone: the catalog lists the model with no tiers; KAS drops any effort sent for it.
	tiersNone
	tiersListed
)

// modelTiers is the catalog's account of one model's effort tiers; levels and def are set only
// when known is tiersListed.
type modelTiers struct {
	def    string
	levels []string
	known  tierKnowledge
}

// tiersOf returns the catalog's account of model's effort tiers.
func (c *catalog) tiersOf(model string) modelTiers {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.models {
		m := &c.models[i]
		switch {
		case m.ID != model:
			continue
		case len(m.EffortTiers) == 0:
			return modelTiers{known: tiersNone}
		}
		return modelTiers{known: tiersListed, levels: slices.Clone(m.EffortTiers), def: m.DefaultEffortLevel}
	}
	return modelTiers{}
}

// thinkingToggleable reports whether the catalog knows the model and lets thinking be turned off.
func (c *catalog) thinkingToggleable(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.models {
		if c.models[i].ID == model {
			return c.models[i].ThinkingToggleable
		}
	}
	return false
}

// thinkingOffCap is the tier list marotte.CapEffortForThinkingOff caps model's xhigh and max
// against; nil (no cap) for an unknown model or one with no lower tier.
func (c *catalog) thinkingOffCap(model string) []marotte.SessionEffortLevel {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.models {
		if c.models[i].ID == model && c.models[i].ThinkingOffEffortCap != "" {
			return []marotte.SessionEffortLevel{{ID: c.models[i].ThinkingOffEffortCap}}
		}
	}
	return nil
}

// thinkingDefaultOff reports whether the catalog knows the model and defaults its thinking off.
func (c *catalog) thinkingDefaultOff(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.models {
		if c.models[i].ID == model {
			return c.models[i].ThinkingDefaultOff
		}
	}
	return false
}
