package trigger

import (
	"regexp"
	"strconv"

	"github.com/cplieger/forgeapi/v2"
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
		in["issue_number"] = strconv.Itoa(number)
	} else {
		in["pr_number"] = strconv.Itoa(number)
	}
	if pr.labeled() {
		in["label"] = matched
	} else {
		in["author"] = matched
	}
	return in
}

func (pr *pollRule) runInputs(in map[string]string, r *forgeapi.Run, result string) map[string]string {
	in["run_id"] = strconv.FormatInt(r.Ref.ID, 10)
	in["result"] = result
	in["workflow"] = pr.ev.Filter.Workflow
	if headSHA.MatchString(r.HeadSHA) {
		in["head_sha"] = r.HeadSHA
	}
	return in
}
