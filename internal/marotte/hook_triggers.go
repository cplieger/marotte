package marotte

import "strings"

// The hook trigger vocabulary (which trigger names KAS loads and what each matcher tests), read by
// internal/agent's Hooks-tab diagnostic.

// HookMatcherSubject is what a trigger's matcher is tested against, and it is
// the fact that decides whether a matcher means anything for that trigger.
//
// The three values mirror KAS's own matcherSubjectForTrigger, spelling included,
// re-read off the 2.28.0 bundle. Keeping the spellings identical is
// deliberate: the classification is upstream's, so a divergence here would be
// marotte inventing a rule and attributing it to KAS.
type HookMatcherSubject string

const (
	// HookMatcherSubjectToolName means the matcher is tested against the tool
	// (its name, shell alias or tags; KAS owns that grammar). A hook with no
	// matcher runs on every tool call, which is legitimate and worth saying out
	// loud rather than refusing.
	HookMatcherSubjectToolName HookMatcherSubject = "toolName"
	// hookMatcherSubjectFilePath means the matcher is tested against the file
	// PATH. A matcher here is fully effective, so neither presence nor absence
	// is a defect.
	hookMatcherSubjectFilePath HookMatcherSubject = "filePath"
	// HookMatcherSubjectNone means the trigger carries nothing to match on, so a
	// matcher is IGNORED. Upstream warns to its own log and tells no client, which
	// is why marotte badges the pairing on the Hooks tab.
	HookMatcherSubjectNone HookMatcherSubject = "none"
)

// The twelve canonical trigger names, as KAS's hook loader spells them.
//
// Constants rather than repeated literals because the spelling table below points
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

// triggerSubjects gives each canonical trigger its one matcher subject, so an alias, which
// names a canonical trigger, cannot disagree about one. The set is CLOSED, which makes the
// pairing check total (TestEveryCanonicalTriggerHasASubject).
var triggerSubjects = map[string]HookMatcherSubject{
	triggerSessionStart:     HookMatcherSubjectNone,
	triggerSessionEnd:       HookMatcherSubjectNone,
	triggerStop:             HookMatcherSubjectNone,
	triggerPreToolUse:       HookMatcherSubjectToolName,
	triggerPostToolUse:      HookMatcherSubjectToolName,
	triggerPreTaskExec:      HookMatcherSubjectNone,
	triggerPostTaskExec:     HookMatcherSubjectNone,
	triggerUserPromptSubmit: HookMatcherSubjectNone,
	triggerPostFileCreate:   hookMatcherSubjectFilePath,
	triggerPostFileSave:     hookMatcherSubjectFilePath,
	triggerPostFileDelete:   hookMatcherSubjectFilePath,
	triggerManual:           HookMatcherSubjectNone,
}

// hookTriggerNames maps trigger spellings (canonical names plus v2 / Kiro-IDE camelCase
// aliases), lowercased for lookup, to the canonical trigger.
var hookTriggerNames = map[string]string{
	// Canonical PascalCase names (self-map via their lowercase key).
	"sessionstart":     triggerSessionStart,
	"sessionend":       triggerSessionEnd,
	"stop":             triggerStop,
	"pretooluse":       triggerPreToolUse,
	"posttooluse":      triggerPostToolUse,
	"pretaskexec":      triggerPreTaskExec,
	"posttaskexec":     triggerPostTaskExec,
	"userpromptsubmit": triggerUserPromptSubmit,
	"postfilecreate":   triggerPostFileCreate,
	"postfilesave":     triggerPostFileSave,
	"postfiledelete":   triggerPostFileDelete,
	"manual":           triggerManual,
	// v2 / Kiro-IDE camelCase aliases.
	"agentstop":         triggerStop,
	"promptsubmit":      triggerUserPromptSubmit,
	"userprompt":        triggerUserPromptSubmit,
	"pretaskexecution":  triggerPreTaskExec,
	"posttaskexecution": triggerPostTaskExec,
	"filecreate":        triggerPostFileCreate,
	"filecreated":       triggerPostFileCreate,
	"filesave":          triggerPostFileSave,
	"filesaved":         triggerPostFileSave,
	"fileedit":          triggerPostFileSave,
	"fileedited":        triggerPostFileSave,
	"filedelete":        triggerPostFileDelete,
	"filedeleted":       triggerPostFileDelete,
	"usertriggered":     triggerManual,
	// Two more spellings KAS itself accepts.
	"agentspawn":    triggerSessionStart,
	"afterfileedit": triggerPostFileSave,
}

// canonicalHookTrigger maps a trigger spelling to its canonical name and reports whether KAS
// loads it.
func canonicalHookTrigger(eventType string) (string, bool) {
	name, ok := hookTriggerNames[strings.ToLower(strings.TrimSpace(eventType))]
	return name, ok
}

// HookTriggerSubject is what a trigger spelling's matcher is tested against, and whether KAS
// loads the trigger at all; KAS's parseHookDocument drops a hook whose trigger it does not
// recognise.
func HookTriggerSubject(eventType string) (HookMatcherSubject, bool) {
	name, ok := canonicalHookTrigger(eventType)
	if !ok {
		return "", false
	}
	return triggerSubjects[name], true
}

// HookMatcherDefect is what is wrong with a trigger-and-matcher pairing, shown as
// a Hooks-tab badge.
type HookMatcherDefect string

const (
	// hookMatcherOK means the pairing is fine.
	hookMatcherOK HookMatcherDefect = ""
	// hookMatcherIneffective means a matcher was supplied for a trigger that has
	// nothing to match on, so it is IGNORED. Always a mistake.
	hookMatcherIneffective HookMatcherDefect = "ineffective"
	// hookMatcherMissingToolName means a tool-name trigger carries no matcher, so
	// the hook runs on EVERY tool call: possibly deliberate, but worth a badge.
	hookMatcherMissingToolName HookMatcherDefect = "missing_tool_matcher"
)

// ClassifyHookMatcher reports what is wrong with a trigger-and-matcher pairing. Both defects
// mirror diagnostics KAS keeps to itself (reportMatcherDiagnostics). An unknown trigger returns
// hookMatcherOK: KAS drops that hook, so a matcher complaint would name the wrong problem.
func ClassifyHookMatcher(trigger, matcher string) HookMatcherDefect {
	subject, ok := HookTriggerSubject(trigger)
	if !ok {
		return hookMatcherOK
	}
	hasMatcher := strings.TrimSpace(matcher) != ""
	switch subject {
	case HookMatcherSubjectToolName:
		if !hasMatcher {
			return hookMatcherMissingToolName
		}
	case HookMatcherSubjectNone:
		if hasMatcher {
			return hookMatcherIneffective
		}
	case hookMatcherSubjectFilePath:
		// A path matcher is fully effective and its absence is a legitimate
		// "every file". Nothing to report in either direction, and saying so is
		// the point: this arm is what keeps the switch exhaustive over the
		// subject enum rather than leaving filePath in a default nobody reads.
	}
	return hookMatcherOK
}
