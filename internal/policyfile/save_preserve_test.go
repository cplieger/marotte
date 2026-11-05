package policyfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// kiroCLIFile is the shape kiro-cli keeps in a workspace-root permissions.yaml: its "Trust this
// workspace?" record (workspaceTrusted, workspaceTrustCase) and its policies array beside rules.
const kiroCLIFile = `# kept by hand: header comment
workspaceTrusted: true
workspaceTrustCase: exact
# the rules marotte edits
rules:
  - capability: shell
    effect: ask
policies:
  - id: corp-baseline # inline policy comment
    source: admin
someFutureKey:
  nested: value
# trailing comment
`

func TestSave_KeepsKeysAndCommentsItDoesNotOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte(kiroCLIFile), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := f.Upsert(&Rule{Capability: "fs_write", Effect: EffectAllow, Match: []string{"src/**"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := Save(t.Context(), path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("saved file does not parse: %v\n%s", err, out)
	}
	t.Run("workspaceTrusted survives", func(t *testing.T) {
		if doc["workspaceTrusted"] != true {
			t.Errorf("workspaceTrusted = %#v, want true\n%s", doc["workspaceTrusted"], out)
		}
	})
	t.Run("workspaceTrustCase survives", func(t *testing.T) {
		if doc["workspaceTrustCase"] != "exact" {
			t.Errorf("workspaceTrustCase = %#v, want exact\n%s", doc["workspaceTrustCase"], out)
		}
	})
	t.Run("policies survives", func(t *testing.T) {
		pol, ok := doc["policies"].([]any)
		if !ok || len(pol) != 1 {
			t.Fatalf("policies = %#v, want a one-entry list\n%s", doc["policies"], out)
		}
		entry, _ := pol[0].(map[string]any)
		if entry["id"] != "corp-baseline" || entry["source"] != "admin" {
			t.Errorf("policies[0] = %#v\n%s", pol[0], out)
		}
	})
	t.Run("an unknown key survives", func(t *testing.T) {
		nested, _ := doc["someFutureKey"].(map[string]any)
		if nested["nested"] != "value" {
			t.Errorf("someFutureKey = %#v\n%s", doc["someFutureKey"], out)
		}
	})
	t.Run("comments survive", func(t *testing.T) {
		for _, c := range []string{
			"# kept by hand: header comment", "# the rules marotte edits",
			"# inline policy comment", "# trailing comment",
		} {
			if !strings.Contains(out, c) {
				t.Errorf("comment %q lost\n%s", c, out)
			}
		}
	})
	t.Run("key order survives", func(t *testing.T) {
		keys := []string{"workspaceTrusted:", "workspaceTrustCase:", "rules:", "policies:", "someFutureKey:"}
		last := -1
		for _, k := range keys {
			i := strings.Index(out, k)
			if i <= last {
				t.Fatalf("key %q out of order (at %d, previous at %d)\n%s", k, i, last, out)
			}
			last = i
		}
	})
	t.Run("rules carry the edit", func(t *testing.T) {
		back, err := Load(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		want := []Rule{
			{Capability: "shell", Effect: EffectAsk},
			{Capability: "fs_write", Effect: EffectAllow, Match: []string{"src/**"}},
		}
		if len(back.Rules) != len(want) {
			t.Fatalf("rules = %+v, want %+v", back.Rules, want)
		}
		for i := range want {
			if signature(&back.Rules[i]) != signature(&want[i]) {
				t.Errorf("rules[%d] = %+v, want %+v", i, back.Rules[i], want[i])
			}
		}
	})
}

// todaysShape is yaml.v3's Marshal of a File holding one rule, the format a rules-only file keeps.
const todaysShape = `rules:
    - capability: fs_write
      effect: allow
      match:
        - src/**
`

func TestSave_RulesOnlyFileIsByteIdentical(t *testing.T) {
	cases := []struct {
		name   string
		before *string // nil: no file
		edit   func(f *File)
		want   string
	}{
		{
			name:   "adding a rule",
			before: new(todaysShape),
			edit: func(f *File) {
				f.Rules = append(f.Rules, Rule{Capability: "shell", Effect: EffectDeny, Match: []string{"*", "rm -rf /"}})
			},
			want: todaysShape + "    - capability: shell\n      effect: deny\n      match:\n        - '*'\n        - rm -rf /\n",
		},
		{
			name:   "changing an effect",
			before: new(todaysShape),
			edit:   func(f *File) { f.ReplaceEffect(&f.Rules[0], EffectAsk) },
			want:   "rules:\n    - capability: fs_write\n      effect: ask\n      match:\n        - src/**\n",
		},
		{
			name:   "removing the last rule",
			before: new(todaysShape),
			edit:   func(f *File) { f.Remove(&f.Rules[0]) },
			want:   "rules: []\n",
		},
		{
			name: "a missing file",
			edit: func(f *File) {
				f.Rules = append(f.Rules, Rule{Capability: "fs_write", Effect: EffectAllow, Match: []string{"src/**"}})
			},
			want: todaysShape,
		},
		{
			name:   "a blank file",
			before: new("  \n"),
			edit: func(f *File) {
				f.Rules = append(f.Rules, Rule{Capability: "fs_write", Effect: EffectAllow, Match: []string{"src/**"}})
			},
			want: todaysShape,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "permissions.yaml")
			if tc.before != nil {
				if err := os.WriteFile(path, []byte(*tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.edit(f)
			if err := Save(t.Context(), path, f); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("Save wrote\n%s\nwant\n%s", got, tc.want)
			}
			legacy, err := yaml.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(legacy) {
				t.Errorf("Save wrote\n%s\nyaml.Marshal of the same File gives\n%s", got, legacy)
			}
		})
	}
}

func saveOne(t *testing.T, before string, edit func(f *File)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	edit(f)
	if err := Save(t.Context(), path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load of the saved file: %v\n%s", err, got)
	}
	return string(got)
}

func addShell(f *File) { f.Rules = append(f.Rules, Rule{Capability: "shell", Effect: EffectAsk}) }

func TestSave_KeepsAnUnchangedRuleAsWritten(t *testing.T) {
	before := "rules:\n  # why fs_write is open\n  - capability: fs_write\n    effect: allow # reviewed\n    description: kept by kiro-cli\n    match: [\"src/**\"]\n"
	got := saveOne(t, before, addShell)
	for _, want := range []string{"# why fs_write is open", "# reviewed", "description: kept by kiro-cli", "capability: shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("saved file lacks %q\n%s", want, got)
		}
	}
}

func TestSave_DropsTheCommentOfARemovedRule(t *testing.T) {
	before := "rules:\n    # about shell\n    - capability: shell\n      effect: ask\n"
	got := saveOne(t, before, func(f *File) { f.Remove(&f.Rules[0]) })
	if got != "rules: []\n" {
		t.Errorf("saved file = %q, want %q", got, "rules: []\n")
	}
}

// collidingRules holds a pattern with a comma beside the same patterns split, which a
// comma-joined key cannot tell apart.
const collidingRules = `rules:
    - capability: shell
      effect: deny
      description: FIRST
      match: ["a,b"]
    - capability: shell
      effect: deny
      description: SECOND
      match: ["a", "b"]
`

func TestSave_WritesExactlyTheRulesItWasGiven(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *File)
	}{
		{"removing the first of a colliding pair", func(f *File) { f.Remove(&f.Rules[0]) }},
		{"asking on the first of a colliding pair", func(f *File) { f.ReplaceEffect(&f.Rules[0], EffectAsk) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "permissions.yaml")
			if err := os.WriteFile(path, []byte(collidingRules), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.edit(f)
			want := slices.Clone(f.Rules)
			if err := Save(t.Context(), path, f); err != nil {
				t.Fatalf("Save: %v", err)
			}
			back, err := Load(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if !slices.EqualFunc(back.Rules, want, func(a, b Rule) bool {
				return a.Capability == b.Capability && a.Effect == b.Effect &&
					slices.Equal(a.Match, b.Match) && slices.Equal(a.Exclude, b.Exclude)
			}) {
				t.Errorf("reloaded rules = %+v, want %+v", back.Rules, want)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "description: SECOND") {
				t.Errorf("the unchanged deny rule lost its description\n%s", raw)
			}
		})
	}
}

func TestSave_RemovingOneOfTwoDuplicatesKeepsNeitherNode(t *testing.T) {
	before := "rules:\n    # c1\n    - capability: shell\n      effect: deny\n      description: FIRST\n" +
		"    # c2\n    - capability: shell\n      effect: deny\n      description: SECOND\n"
	got := saveOne(t, before, func(f *File) { f.Remove(&f.Rules[0]) })
	for _, gone := range []string{"description: FIRST", "# c1"} {
		if strings.Contains(got, gone) {
			t.Errorf("saved file still holds the removed rule's %q\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "capability: shell") {
		t.Errorf("saved file lost the surviving rule\n%s", got)
	}
}

func TestSave_KeepsTwoIdenticalRulesThroughAnUnrelatedEdit(t *testing.T) {
	before := "rules:\n    # c1\n    - capability: shell\n      effect: deny\n      description: FIRST\n" +
		"    # c2\n    - capability: shell\n      effect: deny\n      description: SECOND\n"
	got := saveOne(t, before, func(f *File) {
		f.Rules = append(f.Rules, Rule{Capability: "fs_read", Effect: EffectAllow})
	})
	first, second := strings.Index(got, "description: FIRST"), strings.Index(got, "description: SECOND")
	if first < 0 || second < first {
		t.Errorf("descriptions missing or out of order (FIRST at %d, SECOND at %d)\n%s", first, second, got)
	}
	c1, c2 := strings.Index(got, "# c1"), strings.Index(got, "# c2")
	if c1 < 0 || c2 < c1 {
		t.Errorf("comments missing or out of order (# c1 at %d, # c2 at %d)\n%s", c1, c2, got)
	}
}

// A v4 encoder folds a plain scalar at 80 columns; kiro-cli's long values must stay on one line.
func TestSave_KeepsLongValuesOnOneLine(t *testing.T) {
	longValue := strings.Repeat("word ", 30) + "end"
	longPattern := strings.Repeat("dir/", 30) + "**"
	unknown := "someFutureKey: " + longValue + "\n"
	got := saveOne(t, unknown+"rules: []\n", func(f *File) {
		f.Rules = append(f.Rules, Rule{Capability: "fs_write", Effect: EffectAllow, Match: []string{longPattern}})
	})
	want := unknown + "rules:\n    - capability: fs_write\n      effect: allow\n      match:\n        - " + longPattern + "\n"
	if got != want {
		t.Errorf("saved file = %q, want %q", got, want)
	}
}

func TestSave_DocumentShapes(t *testing.T) {
	t.Run("a comment on an empty rules list stays on its line", func(t *testing.T) {
		before := "rules: [] # none yet\nnext: 1\n"
		if got := saveOne(t, before, func(*File) {}); got != before {
			t.Errorf("saved file = %q, want %q", got, before)
		}
	})
	t.Run("a comment on an empty rules list moves onto the key when a rule is added", func(t *testing.T) {
		got := saveOne(t, "rules: [] # none yet\nnext: 1\n", addShell)
		want := "rules: # none yet\n    - capability: shell\n      effect: ask\nnext: 1\n"
		if got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	t.Run("a comment on a null rules value stays on the rules line", func(t *testing.T) {
		got := saveOne(t, "rules: ~ # none yet\nnext: 1\n", func(*File) {})
		if want := "rules: [] # none yet\nnext: 1\n"; got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	t.Run("a comment on the rules key stays when the last rule goes", func(t *testing.T) {
		got := saveOne(t, "rules: # mine\n    - capability: shell\n      effect: ask\nnext: 1\n",
			func(f *File) { f.Remove(&f.Rules[0]) })
		if want := "rules: [] # mine\nnext: 1\n"; got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	t.Run("a comment-only file keeps its comments", func(t *testing.T) {
		got := saveOne(t, "# managed by hand\n\n# second line\n", addShell)
		want := "# managed by hand\n\n# second line\nrules:\n    - capability: shell\n      effect: ask\n"
		if got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	t.Run("a null document becomes a rules mapping", func(t *testing.T) {
		got := saveOne(t, "~\n", addShell)
		if got != "rules:\n    - capability: shell\n      effect: ask\n" {
			t.Errorf("saved file = %q", got)
		}
	})
	t.Run("a comment beside a null document stays above the rules", func(t *testing.T) {
		got := saveOne(t, "~ # side\n", addShell)
		if want := "# side\nrules:\n    - capability: shell\n      effect: ask\n"; got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	t.Run("comments above and beside a null document both stay", func(t *testing.T) {
		got := saveOne(t, "# top\n~ # side\n", addShell)
		if want := "# top\n# side\nrules:\n    - capability: shell\n      effect: ask\n"; got != want {
			t.Errorf("saved file = %q, want %q", got, want)
		}
	})
	for name, before := range map[string]string{
		"comments around a document marker": "# c\n---\n# d\n",
		"a comment on the document marker":  "--- # c\n",
		"a directive":                       "%YAML 1.1\n---\n",
	} {
		t.Run(name+" stays above the rules", func(t *testing.T) {
			got := saveOne(t, before, addShell)
			if want := before + "rules:\n    - capability: shell\n      effect: ask\n"; got != want {
				t.Errorf("saved file = %q, want %q", got, want)
			}
		})
	}
	for name, c := range map[string]struct{ before, head string }{
		"a document end after a marker":    {"---\n...\n", "---\n"},
		"comments around both markers":     {"# c\n---\n# d\n...\n", "# c\n---\n# d\n"},
		"a comment on the document end":    {"---\n... # e\n", "---\n# e\n"},
		"a comment after the document end": {"---\n...\n# f\n", "---\n# f\n"},
	} {
		t.Run(name+" saves without its end marker", func(t *testing.T) {
			got := saveOne(t, c.before, addShell)
			if want := c.head + "rules:\n    - capability: shell\n      effect: ask\n"; got != want {
				t.Errorf("saved file = %q, want %q", got, want)
			}
		})
	}
	for name, c := range map[string]struct{ before, want string }{
		"a comment on the document end":                 {"rules: []\n... # end\n", "rules: []\n\n# end\n"},
		"comments on and after the document end":        {"rules: []\n... # e\n# f\n", "rules: []\n\n# e\n# f\n"},
		"a foot comment above a commented document end": {"next: 1\nrules: []\n# foot\n... # end\n", "next: 1\nrules: []\n# foot\n# end\n"},
	} {
		t.Run(name+" after a mapping keeps its comments", func(t *testing.T) {
			if got := saveOne(t, c.before, func(*File) {}); got != c.want {
				t.Errorf("saved file = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("a file with no rules key gains one after its other keys", func(t *testing.T) {
		got := saveOne(t, "workspaceTrusted: true\n", addShell)
		if got != "workspaceTrusted: true\nrules:\n    - capability: shell\n      effect: ask\n" {
			t.Errorf("saved file = %q", got)
		}
	})
	t.Run("a JSON file becomes block YAML and keeps its other keys", func(t *testing.T) {
		got := saveOne(t, `{"workspaceTrusted": true, "rules": []}`, addShell)
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
			t.Fatalf("saved file does not parse: %v\n%s", err, got)
		}
		if doc["workspaceTrusted"] != true {
			t.Errorf("workspaceTrusted lost\n%s", got)
		}
		if strings.HasPrefix(strings.TrimSpace(got), "{") || !strings.Contains(got, "\n    - capability: shell\n") {
			t.Errorf("saved file is not block YAML\n%s", got)
		}
	})
}

func TestRulesNode_RefusesAnItemThatIsNotARule(t *testing.T) {
	var prev yaml.Node
	if err := yaml.Unmarshal([]byte("[not a rule]"), &prev); err != nil {
		t.Fatal(err)
	}
	if _, err := rulesNode(prev.Content[0], []Rule{{Capability: "shell", Effect: EffectAsk}}); err == nil {
		t.Error("rulesNode over an undecodable item returned nil")
	}
}

func TestSave_NeverOverwritesAFileLoadRefuses(t *testing.T) {
	cases := map[string]string{
		"malformed":         "rules: [ this is : not : valid",
		"a second document": "rules: []\n---\nrules:\n  - capability: shell\n    effect: allow\n",
		"a sequence root":   "- capability: shell\n  effect: allow\n",
		"a scalar root":     "allow everything\n",
		"rules not a list":  "rules: allow-everything\n",
		"a duplicated key":  "rules: []\nrules: []\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "permissions.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Save(t.Context(), path, &File{Rules: []Rule{{Capability: "shell", Effect: EffectAsk}}}); err == nil {
				t.Errorf("Save over a file Load refuses returned nil")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != body {
				t.Errorf("file changed to %q", got)
			}
		})
	}
}

func TestSave_RefusesAnEditThatBreaksTheDocument(t *testing.T) {
	// An alias elsewhere into the rules sequence loses its anchor when the sequence is replaced.
	body := "rules: &r\n    - capability: shell\n      effect: ask\ncopy: *r\n"
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addShell(f)
	if err := Save(t.Context(), path, f); err == nil {
		t.Errorf("Save wrote a document with a dangling alias")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("file changed to %q", got)
	}
}

// jsonSibling is a permissions.json KAS reads while the .yaml is missing or blank, holding the
// keys and a rule field marotte does not model.
const jsonSibling = `{"workspaceTrusted": true, "policies": ["read-all"],
 "rules": [{"capability": "shell", "effect": "deny", "match": ["rm *"], "description": "kept"}],
 "note": "true"}`

func TestSave_WritesTheYAMLFromTheJSONSibling(t *testing.T) {
	// The .json's keys in its order, its rule as written, the new rule, all in block YAML; the
	// string "true" stays a string.
	const want = `workspaceTrusted: true
policies:
    - read-all
rules:
    - capability: shell
      effect: deny
      match:
        - rm *
      description: kept
    - capability: web_fetch
      effect: allow
note: "true"
`
	for name, yamlBefore := range map[string]*string{"yaml absent": nil, "yaml blank": new("  \n")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			jsonPath, yamlPath := filepath.Join(dir, jsonFilename), filepath.Join(dir, filename)
			if err := os.WriteFile(jsonPath, []byte(jsonSibling), 0o600); err != nil {
				t.Fatal(err)
			}
			if yamlBefore != nil {
				if err := os.WriteFile(yamlPath, []byte(*yamlBefore), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f, err := Load(yamlPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := f.Upsert(&Rule{Capability: "web_fetch", Effect: EffectAllow}); err != nil {
				t.Fatal(err)
			}
			if err := Save(t.Context(), yamlPath, f); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := os.ReadFile(yamlPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("saved .yaml = %q, want %q", got, want)
			}
			if raw, err := os.ReadFile(jsonPath); err != nil || string(raw) != jsonSibling {
				t.Errorf("the .json changed to %q (err %v), want it left as KAS's fallback", raw, err)
			}
		})
	}
}

func TestSave_NeverShadowsAJSONSiblingLoadRefuses(t *testing.T) {
	dir := t.TempDir()
	jsonPath, yamlPath := filepath.Join(dir, jsonFilename), filepath.Join(dir, filename)
	const broken = `{"rules": [`
	if err := os.WriteFile(jsonPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(t.Context(), yamlPath, &File{Rules: []Rule{{Capability: "shell", Effect: EffectAsk}}}); err == nil {
		t.Error("Save over an unparseable .json returned nil")
	}
	if _, err := os.Stat(yamlPath); !os.IsNotExist(err) {
		t.Errorf("Save wrote a .yaml that would shadow the .json (stat err %v)", err)
	}
}
