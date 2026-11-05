package launchrule

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clockRule(source string) *Rule {
	return &Rule{Source: source, Name: "demo", Enabled: true, When: When{Clock: &Spec{Freq: FreqDaily, Hour: 2}}}
}

func eventRule(source string, ev *EventSpec) *Rule {
	return &Rule{Source: source, Name: "demo", Enabled: true, When: When{Event: ev}}
}

func runSpec() *EventSpec {
	return &EventSpec{
		Connection: "github:github.com", Repo: "cplieger/animap-source", Kind: KindRunConcluded,
		Filter: Filter{Workflow: ".github/workflows/weekly.yaml", Branch: "main", Result: []string{"success"}},
	}
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func readRules(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, rulesFileName))
	if err != nil {
		t.Fatalf("read %s: %v", rulesFileName, err)
	}
	return raw
}

func TestTwoRulesOneRecipe(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	clock, err := st.Create(t.Context(), clockRule("bundled://weekly"))
	if err != nil {
		t.Fatalf("Create clock rule: %v", err)
	}
	event, err := st.Create(t.Context(), eventRule("bundled://weekly", runSpec()))
	if err != nil {
		t.Fatalf("Create event rule: %v", err)
	}
	t.Run("ids are minted and distinct", func(t *testing.T) {
		if clock.ID == event.ID {
			t.Errorf("both rules got id %q", clock.ID)
		}
		for _, id := range []string{clock.ID, event.ID} {
			if len(id) != 22 || strings.ContainsAny(id, "+/=") {
				t.Errorf("id %q is not 16 bytes of base64url", id)
			}
		}
	})
	t.Run("both survive a reload", func(t *testing.T) {
		got := openStore(t, dir).List()
		if len(got) != 2 || got[0].ID != clock.ID || got[1].ID != event.ID {
			t.Fatalf("reloaded rules = %+v, want the clock rule then the event rule", got)
		}
		if got[1].When.Event == nil || got[1].When.Event.Repo != "cplieger/animap-source" {
			t.Errorf("reloaded event rule's when = %+v", got[1].When)
		}
	})
	t.Run("the file is 0600", func(t *testing.T) {
		fi, err := os.Stat(filepath.Join(dir, rulesFileName))
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("mode = %o, want 600", mode)
		}
	})
	t.Run("a client id is ignored on create", func(t *testing.T) {
		r := clockRule("bundled://weekly")
		r.ID = clock.ID
		third, err := st.Create(t.Context(), r)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if third.ID == clock.ID {
			t.Errorf("Create kept the client's id %q", r.ID)
		}
	})
}

func TestEventSpecDefaults(t *testing.T) {
	st := openStore(t, t.TempDir())
	run, err := st.Create(t.Context(), eventRule("bundled://x", runSpec()))
	if err != nil {
		t.Fatalf("Create run rule: %v", err)
	}
	ev := run.When.Event
	if ev.IntervalMin != 5 {
		t.Errorf("interval_min = %d, want 5", ev.IntervalMin)
	}
	if got := strings.Join(ev.Filter.RunTriggers, ","); got != "push,schedule,manual" {
		t.Errorf("run_triggers = %q, want push,schedule,manual", got)
	}
	if ev.Hold == nil || *ev.Hold != (Hold{Policy: HoldNewest, Cap: 10}) {
		t.Errorf("run_concluded hold = %+v, want newest cap 10", ev.Hold)
	}
	labeled, err := st.Create(t.Context(), eventRule("bundled://x", &EventSpec{
		Connection: "github:github.com", Repo: "cplieger/animap", Kind: KindIssueLabeled,
		Filter: Filter{Label: "  report:fast "},
	}))
	if err != nil {
		t.Fatalf("Create labeled rule: %v", err)
	}
	if h := labeled.When.Event.Hold; h == nil || *h != (Hold{Policy: HoldFIFO, Cap: 20}) {
		t.Errorf("issue_labeled hold = %+v, want fifo cap 20", h)
	}
	if got := labeled.When.Event.Filter.Label; got != "report:fast" {
		t.Errorf("label = %q, want it trimmed", got)
	}
	if labeled.When.Event.Filter.RunTriggers != nil {
		t.Errorf("issue_labeled got run_triggers %v", labeled.When.Event.Filter.RunTriggers)
	}
}

// eventCase is one TestEventSpecValidation row: edit mutates a valid spec, want is the
// sentence the refusal carries, "" for a spec the store must accept.
type eventCase struct {
	edit func(*EventSpec)
	name string
	want string
}

func eventCases() []eventCase {
	gitlab := func(e *EventSpec) { e.Connection, e.Repo = "gitlab:gitlab.com", "group/sub/project" }
	kind := func(k Kind, f Filter) func(*EventSpec) {
		return func(e *EventSpec) { e.Kind, e.Filter = k, f }
	}
	authors := make([]string, 51)
	for i := range authors {
		authors[i] = "user" + strings.Repeat("x", i)
	}
	return []eventCase{
		{name: "the base spec", edit: func(*EventSpec) {}},
		{name: "connection missing", edit: func(e *EventSpec) { e.Connection = "" }, want: "connection is required"},
		{name: "connection not an id", edit: func(e *EventSpec) { e.Connection = "github.com" }, want: `connection "github.com" is not a forge connection`},
		{name: "connection of an unknown kind", edit: func(e *EventSpec) { e.Connection = "svn:svn.example.org" }, want: "is not a forge connection"},
		{name: "connection with a control character", edit: func(e *EventSpec) { e.Connection = "github:git\x07hub.com" }, want: "connection has a control character"},
		{name: "repo missing", edit: func(e *EventSpec) { e.Repo = "" }, want: "repo is required"},
		{name: "repo with no owner", edit: func(e *EventSpec) { e.Repo = "animap" }, want: "repo: repository selector names no owner"},
		{name: "repo with a group path on github", edit: func(e *EventSpec) { e.Repo = "a/b/c" }, want: "repo: repository selector carries a separator"},
		{name: "repo with a group path on gitlab", edit: func(e *EventSpec) { gitlab(e); e.Filter.Workflow = "" }},
		{name: "kind missing", edit: func(e *EventSpec) { e.Kind = "" }, want: "kind must be one of issue_labeled, pr_labeled, issue_opened, pr_opened, run_concluded"},
		{name: "kind unknown", edit: func(e *EventSpec) { e.Kind = "push" }, want: "kind must be one of"},
		{name: "label missing", edit: kind(KindIssueLabeled, Filter{}), want: "filter.label is required for issue_labeled"},
		{name: "label blank", edit: kind(KindPRLabeled, Filter{Label: "   "}), want: "filter.label is required for pr_labeled"},
		{name: "label over 50 bytes", edit: kind(KindIssueLabeled, Filter{Label: strings.Repeat("l", 51)}), want: "filter.label must be 1 to 50 bytes"},
		{name: "label with a control character", edit: kind(KindIssueLabeled, Filter{Label: "re\nport"}), want: "filter.label has a control character"},
		{name: "label on an opened kind", edit: kind(KindIssueOpened, Filter{Label: "x", Authors: []string{"a"}}), want: "filter.label is only used by issue_labeled and pr_labeled"},
		{name: "label on run_concluded", edit: func(e *EventSpec) { e.Filter.Label = "x" }, want: "filter.label is only used by issue_labeled and pr_labeled"},
		{name: "authors missing", edit: kind(KindPROpened, Filter{}), want: "filter.authors is required for pr_opened"},
		{name: "authors over 50", edit: kind(KindIssueOpened, Filter{Authors: authors}), want: "filter.authors holds at most 50 logins"},
		{name: "author not a login", edit: kind(KindIssueOpened, Filter{Authors: []string{"ok", "not a login"}}), want: `filter.authors: "not a login" is not a login`},
		{name: "author with a path", edit: func(e *EventSpec) {
			gitlab(e)
			e.Kind, e.Filter = KindIssueOpened, Filter{Authors: []string{"group/user"}}
		}, want: `filter.authors: "group/user" is not a login`},
		{name: "authors on a labeled kind", edit: kind(KindPRLabeled, Filter{Label: "x", Authors: []string{"a"}}), want: "filter.authors is only used by issue_opened and pr_opened"},
		{name: "branch on an issue kind", edit: kind(KindIssueOpened, Filter{Authors: []string{"a"}, Branch: "main"}), want: "filter.branch is not used by issue_opened"},
		{name: "branch on a pr kind", edit: kind(KindPROpened, Filter{Authors: []string{"a"}, Branch: "main"})},
		{name: "branch with a control character", edit: func(e *EventSpec) { e.Filter.Branch = "ma\x00in" }, want: "filter.branch has a control character"},
		{name: "branch over 512 bytes", edit: func(e *EventSpec) { e.Filter.Branch = strings.Repeat("b", 513) }, want: "filter.branch is longer than 512 bytes"},
		{name: "branch with pull request runs on forgejo", edit: func(e *EventSpec) {
			e.Connection, e.Filter.Workflow = "codeberg:codeberg.org", "weekly.yaml"
			e.Filter.RunTriggers = []string{"push", "pull_request"}
		}},
		{name: "workflow on a pr kind", edit: kind(KindPROpened, Filter{Authors: []string{"a"}, Workflow: "ci.yaml"}), want: "filter.workflow is only used by run_concluded"},
		{name: "workflow on gitlab", edit: gitlab, want: "filter.workflow: GitLab runs one pipeline configuration per project"},
		{name: "workflow with a control character", edit: func(e *EventSpec) { e.Filter.Workflow = "ci\t.yaml" }, want: "filter.workflow has a control character"},
		{name: "result missing", edit: func(e *EventSpec) { e.Filter.Result = nil }, want: "filter.result is required for run_concluded"},
		{name: "result unknown", edit: func(e *EventSpec) { e.Filter.Result = []string{"success", "cancelled"} }, want: "filter.result must be a subset of success, failure, neutral"},
		{name: "result on an issue kind", edit: kind(KindIssueLabeled, Filter{Label: "x", Result: []string{"success"}}), want: "filter.result is only used by run_concluded"},
		{name: "run trigger unknown", edit: func(e *EventSpec) { e.Filter.RunTriggers = []string{"push", "unknown"} }, want: "filter.run_triggers must be a subset of push, schedule, manual, pull_request, other"},
		{name: "run triggers on a pr kind", edit: kind(KindPROpened, Filter{Authors: []string{"a"}, RunTriggers: []string{"push"}}), want: "filter.run_triggers is only used by run_concluded"},
		{name: "interval below 2", edit: func(e *EventSpec) { e.IntervalMin = 1 }, want: "interval_min must be 2 to 60"},
		{name: "interval above 60", edit: func(e *EventSpec) { e.IntervalMin = 61 }, want: "interval_min must be 2 to 60"},
		{name: "interval of 60", edit: func(e *EventSpec) { e.IntervalMin = 60 }},
		{name: "hold policy unknown", edit: func(e *EventSpec) { e.Hold = &Hold{Policy: "lifo", Cap: 5} }, want: "hold.policy must be newest or fifo"},
		{name: "hold cap zero", edit: func(e *EventSpec) { e.Hold = &Hold{Policy: HoldFIFO} }, want: "hold.cap must be 1 to 50"},
		{name: "hold cap over 50", edit: func(e *EventSpec) { e.Hold = &Hold{Policy: HoldNewest, Cap: 51} }, want: "hold.cap must be 1 to 50"},
		{name: "concurrency reserved", edit: func(e *EventSpec) { e.Concurrency = json.RawMessage(`2`) }, want: "concurrency is not supported yet"},
		{name: "cooldown reserved", edit: func(e *EventSpec) { e.CooldownMin = json.RawMessage(`10`) }, want: "cooldown_min is not supported yet"},
	}
}

func TestEventSpecValidation(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	existing, err := st.Create(t.Context(), eventRule("bundled://x", runSpec()))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, tc := range eventCases() {
		t.Run(tc.name, func(t *testing.T) {
			before := readRules(t, dir)
			spec := runSpec()
			tc.edit(spec)
			_, createErr := st.Create(t.Context(), eventRule("bundled://x", spec))
			spec = runSpec()
			tc.edit(spec)
			_, replaceErr := st.Replace(t.Context(), existing.ID, eventRule("bundled://x", spec))
			for verb, err := range map[string]error{"Create": createErr, "Replace": replaceErr} {
				if tc.want == "" {
					if err != nil {
						t.Errorf("%s refused a valid spec: %v", verb, err)
					}
					continue
				}
				var ve *ValidationError
				if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s error = %v, want a ValidationError carrying %q", verb, err, tc.want)
				}
			}
			if tc.want != "" && !bytes.Equal(before, readRules(t, dir)) {
				t.Errorf("a refused write changed %s", rulesFileName)
			}
		})
	}
}

func TestRuleValidation(t *testing.T) {
	cases := []struct {
		edit func(*Rule)
		name string
		want string
	}{
		{name: "source missing", edit: func(r *Rule) { r.Source = "" }, want: "source is required"},
		{name: "source with a control character", edit: func(r *Rule) { r.Source = "bundled://a\x1b" }, want: "source has a control character"},
		{name: "model over 512 bytes", edit: func(r *Rule) { r.Model = strings.Repeat("m", 513) }, want: "model is longer than 512 bytes"},
		{name: "when empty", edit: func(r *Rule) { r.When = When{} }, want: "when must hold exactly one of clock and event"},
		{name: "when holding both", edit: func(r *Rule) { r.When.Event = runSpec() }, want: "when must hold exactly one of clock and event"},
		{name: "clock spec invalid", edit: func(r *Rule) { r.When.Clock.Minute = 61 }, want: "when.clock: minute 61 out of range 0-59"},
		{name: "input name empty", edit: func(r *Rule) { r.Inputs = map[string]string{"": "x"} }, want: "inputs: a name is empty"},
		{name: "input value over 4 KiB", edit: func(r *Rule) { r.Inputs = map[string]string{"notes": strings.Repeat("v", 4097)} }, want: "inputs.notes is longer than 4096 bytes"},
		{name: "input value with a control character", edit: func(r *Rule) { r.Inputs = map[string]string{"notes": "a\x07b"} }, want: "inputs.notes has a control character"},
		{name: "input value with tab and newline", edit: func(r *Rule) { r.Inputs = map[string]string{"notes": "a\tb\nc"} }},
	}
	dir := t.TempDir()
	st := openStore(t, dir)
	if _, err := st.Create(t.Context(), clockRule("bundled://seed")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := readRules(t, dir)
			r := clockRule("bundled://x")
			tc.edit(r)
			_, err := st.Create(t.Context(), r)
			if tc.want == "" {
				if err != nil {
					t.Errorf("Create refused a valid rule: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Create error = %v, want a ValidationError carrying %q", err, tc.want)
			}
			if !bytes.Equal(before, readRules(t, dir)) {
				t.Errorf("a refused write changed %s", rulesFileName)
			}
		})
	}
}

func TestFixedInputNamedLikeEventInputRefused(t *testing.T) {
	cases := []struct {
		spec  func() *EventSpec
		name  string
		input string
	}{
		{name: "run_id on run_concluded", spec: runSpec, input: "run_id"},
		{name: "event_id on run_concluded", spec: runSpec, input: "event_id"},
		{name: "repo on run_concluded", spec: runSpec, input: "repo"},
		{name: "head_sha on run_concluded", spec: runSpec, input: "head_sha"},
		{name: "issue_number on issue_labeled", input: "issue_number", spec: func() *EventSpec {
			return &EventSpec{Connection: "github:github.com", Repo: "o/r", Kind: KindIssueLabeled, Filter: Filter{Label: "x"}}
		}},
		{name: "author on pr_opened", input: "author", spec: func() *EventSpec {
			return &EventSpec{Connection: "github:github.com", Repo: "o/r", Kind: KindPROpened, Filter: Filter{Authors: []string{"a"}}}
		}},
	}
	st := openStore(t, t.TempDir())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := eventRule("bundled://x", tc.spec())
			r.Inputs = map[string]string{tc.input: "7"}
			_, err := st.Create(t.Context(), r)
			want := "`" + tc.input + "` is set by the event"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Create error = %v, want %q", err, want)
			}
		})
	}
	t.Run("a name the kind does not set", func(t *testing.T) {
		r := eventRule("bundled://x", runSpec())
		r.Inputs = map[string]string{"issue_number": "7", "lane": "fast"}
		if _, err := st.Create(t.Context(), r); err != nil {
			t.Errorf("Create refused inputs the kind does not set: %v", err)
		}
	})
	t.Run("a clock rule may use any name", func(t *testing.T) {
		r := clockRule("bundled://x")
		r.Inputs = map[string]string{"run_id": "7", "event_id": "e"}
		if _, err := st.Create(t.Context(), r); err != nil {
			t.Errorf("Create refused a clock rule's inputs: %v", err)
		}
	})
}

func TestMalformedFileIsAnError(t *testing.T) {
	cases := map[string]string{
		"not json":          `{"rules": [`,
		"an invalid rule":   `[{"id":"a","source":"bundled://a","enabled":true,"when":{}}]`,
		"a rule with no id": `[{"source":"bundled://a","enabled":true,"when":{"clock":{"freq":"daily","hour":1,"minute":0}}}]`,
		"a duplicate id": `[{"id":"a","source":"bundled://a","enabled":true,"when":{"clock":{"freq":"daily","hour":1,"minute":0}}},` +
			`{"id":"a","source":"bundled://b","enabled":true,"when":{"clock":{"freq":"daily","hour":1,"minute":0}}}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, rulesFileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(dir); err == nil {
				t.Errorf("NewStore accepted %s", name)
			}
			if got := readRules(t, dir); string(got) != body {
				t.Errorf("NewStore rewrote the malformed file: %s", got)
			}
		})
	}
}

func TestReplaceAndDelete(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	r, err := st.Create(t.Context(), clockRule("bundled://x"))
	if err != nil {
		t.Fatal(err)
	}
	edit := clockRule("bundled://x")
	edit.Enabled = false
	got, err := st.Replace(t.Context(), r.ID, edit)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got.ID != r.ID || got.Enabled {
		t.Errorf("Replace = %+v, want id %q disabled", got, r.ID)
	}
	if _, err := st.Replace(t.Context(), "missing", edit); !errors.Is(err, ErrNotFound) {
		t.Errorf("Replace(missing) error = %v, want ErrNotFound", err)
	}
	if err := st.Delete(t.Context(), r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := openStore(t, dir).Get(r.ID); ok {
		t.Errorf("rule %q survived Delete and a reload", r.ID)
	}
	if err := st.Delete(t.Context(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(gone) error = %v, want ErrNotFound", err)
	}
}

func statePath(dir, id string) string {
	return filepath.Join(dir, stateDirName, id+".json")
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func TestClientCannotWriteCursor(t *testing.T) {
	for _, field := range []string{"status", "population", "cursor", "queue", "launched", "superseded", "attempts"} {
		t.Run(field, func(t *testing.T) {
			body := `{"source":"bundled://x","enabled":true,"when":{"clock":{"freq":"daily","hour":2}},"` + field + `":{}}`
			_, err := DecodeRule([]byte(body))
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), field+" is written by the server") {
				t.Errorf("DecodeRule error = %v, want a ValidationError naming %s", err, field)
			}
		})
	}
	t.Run("inside the event spec", func(t *testing.T) {
		body := `{"source":"bundled://x","when":{"event":{"connection":"github:github.com","repo":"o/r",` +
			`"kind":"pr_opened","filter":{"authors":["a"]},"cursor":{}}}}`
		if _, err := DecodeRule([]byte(body)); err == nil || !strings.Contains(err.Error(), "cursor") {
			t.Errorf("DecodeRule error = %v, want the event spec's cursor refused", err)
		}
	})
	t.Run("an unknown field", func(t *testing.T) {
		_, err := DecodeRule([]byte(`{"source":"bundled://x","colour":"red"}`))
		if err == nil || !strings.Contains(err.Error(), `unknown field "colour"`) {
			t.Errorf("DecodeRule error = %v, want the unknown field named", err)
		}
	})
	t.Run("an edit leaves the state file alone", func(t *testing.T) {
		dir := t.TempDir()
		st := openStore(t, dir)
		r, err := st.Create(t.Context(), clockRule("bundled://x"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.WriteState(t.Context(), r.ID, &State{Status: Status{LastRunID: "wf-1"}}); err != nil {
			t.Fatalf("WriteState: %v", err)
		}
		before := readFile(t, statePath(dir, r.ID))
		edit, err := DecodeRule([]byte(`{"source":"bundled://x","enabled":false,"when":{"clock":{"freq":"daily","hour":4}}}`))
		if err != nil {
			t.Fatalf("DecodeRule: %v", err)
		}
		if _, err := st.Replace(t.Context(), r.ID, &edit); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		if !bytes.Equal(before, readFile(t, statePath(dir, r.ID))) {
			t.Errorf("Replace changed the rule's state file")
		}
	})
}

func TestStateWriteTouchesOnlyItsRuleFile(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	a, err := st.Create(t.Context(), clockRule("bundled://a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(t.Context(), eventRule("bundled://b", runSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteState(t.Context(), b.ID, &State{Status: Status{LastRunID: "wf-b"}}); err != nil {
		t.Fatalf("WriteState(b): %v", err)
	}
	rulesBefore, bBefore := readRules(t, dir), readFile(t, statePath(dir, b.ID))
	held := []json.RawMessage{json.RawMessage(`{"key":"k1"}`), json.RawMessage(`{"key":"k2","observed_at":"x"}`)}
	if err := st.WriteState(t.Context(), a.ID, &State{Status: Status{LastRunID: "wf-a"}, Queue: held}); err != nil {
		t.Fatalf("WriteState(a): %v", err)
	}
	t.Run("the definitions are byte-identical", func(t *testing.T) {
		if !bytes.Equal(rulesBefore, readRules(t, dir)) {
			t.Errorf("WriteState changed %s", rulesFileName)
		}
	})
	t.Run("the other rule's file is byte-identical", func(t *testing.T) {
		if !bytes.Equal(bBefore, readFile(t, statePath(dir, b.ID))) {
			t.Errorf("WriteState(a) changed b's state file")
		}
	})
	t.Run("the state reloads", func(t *testing.T) {
		got, err := openStore(t, dir).State(a.ID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if got.Status.LastRunID != "wf-a" || got.Held() != 2 {
			t.Errorf("reloaded state = %+v, want last_run_id wf-a and 2 held", got)
		}
	})
	t.Run("modes", func(t *testing.T) {
		fi, err := os.Stat(statePath(dir, a.ID))
		if err != nil {
			t.Fatal(err)
		}
		di, err := os.Stat(filepath.Join(dir, stateDirName))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("modes = file %o, dir %o, want 600 and 700", fi.Mode().Perm(), di.Mode().Perm())
		}
	})
	t.Run("a new rule's state carries its population", func(t *testing.T) {
		fresh, err := st.Create(t.Context(), eventRule("bundled://c", runSpec()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.State(fresh.ID)
		if err != nil {
			t.Fatal(err)
		}
		// keyenc.Join of the connection, the repository's RepoRef.Encode and the kind; the connection's ':' is escaped.
		want := `github\:github.com:v1.63706c69656765722f616e696d61702d736f75726365:run_concluded`
		if got.Population != want {
			t.Errorf("population = %q, want %q", got.Population, want)
		}
		clock, err := st.State(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if clock.Population != "" {
			t.Errorf("clock rule population = %q, want none", clock.Population)
		}
	})
}

func TestOrphanStateFileRemoved(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	r, err := st.Create(t.Context(), clockRule("bundled://a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteState(t.Context(), r.ID, &State{Status: Status{LastRunID: "wf"}}); err != nil {
		t.Fatal(err)
	}
	orphan := statePath(dir, "AAAAAAAAAAAAAAAAAAAAAA")
	if err := os.WriteFile(orphan, []byte(`{"status":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	openStore(t, dir)
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphan state file survived a load: %v", err)
	}
	if _, err := os.Stat(statePath(dir, r.ID)); err != nil {
		t.Errorf("the live rule's state file is gone: %v", err)
	}
}

func TestUnreadableStateFileBlocksRule(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	r, err := st.Create(t.Context(), eventRule("bundled://a", runSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, stateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"not json":                  `{"status":`,
		"a queue entry with no key": `{"queue":[{"observed_at":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := statePath(dir, r.ID)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			reloaded := openStore(t, dir)
			_, err := reloaded.State(r.ID)
			want := "could not read launch-state/" + r.ID + ".json: "
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("State error = %v, want it to start %q", err, want)
			}
			if err := reloaded.WriteState(t.Context(), r.ID, &State{}); err == nil {
				t.Errorf("WriteState overwrote an unreadable state file")
			}
			if _, err := reloaded.Replace(t.Context(), r.ID, eventRule("bundled://a", runSpec())); err != nil {
				t.Errorf("Replace of a blocked rule: %v", err)
			}
			if got := readFile(t, path); string(got) != body {
				t.Errorf("the unreadable file changed to %s", got)
			}
		})
	}
}

func TestDeleteRuleRemovesStateFile(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	r, err := st.Create(t.Context(), clockRule("bundled://a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteState(t.Context(), r.ID, &State{Status: Status{LastRunID: "wf"}}); err != nil {
		t.Fatal(err)
	}
	t.Run("delete removes it", func(t *testing.T) {
		if err := st.Delete(t.Context(), r.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(statePath(dir, r.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("state file after Delete: %v", err)
		}
	})
	t.Run("a failed removal is retried at the next load", func(t *testing.T) {
		r2, err := st.Create(t.Context(), clockRule("bundled://b"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.WriteState(t.Context(), r2.ID, &State{Status: Status{LastRunID: "wf"}}); err != nil {
			t.Fatal(err)
		}
		prev := removeFile
		removeFile = func(string) error { return errors.New("injected") }
		t.Cleanup(func() { removeFile = prev })
		if err := st.Delete(t.Context(), r2.ID); err != nil {
			t.Fatalf("Delete with a failing removal: %v", err)
		}
		if _, ok := st.Get(r2.ID); ok {
			t.Errorf("the rule survived Delete")
		}
		if _, err := os.Stat(statePath(dir, r2.ID)); err != nil {
			t.Fatalf("the injected failure left no file to retry: %v", err)
		}
		removeFile = prev
		openStore(t, dir)
		if _, err := os.Stat(statePath(dir, r2.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the next load left the deleted rule's state file: %v", err)
		}
	})
}
