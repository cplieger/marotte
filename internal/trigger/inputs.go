package trigger

import (
	"regexp"
	"strconv"

	"github.com/cplieger/forgeapi/v2"
)

// The event inputs that name an event's item or run.
const (
	inputIssueNumber = "issue_number"
	inputPRNumber    = "pr_number"
	inputRunID       = "run_id"
)

// headSHA is a commit id of a SHA-1 or a SHA-256 repository; anything else is not passed.
var headSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// eventInputs are the inputs every event sets. They carry ids and the rule's own fields only: forge text (a
// title, body, branch or author) never becomes an input.
func (pr *pollRule) eventInputs(key, eventID string) map[string]string {
	return map[string]string{
		"repo":         pr.feed.Selector,
		"forge":        pr.feed.Connection,
		"trigger_kind": string(pr.ev.Kind),
		"trigger_rule": pr.rule.ID,
		"trigger_key":  key,
		"event_id":     eventID,
	}
}

// itemInputs adds a labeled or opened event's item number and the rule's own matched string.
func (pr *pollRule) itemInputs(in map[string]string, part string, number int, matched string) map[string]string {
	if part == partIssue {
		in[inputIssueNumber] = strconv.Itoa(number)
	} else {
		in[inputPRNumber] = strconv.Itoa(number)
	}
	if pr.labeled() {
		in["label"] = matched
	} else {
		in["author"] = matched
	}
	return in
}

func (pr *pollRule) runInputs(in map[string]string, r *forgeapi.Run, result string) map[string]string {
	in[inputRunID] = strconv.FormatInt(r.Ref.ID, 10)
	in["result"] = result
	in["workflow"] = pr.ev.Filter.Workflow
	if headSHA.MatchString(r.HeadSHA) {
		in["head_sha"] = r.HeadSHA
	}
	return in
}

// EventLabel names a triggered launch's event in its run label: "issue 42", "pr 17" or "run 1234567".
func EventLabel(inputs map[string]string) string {
	switch {
	case inputs[inputRunID] != "":
		return partRun + " " + inputs[inputRunID]
	case inputs[inputPRNumber] != "":
		return partPR + " " + inputs[inputPRNumber]
	case inputs[inputIssueNumber] != "":
		return partIssue + " " + inputs[inputIssueNumber]
	}
	return "triggered"
}
