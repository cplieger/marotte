package command

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/rule"
)

func ruleSpecForValidation() rule.Spec {
	return rule.Spec{
		Name: "Check in", Prompt: "Look at the open pull requests.", Enabled: true,
		Triggers: []rule.Trigger{{ID: "new:a", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 60}}},
	}
}

func TestValidateRuleSpec_AcceptsAValidSpec(t *testing.T) {
	if errs := ValidateRuleSpec(ruleSpecForValidation(), rule.ValidateContext{}); len(errs) != 0 {
		t.Fatalf("errors %v, want none", errs)
	}
}

func TestValidateRuleSpec_RefusesAContextMentionInThePromptAndTheName(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*rule.Spec)
		field string
		want  string
	}{
		{
			"in the prompt", func(s *rule.Spec) { s.Prompt = "Read #[[file:notes.md]] and act." },
			"prompt", "Context mentions are not supported in rule prompts; name the file in words",
		},
		{"in the name", func(s *rule.Spec) { s.Name = "Weekly #[[a:b]]" }, "name", "Context mentions are not supported here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ruleSpecForValidation()
			tc.edit(&s)
			errs := ValidateRuleSpec(s, rule.ValidateContext{})
			if len(errs) != 1 || errs[0].Field != tc.field || errs[0].Error != tc.want {
				t.Fatalf("errors %v, want one at %q reading %q", errs, tc.field, tc.want)
			}
		})
	}
}

func TestValidateRuleSpec_AnswersTheMentionsBesideRuleValidatesErrors(t *testing.T) {
	s := ruleSpecForValidation()
	s.Name = "#[[a:b]]"
	s.Prompt = "See #[[url:https://example.com]]"
	s.Triggers[0].Every.Minutes = 1
	var got []string
	for _, e := range ValidateRuleSpec(s, rule.ValidateContext{}) {
		got = append(got, e.Field)
	}
	slices.Sort(got)
	if want := []string{"name", "prompt", "triggers[0].every.minutes"}; !slices.Equal(got, want) {
		t.Errorf("errors at %q, want %q", got, want)
	}
}

func TestValidateRuleSpec_TextThatIsNotAMentionPasses(t *testing.T) {
	for _, text := range []string{"#[[File:x]]", "#[file:x]", "[[file:x]]", "#[[file:x\n]]", "use #42 and [[x]]"} {
		s := ruleSpecForValidation()
		s.Prompt = text
		if errs := ValidateRuleSpec(s, rule.ValidateContext{}); len(errs) != 0 {
			t.Errorf("%q: errors %v, want none", text, errs)
		}
	}
	s := ruleSpecForValidation()
	s.Name = "Weekly #[[File:x]] and #42"
	if errs := ValidateRuleSpec(s, rule.ValidateContext{}); len(errs) != 0 {
		t.Errorf("name: errors %v, want none", errs)
	}
}
