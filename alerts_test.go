package main

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/testsupport"
	"go.yaml.in/yaml/v4"
)

// shippedAlerts holds every alert name alerts/promql.yaml has carried: "" for one still in the file, else the alert
// now covering its condition or "removed: <why>". Deleting or renaming a rule fails the test until its entry here
// records the decision.
var shippedAlerts = map[string]string{
	"MarotteDown":                       "",
	"MarotteEngineDown":                 "",
	"MarotteEngineCrashed":              "",
	"MarotteEngineCrashLoop":            "",
	"MarotteEngineStoppedExternally":    "",
	"MarotteEngineMemoryHigh":           "",
	"MarottePromptFailures":             "",
	"MarotteSignInStagedCopyLeft":       "",
	"MarotteScheduleFailing":            "",
	"MarotteScheduleStalled":            "",
	"MarotteScheduleSlotNotStarted":     "",
	"MarotteUnattendedRunNeedsApproval": "",
	"MarotteUnattendedRunStopped":       "",
	"MarotteUnattendedRunCancelFailed":  "",
	"MarotteUnattendedRunSelfApproved":  "",
	"MarotteTriggerRuleBlocked":         "",
	"MarotteLaunchRulesNotWritten":      "",
	"MarotteTriggerQueueFull":           "",
	"MarotteTriggerPollFailing":         "",
	"MarotteTriggerCatchUpGap":          "",
}

type ruleFile struct {
	Groups []struct {
		Rules []struct {
			Alert string `yaml:"alert"`
			Expr  string `yaml:"expr"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func TestAlerts_everyRuleReadsASeriesThePageServes(t *testing.T) {
	page := servedPage(t)
	for alert, expr := range readRules(t) {
		t.Run(alert, func(t *testing.T) {
			names := metricNames(expr)
			if len(names) == 0 {
				t.Fatalf("no metric name found in %q", expr)
			}
			for _, name := range names {
				// up is the scraper's own series, never on the page.
				if _, served := page[name]; name != "up" && !served {
					t.Errorf("reads %s, which /metrics does not serve", name)
				}
			}
		})
	}
}

// A rule's label matcher names values the app writes, so a renamed cause, class or reason fails here instead of
// silently matching nothing, or, under a negative matcher, matching everything.
func TestAlerts_everyMatchedLabelValueIsOneThePageServes(t *testing.T) {
	page := servedPage(t)
	for alert, expr := range readRules(t) {
		t.Run(alert, func(t *testing.T) {
			for _, sel := range promSelector.FindAllStringSubmatch(expr, -1) {
				family := sel[1]
				for _, m := range promMatcher.FindAllStringSubmatch(sel[2], -1) {
					label, op, value := m[1], m[2], m[3]
					if family == "up" || label == "job" {
						continue
					}
					values := []string{value}
					if op == "=~" || op == "!~" {
						values = strings.Split(value, "|")
					}
					for _, v := range values {
						if !page[family][label][v] {
							t.Errorf("%s{%s%s...} names %s=%q, which /metrics never writes", family, label, op, label, v)
						}
					}
				}
			}
		})
	}
}

// The crash rules leave out exactly the stops marotte asked for, so a new requested-stop cause cannot page as a
// crash, and an outside stop, which has its own rule.
func TestAlerts_theCrashRulesLeaveOutExactlyTheRequestedAndOutsideStops(t *testing.T) {
	rules := readRules(t)
	var want []string
	for _, c := range testsupport.PresetLabelValues("marotte_engine_exits_total", "cause") {
		if obs.RequestedStop(c) {
			want = append(want, c)
		}
	}
	slices.Sort(want)
	for _, alert := range []string{"MarotteEngineCrashed", "MarotteEngineCrashLoop"} {
		t.Run(alert, func(t *testing.T) {
			var excluded []string
			outside := false
			for _, m := range promMatcher.FindAllStringSubmatch(rules[alert], -1) {
				switch label, op, value := m[1], m[2], m[3]; {
				case label == "cause" && op == "!~":
					excluded = append(excluded, strings.Split(value, "|")...)
				case label == "cause" && op == "!=" && value == "external_stop":
					outside = true
				case label == "cause":
					t.Errorf("unexpected matcher cause%s%q", op, value)
				}
			}
			if got := slices.Sorted(slices.Values(excluded)); !slices.Equal(got, want) {
				t.Errorf("cause!~ leaves out %v, want exactly the causes obs.RequestedStop accepts %v", got, want)
			}
			if !outside {
				t.Error(`no cause!="external_stop" matcher`)
			}
		})
	}
}

func TestAlerts_aShippedAlertLeavesOnlyByARecordedDecision(t *testing.T) {
	rules := readRules(t)
	for alert := range rules {
		if _, ok := shippedAlerts[alert]; !ok {
			t.Errorf("%s is not in shippedAlerts", alert)
		}
	}
	for alert, now := range shippedAlerts {
		_, inFile := rules[alert]
		switch {
		case now == "" && !inFile:
			t.Errorf("%s left alerts/promql.yaml; record its successor or its removal in shippedAlerts", alert)
		case now != "" && inFile:
			t.Errorf("%s is in the file but recorded as %q", alert, now)
		case now != "" && !strings.HasPrefix(now, "removed: "):
			if _, ok := rules[now]; !ok {
				t.Errorf("%s's successor %s is not in the file", alert, now)
			}
		}
	}
}

// Every rule has a promtool fixture and a row on the monitoring page, so a new rule cannot ship untested or
// undocumented.
func TestAlerts_everyRuleHasAFixtureAndAPageRow(t *testing.T) {
	fixtures := readFile(t, "tests/promql_test.yaml")
	page := readFile(t, "docs/monitoring.md")
	for alert := range readRules(t) {
		if !strings.Contains(fixtures, "alertname: "+alert+"\n") {
			t.Errorf("%s has no alert_rule_test in tests/promql_test.yaml", alert)
		}
		if !strings.Contains(page, "| `"+alert+"` |") {
			t.Errorf("%s has no row in docs/monitoring.md", alert)
		}
	}
}

// servedPage records one sample in every family that starts with none, since such a family prints no series, and
// returns the page's label values by family and label; a family with no labels maps to an empty, non-nil set.
func servedPage(t *testing.T) map[string]map[string]map[string]bool {
	t.Helper()
	obs.Schedule("alerts-test", 0, time.Unix(1, 0))
	obs.EngineMemory(1, 1)
	obs.SignInBackend("alerts-test")
	obs.TriggerRule("alerts-test", &obs.TriggerRuleState{})

	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rec.Code)
	}
	out := map[string]map[string]map[string]bool{}
	for line := range strings.Lines(rec.Body.String()) {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series := line[:max(strings.LastIndexByte(line, ' '), 0)]
		name, set, labeled := strings.Cut(series, "{")
		if labeled {
			var ok bool
			if set, ok = strings.CutSuffix(set, "}"); !ok {
				t.Fatalf("unparsable series in %q", line)
			}
		}
		labels := out[name]
		if labels == nil {
			labels = map[string]map[string]bool{}
			out[name] = labels
		}
		for pair := range strings.SplitSeq(set, ",") {
			k, v, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			if labels[k] == nil {
				labels[k] = map[string]bool{}
			}
			labels[k][strings.Trim(v, `"`)] = true
		}
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// readRules returns alerts/promql.yaml's expressions by alert name.
func readRules(t *testing.T) map[string]string {
	t.Helper()
	var f ruleFile
	if err := yaml.Unmarshal([]byte(readFile(t, "alerts/promql.yaml")), &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			if _, dup := out[r.Alert]; dup {
				t.Errorf("alert %s twice", r.Alert)
			}
			out[r.Alert] = r.Expr
		}
	}
	if len(out) == 0 {
		t.Fatal("no rules in alerts/promql.yaml")
	}
	return out
}

var (
	promSelector = regexp.MustCompile(`([A-Za-z_:][A-Za-z0-9_:]*)\{([^}]*)\}`)
	promMatcher  = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(=~|!~|!=|=)\s*"([^"]*)"`)
	promString   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	promMatchers = regexp.MustCompile(`\{[^}]*\}|\[[^\]]*\]`)
	promLabels   = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)`)
	promToken    = regexp.MustCompile(`[A-Za-z_:][A-Za-z0-9_:]*(\s*\()?|[0-9][0-9A-Za-z_.]*`)
	promKeywords = map[string]bool{
		"and": true, "or": true, "unless": true, "offset": true, "bool": true,
		"by": true, "without": true, "on": true, "ignoring": true, "group_left": true, "group_right": true,
		"inf": true, "nan": true,
	}
)

// metricNames returns the metric names expr reads, sorted: every name that is not a function call, a keyword, a
// label or a number.
func metricNames(expr string) []string {
	expr = promString.ReplaceAllString(expr, `""`)
	expr = promMatchers.ReplaceAllString(expr, " ")
	expr = promLabels.ReplaceAllString(expr, " ")
	names := map[string]bool{}
	for _, m := range promToken.FindAllStringSubmatch(expr, -1) {
		tok := m[0]
		if m[1] != "" || tok[0] >= '0' && tok[0] <= '9' || promKeywords[strings.ToLower(tok)] {
			continue
		}
		names[tok] = true
	}
	return slices.Sorted(maps.Keys(names))
}
