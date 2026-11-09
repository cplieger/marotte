// Package kirosession reaps the kiro-cli / KAS on-disk session state that backs session/load
// resume. It lives while a holder names it (a chat's chain, a run's launching session while KAS
// keeps the run, a resident session, a live run's step sessions). marotte is the sole retention
// authority: KAS's sessionEviction defaults to disabled and the boot conformance check keeps it so.
//
// Layout (KAS 2.12):
//
//	$KIRO_HOME/sessions/<workspace-hash>/sess_<id>/   — the KAS session dir
//	$KIRO_HOME/sessions/<workspace-hash>/workflows/<workflowId>/ — a run's state
//	<workspace>/.kiro/workflows/<workflowId>/          — a run's state KAS still finds by id
//	$KIRO_HOME/sessions/cli/sess_<id>.history         — its history sidecar
//	$KIRO_HOME/sessions/cli/<uuid>.{json,jsonl,lock}  — dead v2-engine files
//
// The workspace-hash dir is untracked, so a session is found by globbing sessions/*/sess_<id>,
// which crosses every bucket: each match must still name this workspace (belongsToWorkspace).
package kirosession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/ids"
)

// sessionPrefix is the v3 KAS session-id prefix. Anything without it is empty,
// garbage, or a dead v2-engine file the sweep reaps purely on age.
const sessionPrefix = "sess_"

// defaultGuard spares session state younger than this from the orphan sweep: it
// covers the create race, since KAS writes the session dir a moment before marotte
// persists the chat file that references it. Direct Reap is not subject to it —
// there the caller knows the chat is gone.
const defaultGuard = 10 * time.Minute

// sessionRecordName is the per-session file KAS writes its own metadata into. Its
// workspacePaths list is the subject record every removal here is checked against.
const sessionRecordName = "session.json"

// cliDirName is the directory under sessions/ that holds sidecars, not a workspace bucket.
const cliDirName = "cli"

// workflowsDirName is the per-bucket directory KAS keeps a workflow RUN's state in:
// sessions/<workspace-hash>/workflows/<workflowId>/.
const workflowsDirName = "workflows"

// runStateFileName is the file in a run's state directory that KAS's reader parses whole.
const runStateFileName = "workflow-state.json"

// stateParentKey is the top-level member of a run's state that names the session the run was launched from, the one
// a verb on that run loads. TestRunStateParentKeyMatchesBundle pins it to KAS's reader.
const stateParentKey = "parentSessionId"

// Reaper removes on-disk KAS session state under sessionsDir, for the one
// workspace it is entitled to reap.
type Reaper struct {
	sessionsDir string
	// workspaceRoot is the workspace this reaper may delete sessions for. Every
	// removal reads the candidate's own workspacePaths and skips a session whose
	// list does not name it; an empty root names nothing, so it reaps nothing.
	workspaceRoot string
	guard         time.Duration
}

// New returns a Reaper rooted at sessionsDir (typically
// filepath.Join(workspace.KiroHome(), "sessions")) and confined to workspaceRoot
// (the agent engine's cwd).
func New(sessionsDir, workspaceRoot string) *Reaper {
	root := workspaceRoot
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Reaper{sessionsDir: sessionsDir, workspaceRoot: root, guard: defaultGuard}
}

// A mismatch is a skip and so is DOUBT (unreadable record, empty list): a wrong skip costs disk, a
// wrong removal another workspace's history.
func (r *Reaper) belongsToWorkspace(sessionDir string) bool {
	paths, _, ok := readSessionRecord(sessionDir)
	if !ok {
		return false
	}
	return namesRoot(paths, r.workspaceRoot)
}

// Split out so the sweep's one-read path applies the same rule rather than a second copy.
func namesRoot(paths []string, root string) bool {
	for _, p := range paths {
		if p != "" && filepath.Clean(p) == root {
			return true
		}
	}
	return false
}

// readSessionRecord decodes the two facts this package reads from a session's session.json: the
// workspace roots it claims and the workflow RUN it was created for (empty for non-step sessions);
// ok is false when absent or undecodable. One reader, so two reads cannot disagree.
func readSessionRecord(sessionDir string) (workspacePaths []string, workflowID string, ok bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, sessionRecordName))
	if err != nil {
		return nil, "", false
	}
	var rec struct {
		Meta struct {
			Kiro struct {
				Workflow struct {
					WorkflowID string `json:"workflowId"`
				} `json:"workflow"`
			} `json:"kiro"`
		} `json:"_meta"`
		WorkspacePaths []string `json:"workspacePaths"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return nil, "", false
	}
	return rec.WorkspacePaths, rec.Meta.Kiro.Workflow.WorkflowID, true
}

// WorkspacePaths is the workspace roots KAS recorded for sessionID under sessionsDir, in its
// order: the session's cwd, then its additional directories. ok is false for a malformed id and
// when no record names an absolute root list.
func WorkspacePaths(sessionsDir, sessionID string) (paths []string, ok bool) {
	if !validSessionID(sessionID) {
		return nil, false
	}
	dirs, _ := filepath.Glob(filepath.Join(sessionsDir, "*", sessionID))
	for _, dir := range dirs {
		paths, _, ok := readSessionRecord(dir)
		if ok && len(paths) > 0 && !slices.ContainsFunc(paths, func(p string) bool { return !filepath.IsAbs(p) }) {
			return paths, true
		}
	}
	return nil, false
}

// Reap removes all on-disk state for a single session id, immediately and
// regardless of age; used on chat delete. A no-op for an empty or malformed id, or
// a nil reaper. The cli/ sidecars go unconditionally: they are addressed by the
// exact id the caller owns, and a sidecar whose dir is already gone must still be
// reclaimable.
func (r *Reaper) Reap(sessionID string) {
	if r == nil || !validSessionID(sessionID) {
		return
	}
	// Session dir under an untracked per-workspace-hash subdir: glob for it. Each
	// match still has to say it belongs to this workspace before it is removed.
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	for _, d := range dirs {
		if !r.belongsToWorkspace(d) {
			slog.Debug("kirosession: skipped a session dir that does not name this workspace",
				"session", sessionID, "path", d, "workspace", r.workspaceRoot)
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			slog.Warn("kirosession: remove session dir", "session", sessionID, "path", d, "error", err)
		}
	}
	r.removeCLISidecars(sessionID)
}

// sweep removes orphaned session state: any session dir or cli sidecar whose id is not in
// referenced and is older than the guard, plus dead v2-engine files. Returns the number reaped.
// `referenced` must be OrphanSweep's complete union and nothing less, or live history goes; an
// empty one refuses while sessions exist on disk.
func (r *Reaper) sweep(referenced map[string]struct{}) int {
	if r == nil {
		return 0
	}
	if len(referenced) == 0 {
		// Only a refusal when there is something to lose. An empty keep-list
		// against an empty tree is an ordinary no-op on a fresh volume.
		if n := r.countSessions(); n > 0 {
			slog.Error("kirosession: REFUSING orphan sweep, keep-list is empty but sessions exist on disk",
				"sessions_on_disk", n,
				"sessions_dir", r.sessionsDir,
				"hint", "the chat store and the Kiro home are pointing at different volumes; set KIRO_HOME under the same root as KIRO_CONFIG_DIR")
			return 0
		}
		return 0
	}
	cutoff := time.Now().Add(-r.guard)
	reaped := r.sweepSessionDirs(referenced, cutoff)
	reaped += r.sweepCLI(referenced, cutoff)
	if reaped > 0 {
		slog.Info("kirosession: orphan sweep reaped sessions", "count", reaped)
	}
	return reaped
}

// countSessions counts `sess_*` entries under every workspace-hash directory,
// answering only "is there anything here to lose" for the empty-keep-list guard.
func (r *Reaper) countSessions() int {
	hashes, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		// Unreadable root: report a non-zero count so the guard errs toward refusing.
		// A sweep could not enumerate anything to delete anyway.
		if !errors.Is(err, os.ErrNotExist) {
			return 1
		}
		return 0
	}
	n := 0
	for _, h := range hashes {
		// Skip "cli": it holds per-session SIDECARS, so counting it as a workspace
		// hash double-counts every session and overstates what is at stake.
		if !h.IsDir() || h.Name() == cliDirName {
			continue
		}
		entries, dErr := os.ReadDir(filepath.Join(r.sessionsDir, h.Name()))
		if dErr != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), sessionPrefix) {
				n++
			}
		}
	}
	return n
}

func (r *Reaper) sweepSessionDirs(referenced map[string]struct{}, cutoff time.Time) int {
	hashDirs, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, hd := range hashDirs {
		if !hd.IsDir() || hd.Name() == cliDirName {
			continue // "cli" is handled by sweepCLI; skip non-dirs
		}
		reaped += r.sweepHashDir(filepath.Join(r.sessionsDir, hd.Name()), referenced, cutoff)
	}
	return reaped
}

func (r *Reaper) sweepHashDir(sub string, referenced map[string]struct{}, cutoff time.Time) int {
	sessDirs, err := os.ReadDir(sub)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, sd := range sessDirs {
		if r.reapOrphanSessionDir(sub, sd, referenced, cutoff) {
			reaped++
		}
	}
	return reaped
}

func (r *Reaper) reapOrphanSessionDir(sub string, sd os.DirEntry, referenced map[string]struct{}, cutoff time.Time) bool {
	name := sd.Name()
	if !sd.IsDir() || !strings.HasPrefix(name, sessionPrefix) {
		return false
	}
	if _, keep := referenced[name]; keep {
		return false
	}
	info, err := sd.Info()
	if err != nil || info.ModTime().After(cutoff) {
		return false // spare young orphans (create race)
	}
	path := filepath.Join(sub, name)
	switch r.spareReason(sub, path) {
	case spareForeignWorkspace:
		slog.Debug("kirosession sweep: skipped a session that does not name this workspace",
			"path", path, "workspace", r.workspaceRoot)
		return false
	case spareLiveRun:
		slog.Debug("kirosession sweep: spared a workflow step session whose run still exists",
			"path", path)
		return false
	case spareNone:
	}
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("kirosession sweep: remove session dir", "path", path, "error", err)
		return false
	}
	r.removeCLISidecars(name)
	return true
}

// sweepSpare names why the orphan sweep must leave a session alone, or spareNone
// when it may be reaped. Typed rather than a bool because two callers report it
// differently, and constant rather than a string for attribute-keyed log filters.
type sweepSpare int

const (
	// spareNone: the sweep may reap this session.
	spareNone sweepSpare = iota
	// spareForeignWorkspace: its own record does not name this workspace, or
	// could not be read at all (doubt retains, this package's posture).
	spareForeignWorkspace
	// spareLiveRun: it is a workflow step of a run whose state is still on disk.
	spareLiveRun
)

// A step session is referenced by no chat, so without the second question a sweep would reap it
// mid-run.
func (r *Reaper) spareReason(sub, path string) sweepSpare {
	paths, workflowID, ok := readSessionRecord(path)
	// The sweep enumerates every bucket, so a session belonging to another workspace
	// root under this $KIRO_HOME is unreferenced by construction.
	if !ok || !namesRoot(paths, r.workspaceRoot) {
		return spareForeignWorkspace
	}
	if workflowID == "" {
		return spareNone
	}
	runDir := runStateIn(sub, workflowID)
	info, sErr := os.Stat(runDir)
	switch {
	case sErr == nil && info.IsDir():
		return spareLiveRun
	case sErr == nil, errors.Is(sErr, fs.ErrNotExist):
		return spareNone
	default:
		// Only a clean ENOENT says the run is gone; any other fault is doubt.
		slog.Debug("kirosession sweep: run dir could not be stat'ed; sparing its step session",
			"path", runDir, "error", sErr)
		return spareLiveRun
	}
}

// runStateIn is where KAS keeps run workflowID's state in bucket; an empty workflowID names the bucket's runs
// directory. eachRunState and spareReason both read through it, so the two readers of KAS's run state cannot drift.
func runStateIn(bucket, workflowID string) string {
	return filepath.Join(bucket, workflowsDirName, workflowID)
}

// eachRunState calls fn with every run directory KAS keeps state in: each bucket's runs directory and the
// workspace's own .kiro/workflows, where KAS still finds a run by id for inspect, retry and delete though its list
// never scans it. A missing directory holds none; any other listing failure, or fn's error, ends the walk with it.
func (r *Reaper) eachRunState(fn func(workflowID, dir string) error) error {
	bases, err := r.runStateBases()
	if err != nil {
		return err
	}
	for _, base := range bases {
		if err := eachRunIn(base, fn); err != nil {
			return err
		}
	}
	return nil
}

// runStateBases answers every runs directory eachRunState walks, whether it exists or not.
func (r *Reaper) runStateBases() ([]string, error) {
	buckets, err := os.ReadDir(r.sessionsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("listing %s: %w", r.sessionsDir, err)
	}
	bases := make([]string, 0, len(buckets)+1)
	for _, b := range buckets {
		if b.IsDir() && b.Name() != cliDirName {
			bases = append(bases, runStateIn(filepath.Join(r.sessionsDir, b.Name()), ""))
		}
	}
	if r.workspaceRoot != "" {
		bases = append(bases, runStateIn(filepath.Join(r.workspaceRoot, ".kiro"), ""))
	}
	return bases, nil
}

// eachRunIn calls fn with every run directory in base whose name is a workflow id; a missing base holds none.
func eachRunIn(base string, fn func(workflowID, dir string) error) error {
	runs, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("listing %s: %w", base, err)
	}
	for _, e := range runs {
		if !e.IsDir() || !ids.ValidChatID(e.Name()) {
			continue
		}
		if err := fn(e.Name(), filepath.Join(base, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RunStates answers the workflow ids KAS keeps run state for (eachRunState's places). Any listing failure but a
// missing directory is an error, because an id it could not list may be live. A nil reaper answers none.
func (r *Reaper) RunStates() (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if r == nil {
		return out, nil
	}
	if err := r.eachRunState(func(workflowID, _ string) error {
		out[workflowID] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// runParents answers the launching session of every run KAS keeps state for. Every bucket is read, because a run's
// state sits in its workspace's bucket and its launching session in the launcher's. A state file that exists but
// cannot be read is an error naming it; one that reads but does not decode holds nothing and is listed in corrupt,
// since KAS's reader rejects it too and no verb can load that run.
func (r *Reaper) runParents() (held map[string]struct{}, corrupt []string, err error) {
	held = map[string]struct{}{}
	err = r.eachRunState(func(_, dir string) error {
		path := filepath.Join(dir, runStateFileName)
		data, rErr := os.ReadFile(path)
		if errors.Is(rErr, fs.ErrNotExist) {
			return nil
		}
		if rErr != nil {
			return fmt.Errorf("reading run state %s: %w", path, rErr)
		}
		parent, ok := decodeStateParent(data)
		if !ok {
			corrupt = append(corrupt, path)
		} else if parent != "" {
			held[parent] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return held, corrupt, nil
}

// decodeStateParent reads stateParentKey off a run state, ok false when the file is not a JSON object or the member
// is not a string. The member is looked up by its exact key: a struct tag cannot name a constant, and its decode
// would also match a differently-cased key KAS's reader ignores.
func decodeStateParent(data []byte) (parent string, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil || m == nil {
		return "", false
	}
	raw, present := m[stateParentKey]
	if !present {
		return "", true
	}
	if json.Unmarshal(raw, &parent) != nil {
		return "", false
	}
	return parent, true
}

// sweepCLI reaps orphaned files under sessions/cli/: v3 sess_<id>.* sidecars whose
// id is unreferenced, and dead v2-engine files, both gated on the guard window.
func (r *Reaper) sweepCLI(referenced map[string]struct{}, cutoff time.Time) int {
	cliDir := filepath.Join(r.sessionsDir, cliDirName)
	entries, err := os.ReadDir(cliDir)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, e := range entries {
		if r.reapOrphanCLIFile(cliDir, e, referenced, cutoff) {
			reaped++
		}
	}
	return reaped
}

// reapOrphanCLIFile removes one sessions/cli/ file when it is an unreferenced v3
// sidecar or a dead v2-engine file, older than the guard. Reports whether a v3
// SESSION was reaped; dead v2 files are not counted.
func (r *Reaper) reapOrphanCLIFile(cliDir string, e os.DirEntry, referenced map[string]struct{}, cutoff time.Time) bool {
	if e.IsDir() {
		return false
	}
	name := e.Name()
	isV3 := strings.HasPrefix(name, sessionPrefix)
	if isV3 {
		id, _, _ := strings.Cut(name, ".") // "sess_<id>.history" → "sess_<id>"; uuids carry no dots
		if _, keep := referenced[id]; keep {
			return false // live v3 session
		}
		if !r.sidecarReapable(id) {
			slog.Debug("kirosession sweep: skipped a cli sidecar whose session names another workspace",
				"file", name, "workspace", r.workspaceRoot)
			return false
		}
	}
	info, err := e.Info()
	if err != nil || info.ModTime().After(cutoff) {
		return false
	}
	if err := os.Remove(filepath.Join(cliDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("kirosession sweep: remove cli file", "file", name, "error", err)
		return false
	}
	return isV3
}

// A sidecar carries no workspacePaths, so its session DIR answers; a sidecar with no dir anywhere
// is stranded (a crash between removals) and stays reclaimable.
func (r *Reaper) sidecarReapable(sessionID string) bool {
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	stranded := true
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false // a dir that exists but cannot be judged keeps its sidecar
		}
		if err != nil || !info.IsDir() {
			continue
		}
		stranded = false
		if r.spareReason(filepath.Dir(d), d) == spareNone {
			return true
		}
	}
	return stranded
}

func (r *Reaper) removeCLISidecars(sessionID string) {
	sidecars, _ := filepath.Glob(filepath.Join(r.sessionsDir, cliDirName, sessionID+".*"))
	for _, s := range sidecars {
		if err := os.Remove(s); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("kirosession: remove cli sidecar", "session", sessionID, "path", s, "error", err)
		}
	}
}

// validSessionID reports whether id is a well-formed KAS session id safe to use in
// a filesystem glob: the sess_ prefix plus id-safe chars (hex, dashes,
// underscores — no glob metacharacters, path separators or dots).
func validSessionID(id string) bool {
	rest, ok := strings.CutPrefix(id, sessionPrefix)
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
