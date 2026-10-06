package marotte

import (
	"slices"
	"strings"
)

// The hook trigger vocabulary (which trigger names KAS loads and what each matcher tests), shared
// by internal/command's create_hook validation and internal/agent's diagnostic, so the two tables
// cannot disagree.

// HookMatcherSubject is what a trigger's matcher is tested against, and it is
// the fact that decides whether a matcher means anything for that trigger.
//
// The three values mirror KAS's own matcherSubjectForTrigger, spelling included,
// read off the stock 2.19.2 bundle. Keeping the spellings identical is
// deliberate: the classification is upstream's, so a divergence here would be
// marotte inventing a rule and attributing it to KAS.
type HookMatcherSubject string

const (
	// HookMatcherSubjectToolName means the matcher is tested against the tool
	// NAME. A hook with no matcher runs on every tool call, which is legitimate
	// and worth saying out loud rather than refusing.
	HookMatcherSubjectToolName HookMatcherSubject = "toolName"
	// HookMatcherSubjectFilePath means the matcher is tested against the file
	// PATH. A matcher here is fully effective, so neither presence nor absence
	// is a defect.
	HookMatcherSubjectFilePath HookMatcherSubject = "filePath"
	// HookMatcherSubjectNone means the trigger carries nothing to match on, so a
	// matcher is IGNORED. Upstream warns to its own log and tells no client, which
	// is why marotte refuses the pairing at creation instead.
	HookMatcherSubjectNone HookMatcherSubject = "none"
)

// HookTrigger is one canonical trigger name with its matcher subject.
type HookTrigger struct {
	// Name is the PascalCase trigger name KAS's hook loader expects.
	Name string
	// Subject is what this trigger's matcher is tested against.
	Subject HookMatcherSubject
	// CommandOnly means KAS runs only command actions for this trigger, so an
	// agent action loads and never fires.
	CommandOnly bool
}

// The twelve canonical trigger names, as KAS's hook loader spells them.
//
// Constants rather than repeated literals because the alias table below points
// many spellings at ONE canonical name, and that is the file's invariant: a typo
// in an alias would otherwise mint a silent thirteenth trigger that KAS then drops.
// Unexported — nothing outside this package names a trigger, it normalizes one.
const (
	triggerSessionStart     = "SessionStart"
	triggerSessionEnd       = "SessionEnd"
	triggerStop             = "Stop"
	triggerPreToolUse       = "PreToolUse"
	triggerPostToolUse      = "PostToolUse"
	triggerPreTaskExec      = "PreTaskExec"
	triggerPostTaskExec     = "PostTaskExec"
	triggerUserPromptSubmit = "UserPromptSubmit"
	triggerPostFileCreate   = "PostFileCreate"
	triggerPostFileSave     = "PostFileSave"
	triggerPostFileDelete   = "PostFileDelete"
	triggerManual           = "Manual"
)

// hookTriggers maps event-type payload values (marotte's own vocabulary plus v2 /
// Kiro-IDE camelCase aliases), lowercased for lookup, to the canonical trigger; an
// alias shares its canonical name's meta, so it cannot disagree about a subject.
// The set is CLOSED and each of the 12 canonical triggers has exactly one subject,
// which makes the pairing check total (TestEveryCanonicalTriggerHasASubject).
var hookTriggers = map[string]HookTrigger{
	// Canonical PascalCase names (self-map via their lowercase key).
	"sessionstart":     {Name: triggerSessionStart, Subject: HookMatcherSubjectNone},
	"sessionend":       {Name: triggerSessionEnd, Subject: HookMatcherSubjectNone, CommandOnly: true},
	"stop":             {Name: triggerStop, Subject: HookMatcherSubjectNone},
	"pretooluse":       {Name: triggerPreToolUse, Subject: HookMatcherSubjectToolName},
	"posttooluse":      {Name: triggerPostToolUse, Subject: HookMatcherSubjectToolName},
	"pretaskexec":      {Name: triggerPreTaskExec, Subject: HookMatcherSubjectNone},
	"posttaskexec":     {Name: triggerPostTaskExec, Subject: HookMatcherSubjectNone},
	"userpromptsubmit": {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"postfilecreate":   {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"postfilesave":     {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"postfiledelete":   {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"manual":           {Name: triggerManual, Subject: HookMatcherSubjectNone},
	// v2 / Kiro-IDE camelCase aliases.
	"agentstop":         {Name: triggerStop, Subject: HookMatcherSubjectNone},
	"promptsubmit":      {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"userprompt":        {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"pretaskexecution":  {Name: triggerPreTaskExec, Subject: HookMatcherSubjectNone},
	"posttaskexecution": {Name: triggerPostTaskExec, Subject: HookMatcherSubjectNone},
	"filecreate":        {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"filecreated":       {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"filesave":          {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"filesaved":         {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"fileedit":          {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"fileedited":        {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"filedelete":        {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"filedeleted":       {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"usertriggered":     {Name: triggerManual, Subject: HookMatcherSubjectNone},
	// Two more spellings KAS itself accepts. Their absence meant a payload
	// using either produced a hook file KAS then discarded.
	"agentspawn":    {Name: triggerSessionStart, Subject: HookMatcherSubjectNone},
	"afterfileedit": {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
}

// NormalizeHookTrigger maps a client event-type value, or a canonical name KAS reported, to its
// canonical trigger and reports whether KAS will load it. An unknown trigger is refused: KAS's
// parseHookDocument silently drops such a hook, so it would never load or fire.
func NormalizeHookTrigger(eventType string) (HookTrigger, bool) {
	t, ok := hookTriggers[strings.ToLower(strings.TrimSpace(eventType))]
	return t, ok
}

// KnownHookTriggers lists the canonical trigger names for an error message, so a
// rejection tells the caller what IS accepted rather than only what is not.
func KnownHookTriggers() string {
	seen := make(map[string]struct{}, len(hookTriggers))
	names := make([]string, 0, len(hookTriggers))
	for _, v := range hookTriggers {
		if _, dup := seen[v.Name]; dup {
			continue
		}
		seen[v.Name] = struct{}{}
		names = append(names, v.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// HookMatcherDefect is what is wrong with a trigger-and-matcher pairing, and the
// two members are deliberately on DIFFERENT surfaces because they are different
// kinds of mistake.
type HookMatcherDefect string

const (
	// HookMatcherOK means the pairing is fine.
	HookMatcherOK HookMatcherDefect = ""
	// HookMatcherIneffective means a matcher was supplied for a trigger that has
	// nothing to match on, so it is IGNORED. Always a mistake, cheap to fix at
	// the form, and therefore refused at creation.
	HookMatcherIneffective HookMatcherDefect = "ineffective"
	// HookMatcherMissingToolName means a tool-name trigger carries no matcher, so
	// the hook runs on EVERY tool call. A legitimate choice ("run on every tool
	// call") that must not be blocked, so it is a badge on the read surface.
	HookMatcherMissingToolName HookMatcherDefect = "missing_tool_matcher"
)

// ClassifyHookMatcher reports what is wrong with a trigger-and-matcher pairing, one function for
// the write and read sides. Both defects mirror diagnostics KAS keeps to itself
// (reportMatcherDiagnostics). An unknown trigger returns HookMatcherOK: NormalizeHookTrigger
// already refused it.
func ClassifyHookMatcher(trigger, matcher string) HookMatcherDefect {
	t, ok := NormalizeHookTrigger(trigger)
	if !ok {
		return HookMatcherOK
	}
	hasMatcher := strings.TrimSpace(matcher) != ""
	switch t.Subject {
	case HookMatcherSubjectToolName:
		if !hasMatcher {
			return HookMatcherMissingToolName
		}
	case HookMatcherSubjectNone:
		if hasMatcher {
			return HookMatcherIneffective
		}
	case HookMatcherSubjectFilePath:
		// A path matcher is fully effective and its absence is a legitimate
		// "every file". Nothing to report in either direction, and saying so is
		// the point: this arm is what keeps the switch exhaustive over the
		// subject enum rather than leaving filePath in a default nobody reads.
	}
	return HookMatcherOK
}
