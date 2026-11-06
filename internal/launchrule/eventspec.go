package launchrule

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/keyenc"
)

// Kind is the forge event an event rule watches for.
type Kind string

// The event kinds an event rule can watch for.
const (
	KindIssueLabeled Kind = "issue_labeled"
	KindPRLabeled    Kind = "pr_labeled"
	KindIssueOpened  Kind = "issue_opened"
	KindPROpened     Kind = "pr_opened"
	KindRunConcluded Kind = "run_concluded"
)

var kinds = []Kind{KindIssueLabeled, KindPRLabeled, KindIssueOpened, KindPROpened, KindRunConcluded}

func (k Kind) labeled() bool { return k == KindIssueLabeled || k == KindPRLabeled }
func (k Kind) opened() bool  { return k == KindIssueOpened || k == KindPROpened }
func (k Kind) issue() bool   { return k == KindIssueLabeled || k == KindIssueOpened }

// eventInputs are the inputs a launch for kind k sets, so a fixed input may not take their names.
func (k Kind) eventInputs() []string {
	names := []string{"repo", "forge", "trigger_kind", "trigger_rule", "trigger_key", "event_id"}
	switch {
	case k == KindRunConcluded:
		return append(names, "run_id", "result", "head_sha", "workflow")
	case k.issue():
		names = append(names, "issue_number")
	default:
		names = append(names, "pr_number")
	}
	if k.labeled() {
		return append(names, "label")
	}
	return append(names, "author")
}

// HoldPolicy is what happens to events that match while the recipe cannot launch.
type HoldPolicy string

// The two hold policies.
const (
	HoldNewest HoldPolicy = "newest"
	HoldFIFO   HoldPolicy = "fifo"
)

// Hold bounds the events a rule keeps while its recipe cannot launch.
type Hold struct {
	Policy HoldPolicy `json:"policy"`
	Cap    int        `json:"cap"`
}

// Filter narrows the events of a kind; which fields a kind takes is EventSpec's validation.
type Filter struct {
	Label       string   `json:"label,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Workflow    string   `json:"workflow,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Result      []string `json:"result,omitempty"`
	RunTriggers []string `json:"run_triggers,omitempty"`
}

// EventSpec is an event rule's `when`: one kind of event on one repository, read through one forge connection.
// The store fills IntervalMin, Hold and a run rule's Filter.RunTriggers with their defaults when unset.
type EventSpec struct {
	Hold *Hold `json:"hold,omitempty"`
	// Concurrency and CooldownMin are reserved: a body carrying either is refused by name rather than ignored.
	Concurrency json.RawMessage `json:"concurrency,omitempty"`
	CooldownMin json.RawMessage `json:"cooldown_min,omitempty"`
	Connection  string          `json:"connection"`
	Repo        string          `json:"repo"`
	Kind        Kind            `json:"kind"`
	Filter      Filter          `json:"filter"`
	IntervalMin int             `json:"interval_min"`
}

const (
	defaultIntervalMin = 5
	minIntervalMin     = 2
	maxIntervalMin     = 60
	maxHoldCap         = 50
	// maxLabelBytes is GitHub's own label-name limit.
	maxLabelBytes = 50
	maxAuthors    = 50
)

var (
	runResults         = []string{"success", "failure", "neutral"}
	runTriggers        = []string{"push", "schedule", "manual", "pull_request", "other"}
	defaultRunTriggers = []string{"push", "schedule", "manual"}
)

// connectionFamilies maps a connection id's kind to its forge family. forges.MakeID owns the id format and
// forges.Kind these names; the copy keeps launchrule below internal/forges in the import graph.
var connectionFamilies = map[string]forgeapi.Family{
	"github":   forgeapi.FamilyGitHub,
	"gitlab":   forgeapi.FamilyGitLab,
	"gitea":    forgeapi.FamilyGitea,
	"codeberg": forgeapi.FamilyGitea,
}

// normalize validates e and fills its defaults.
func (e *EventSpec) normalize() error {
	if len(e.Concurrency) > 0 {
		return invalid("concurrency is not supported yet")
	}
	if len(e.CooldownMin) > 0 {
		return invalid("cooldown_min is not supported yet")
	}
	family, err := connectionFamily(e.Connection)
	if err != nil {
		return err
	}
	if err := checkRepo(family, e.Repo); err != nil {
		return err
	}
	if !slices.Contains(kinds, e.Kind) {
		return invalid("kind must be one of issue_labeled, pr_labeled, issue_opened, pr_opened, run_concluded")
	}
	if err := e.Filter.normalize(e.Kind, family); err != nil {
		return err
	}
	return e.normalizeCadence()
}

func (e *EventSpec) normalizeCadence() error {
	if e.IntervalMin == 0 {
		e.IntervalMin = defaultIntervalMin
	}
	if e.IntervalMin < minIntervalMin || e.IntervalMin > maxIntervalMin {
		return invalid("interval_min must be 2 to 60")
	}
	if e.Hold == nil {
		e.Hold = &Hold{Policy: HoldFIFO, Cap: 20}
		if e.Kind == KindRunConcluded {
			e.Hold = &Hold{Policy: HoldNewest, Cap: 10}
		}
		return nil
	}
	if e.Hold.Policy != HoldNewest && e.Hold.Policy != HoldFIFO {
		return invalid("hold.policy must be newest or fifo")
	}
	if e.Hold.Cap < 1 || e.Hold.Cap > maxHoldCap {
		return invalid("hold.cap must be 1 to 50")
	}
	return nil
}

func connectionFamily(id string) (forgeapi.Family, error) {
	if id == "" {
		return forgeapi.FamilyUnknown, invalid("connection is required")
	}
	if err := checkText("connection", id); err != nil {
		return forgeapi.FamilyUnknown, err
	}
	parts, err := keyenc.Split(id)
	if err == nil && len(parts) == 2 && parts[1] != "" && !strings.ContainsAny(parts[1], "/@") {
		if family, ok := connectionFamilies[parts[0]]; ok {
			return family, nil
		}
	}
	return forgeapi.FamilyUnknown, invalid("connection %q is not a forge connection", id)
}

func checkRepo(family forgeapi.Family, repo string) error {
	if repo == "" {
		return invalid("repo is required")
	}
	if err := checkText("repo", repo); err != nil {
		return err
	}
	err := forgeapi.ValidateSelector(family, repo)
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok {
		return invalid("repo: %s", fe.Message)
	}
	if err != nil {
		return invalid("repo: %v", err)
	}
	return nil
}

// normalize checks each filter field against kind, the one rule table of which kind takes which field.
func (f *Filter) normalize(kind Kind, family forgeapi.Family) error {
	if err := f.normalizeLabel(kind); err != nil {
		return err
	}
	if err := f.checkAuthors(kind, family); err != nil {
		return err
	}
	if f.Branch != "" && kind.issue() {
		return invalid("filter.branch is not used by %s", kind)
	}
	if err := checkText("filter.branch", f.Branch); err != nil {
		return err
	}
	if kind != KindRunConcluded {
		return f.refuseRunFields()
	}
	return f.normalizeRun(family)
}

func (f *Filter) normalizeLabel(kind Kind) error {
	if !kind.labeled() {
		if f.Label != "" {
			return invalid("filter.label is only used by issue_labeled and pr_labeled")
		}
		return nil
	}
	f.Label = strings.TrimSpace(f.Label)
	if f.Label == "" {
		return invalid("filter.label is required for %s", kind)
	}
	if err := checkText("filter.label", f.Label); err != nil {
		return err
	}
	if len(f.Label) > maxLabelBytes {
		return invalid("filter.label must be 1 to 50 bytes")
	}
	return nil
}

func (f *Filter) checkAuthors(kind Kind, family forgeapi.Family) error {
	if !kind.opened() {
		if len(f.Authors) > 0 {
			return invalid("filter.authors is only used by issue_opened and pr_opened")
		}
		return nil
	}
	if len(f.Authors) == 0 {
		return invalid("filter.authors is required for %s", kind)
	}
	if len(f.Authors) > maxAuthors {
		return invalid("filter.authors holds at most 50 logins")
	}
	for _, a := range f.Authors {
		if strings.Contains(a, "/") || forgeapi.ValidateOwner(family, a) != nil {
			return invalid("filter.authors: %q is not a login", a)
		}
	}
	return nil
}

func (f *Filter) refuseRunFields() error {
	switch {
	case f.Workflow != "":
		return invalid("filter.workflow is only used by run_concluded")
	case len(f.Result) > 0:
		return invalid("filter.result is only used by run_concluded")
	case len(f.RunTriggers) > 0:
		return invalid("filter.run_triggers is only used by run_concluded")
	}
	return nil
}

func (f *Filter) normalizeRun(family forgeapi.Family) error {
	if f.Workflow != "" && family == forgeapi.FamilyGitLab {
		return invalid("filter.workflow: GitLab runs one pipeline configuration per project")
	}
	if err := checkText("filter.workflow", f.Workflow); err != nil {
		return err
	}
	if len(f.Result) == 0 {
		return invalid("filter.result is required for run_concluded")
	}
	if !subset(f.Result, runResults) {
		return invalid("filter.result must be a subset of success, failure, neutral")
	}
	if len(f.RunTriggers) == 0 {
		f.RunTriggers = slices.Clone(defaultRunTriggers)
	}
	if !subset(f.RunTriggers, runTriggers) {
		return invalid("filter.run_triggers must be a subset of push, schedule, manual, pull_request, other")
	}
	return nil
}

func subset(got, allowed []string) bool {
	for _, v := range got {
		if !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

func (e *EventSpec) clone() *EventSpec {
	c := *e
	if e.Hold != nil {
		h := *e.Hold
		c.Hold = &h
	}
	c.Concurrency = slices.Clone(e.Concurrency)
	c.CooldownMin = slices.Clone(e.CooldownMin)
	c.Filter.Authors = slices.Clone(e.Filter.Authors)
	c.Filter.Result = slices.Clone(e.Filter.Result)
	c.Filter.RunTriggers = slices.Clone(e.Filter.RunTriggers)
	return &c
}
