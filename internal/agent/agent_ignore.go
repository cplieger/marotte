package agent

// KAS's ignore evaluators deny before any allow rule is consulted, so marotte runs no
// matcher: it only sends the list, over a connection-scope notification.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// bridge.Notify carries no deadline.
const ignorePushTimeout = 10 * time.Second

// readAgentIgnoreFiles resolves the list sent to KAS from d and returns the key's decode error, with the floor alone
// beside it. An absent key means the floor.
func readAgentIgnoreFiles(d settings.Document) ([]string, error) {
	list, _, err := settings.Lookup[[]string](d, settings.KeyAgentIgnoreFiles)
	if err != nil {
		return []string{settings.AgentIgnoreFloor}, err
	}
	return settings.AgentIgnoreList(list), nil
}

// spawnIgnoreFiles is EngineOpts.IgnoreFiles: the configDoc owner's list, so over an unreadable document a spawn
// sends the last readable list. Sending nothing would leave `.kiroignore` unenforced for the session, so the floor is
// what a never-readable document or a list that does not decode gets.
func (rt *Runtime) spawnIgnoreFiles(ctx context.Context) []string {
	doc, src := rt.configDocument(ctx)
	switch {
	case src == fromDefaults:
		slog.Warn("agent ignore files: config.json cannot be read and none was read since marotte started; this spawn enforces only the floor",
			"key", settings.KeyAgentIgnoreFiles)
	case doc.engine.ignoreInvalid:
		slog.Warn("agent ignore files: the stored list does not decode; this spawn enforces only the floor",
			"key", settings.KeyAgentIgnoreFiles)
	}
	return doc.engine.ignoreFiles
}

// ignoreReport is which list the unreadable-ignore-list report said a restarted engine enforces.
type ignoreReport uint8

const (
	ignoreReportNone  ignoreReport = iota // nothing reported: the list reads, or no engine is up
	ignoreReportFloor                     // only the floor: no readable list since boot, or the stored list does not decode
	ignoreReportLast                      // the last list marotte read
)

// reportIgnoreFilesUnreadable tells the operator and the client what is enforced while the list cannot be pushed: the
// running engine keeps its previous list, because an empty push would CLEAR enforcement in KAS, and an engine started
// meanwhile gets what spawnIgnoreFiles sends. It broadcasts only when that answer differs from reported, and returns
// the answer; with no engine up there is nothing to report.
func (rt *Runtime) reportIgnoreFilesUnreadable(ctx context.Context, reported ignoreReport) ignoreReport {
	if h, _ := rt.engine.current(); h == nil {
		return ignoreReportNone
	}
	doc, src := rt.configDocument(ctx)
	kind := ignoreReportLast
	if src == fromDefaults || doc.engine.ignoreInvalid {
		kind = ignoreReportFloor
	}
	if kind == reported {
		return kind
	}
	enforced := "the last list marotte read"
	if kind == ignoreReportFloor {
		slog.Error("agent ignore files not pushed; the agent engine keeps its previous list, and an engine started before the file is fixed enforces only the floor",
			"key", settings.KeyAgentIgnoreFiles, "floor", settings.AgentIgnoreFloor)
		enforced = "only " + settings.AgentIgnoreFloor
	} else {
		slog.Error("agent ignore files not pushed; the agent engine keeps its previous list, and an engine started before the file is fixed starts on the last list marotte read",
			"key", settings.KeyAgentIgnoreFiles)
	}
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPolicyError, "", marotte.PolicyErrorPayload{
		Errors: []marotte.PolicyErrorItem{{
			Source: settings.Filename,
			Message: "The agent ignore file list could not be read. The agent engine keeps enforcing its previous list; " +
				"if it restarts before the file is fixed it enforces " + enforced + ".",
		}},
	}))
	return kind
}
