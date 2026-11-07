package trigger

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/launchrule"
)

// The fixed words of event keys and event ids.
const (
	partIssue   = "issue"
	partPR      = "pr"
	partRun     = "run"
	partLabeled = "labeled"
	partOpened  = "opened"
)

// foldRepo is the repository part of an event key and an event id: the selector with ASCII A-Z folded, the
// folding RepoRef.Encode applies before it hex-encodes, so two spellings of one repository name it alike.
func foldRepo(selector string) string {
	b := []byte(selector)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// itemPart is the key word of a labeled or opened kind's item.
func itemPart(k launchrule.Kind) string {
	if k == launchrule.KindIssueLabeled || k == launchrule.KindIssueOpened {
		return partIssue
	}
	return partPR
}

// connectionParts splits a forges.MakeID connection id into its kind and host.
// The connection kinds whose reads the trigger poller treats apart; forges.Kind owns the full set.
const (
	kindGitHub = "github"
	kindGitLab = "gitlab"
)

func connectionParts(id string) (kind, host string) {
	parts, err := keyenc.Split(id)
	if err != nil || len(parts) != 2 {
		return "", id
	}
	return parts[0], parts[1]
}

func labeledKey(rule, repo, part string, number int, gain string) string {
	return keyenc.Join(rule, repo, part, strconv.Itoa(number), partLabeled, gain)
}

func openedKey(rule, repo, part string, number int) string {
	return keyenc.Join(rule, repo, part, strconv.Itoa(number), partOpened)
}

func runKey(rule, repo string, id int64) string {
	return keyenc.Join(rule, repo, partRun, strconv.FormatInt(id, 10))
}

// itemCoalesceKey is a labeled or opened event's key without its last two parts, so newest keeps the latest
// decision per item.
func itemCoalesceKey(rule, repo, part string, number int) string {
	return keyenc.Join(rule, repo, part, strconv.Itoa(number))
}

// runCoalesceKey groups the runs of one workflow on one branch. It holds a branch name, forge text, so it is never
// logged, shown or passed as an input.
func runCoalesceKey(repoID, workflowPath, branch string) string {
	return keyenc.Join(repoID, workflowPath, branch)
}

// mintGain is the instant a label gain seen by a poll started at start is minted at. A rule never mints at or
// below last, so a clock stepped back still mints above every id the rule has passed.
func mintGain(start, last time.Time) time.Time {
	m := start.UTC().Truncate(time.Millisecond)
	if !m.After(last) {
		m = last.UTC().Add(time.Millisecond)
	}
	return m
}

// formatGain writes a minted gain as 20261104T101530123Z.
func formatGain(t time.Time) string {
	t = t.UTC()
	return t.Format("20060102T150405") + fmt.Sprintf("%03dZ", t.Nanosecond()/int(time.Millisecond))
}

// Event ids are compared whole by a recipe, never split: a GitLab selector may hold "/".

func labeledEventID(host, repo, part string, number int, rule, gain string) string {
	return strings.Join([]string{host, repo, part, strconv.Itoa(number), partLabeled, rule, gain}, "/")
}

func openedEventID(host, repo, part string, number int) string {
	return strings.Join([]string{host, repo, part, strconv.Itoa(number), partOpened}, "/")
}

func runEventID(host, repo string, id int64, result string) string {
	return strings.Join([]string{host, repo, partRun, strconv.FormatInt(id, 10), result}, "/")
}
