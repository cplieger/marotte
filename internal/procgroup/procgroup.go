// Package procgroup signals a spawned command's whole process group instead of
// just its head.
//
// Every consumer must pair Kill with SysProcAttr{Setpgid: true} on the command it
// spawns. Without that the child inherits marotte's OWN process group, and Kill's
// leader guard is all that stands between a teardown and marotte signalling
// itself.
package procgroup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kill signals p's whole process group, falling back to the head alone when p is
// not its own group leader. A command that setsid()s still escapes the group.
//
// The head alone leaks the tree: an agent-chosen command need never read stdin,
// and kiro-cli 2.18.0 leaves its node child reparented to init even with stdin
// closed. The pgid == pid guard is load-bearing — without Setpgid the child
// inherits marotte's own group, so Kill(-pgid, sig) would signal marotte itself.
func Kill(p *os.Process, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(p.Pid)
	if err == nil && owns(p.Pid, pgid) {
		if gErr := syscall.Kill(-pgid, sig); gErr == nil {
			return nil
		}
	}
	// Not its own leader (the group is OURS), already reaped, or the group signal
	// failed: the head is the only safe target left.
	return p.Signal(sig)
}

// GroupOf reports the process group p leads, and false when it leads none. Read
// it BEFORE the signal that empties the group: the head can be reaped the instant
// it dies and getpgid(2) then answers ESRCH, leaving nothing to wait on in
// exactly the case where teardown worked.
func GroupOf(p *os.Process) (pgid int, ok bool) {
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil || !owns(p.Pid, pgid) {
		return 0, false
	}
	return pgid, true
}

// waitGonePoll is the interval between WaitGone's /proc sweeps: an ordinary
// teardown returns in one or two, a full-budget wait in a handful.
const waitGonePoll = 20 * time.Millisecond

// WaitGone blocks until pgid's process group holds no LIVE member, bounded by
// budget. A false return is worth a log line and nothing more; the signal has
// already been sent.
//
// A ZOMBIE counts as gone, which is why this reads /proc rather than polling
// kill(-pgid, 0): an unreaped member is still signallable, and a caller's own
// child stays a zombie until the caller's Wait, which runs after this wait, so
// that probe would burn the budget on every teardown that WORKED.
func WaitGone(pgid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !groupHasLiveMember(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(waitGonePoll)
	}
}

// groupHasLiveMember reports whether any process in pgid is in a state other than
// zombie. A pgid at or below 1 answers false without walking anything: 0 is the
// caller's own group to kill(2) and 1 is init's, so a caller that lost its pgid
// would otherwise wait on the whole container.
func groupHasLiveMember(pgid int) bool {
	return LiveMembers(pgid) > 0
}

// LiveMembers counts pgid's members in a state other than zombie, 0 for an id at or below 1.
// An unreadable /proc counts 0: "still alive" would burn every waiter's whole budget for no
// information.
func LiveMembers(pgid int) int {
	if pgid <= 1 {
		return 0
	}
	n := 0
	for _, st := range liveProcs() {
		if st.pgrp == pgid {
			n++
		}
	}
	return n
}

// Group is a process group GroupsBelow found. leader is its leader's start time (/proc stat field
// 22), 0 when that stat could not be read, commonly because the leader was already reaped: Stale
// uses it to tell the group from a later one that reuses the id once this one empties.
type Group struct {
	ID     int
	leader uint64
}

// Stale reports whether g's id now names a later group: a process exists at that pid with another
// start time. The kernel reserves a group's id while any member lives, so such a process cannot be
// one of g's. No process at the id is not stale: that is a group whose leader was reaped.
func (g Group) Stale() bool {
	st, ok := readStat(g.ID)
	return ok && st.start != g.leader
}

func stampGroup(pgid int) Group {
	st, _ := readStat(pgid)
	return Group{ID: pgid, leader: st.start}
}

// GroupsBelow reports the distinct process groups of root's live descendants, leaving out root's
// own group, the caller's group and any id at or below 1, so every group it returns is safe to
// hand to KillGroup. kiro-cli's node engine leads its own group and the stdio MCP servers it spawns
// join that group, so a signal to the wrapper's group never reaches them.
func GroupsBelow(root int) []Group {
	procs := liveProcs()
	children := make(map[int][]int, len(procs))
	for pid, st := range procs {
		children[st.ppid] = append(children[st.ppid], pid)
	}
	skip := map[int]bool{syscall.Getpgrp(): true}
	if st, ok := procs[root]; ok {
		skip[st.pgrp] = true
	}
	var groups []Group
	seen := map[int]bool{}
	queue := children[root]
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if g := procs[pid].pgrp; g > 1 && !skip[g] && !seen[g] {
			seen[g] = true
			groups = append(groups, stampGroup(g))
		}
		queue = append(queue, children[pid]...)
	}
	return groups
}

// errGroupRefused is KillGroup declining a group it must not or need not signal.
var errGroupRefused = errors.New("procgroup: refusing to signal group")

// KillGroup signals every member of g, a group the caller does not lead (GroupsBelow's result).
// It refuses ids at or below 1, the caller's own group, a group with no live member, and a Stale g.
func KillGroup(g Group, sig syscall.Signal) error {
	if g.ID <= 1 || g.ID == syscall.Getpgrp() {
		return fmt.Errorf("%w %d: init's or the caller's own", errGroupRefused, g.ID)
	}
	if !groupHasLiveMember(g.ID) {
		return fmt.Errorf("%w %d: no live member", errGroupRefused, g.ID)
	}
	if g.Stale() {
		return fmt.Errorf("%w %d: the id now names a later group", errGroupRefused, g.ID)
	}
	return syscall.Kill(-g.ID, sig)
}

// liveProcs reads every non-zombie process's stat fields, keyed by pid.
func liveProcs() map[int]procStat {
	out := map[int]procStat{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		pid, cerr := strconv.Atoi(e.Name())
		if cerr != nil {
			continue
		}
		if st, ok := readStat(pid); ok && st.state != 'Z' {
			out[pid] = st
		}
	}
	return out
}

// procStat is the part of /proc/<pid>/stat this package reads.
type procStat struct {
	start      uint64
	ppid, pgrp int
	state      byte
}

// statStartIndex is starttime's index after the last ')': stat field 22, counted from field 3.
const statStartIndex = 22 - 3

// readStat reads pid's state letter, parent, process group and start time out of /proc/<pid>/stat.
// Fields are counted from the LAST ')' because field 2 is the executable name in parens and may
// itself contain spaces and parens; after that bracket the fields are state, ppid, pgrp.
func readStat(pid int) (procStat, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// The process exited between the directory read and this one, which is
		// the commonest outcome during a teardown sweep.
		return procStat{}, false
	}
	_, rest, found := bytes.CutLast(raw, []byte(")"))
	if !found {
		return procStat{}, false
	}
	f := strings.Fields(string(rest))
	if len(f) <= statStartIndex || f[0] == "" {
		return procStat{}, false
	}
	ppid, perr := strconv.Atoi(f[1])
	pgrp, gerr := strconv.Atoi(f[2])
	start, serr := strconv.ParseUint(f[statStartIndex], 10, 64)
	if perr != nil || gerr != nil || serr != nil {
		return procStat{}, false
	}
	return procStat{start: start, ppid: ppid, pgrp: pgrp, state: f[0][0]}, true
}

// owns reports whether pid leads pgid, i.e. whether Setpgid took and the group
// holds only this command's tree. Pure and exported so the guard that keeps
// Kill(-pgid, …) off marotte's own group can be tested without signalling a
// non-leader, which would hit the test binary.
func owns(pid, pgid int) bool { return pgid == pid }

// AlreadyGone reports whether err says the signal target had already been reaped,
// which for a kill is success under another name. Two spellings, one condition: a
// raw syscall reports ESRCH, (*os.Process).Signal translates it to
// os.ErrProcessDone. EPERM and EINVAL are excluded — real failures a caller must
// still report.
func AlreadyGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
