package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// readCPUTicks is pid's CPU time so far, every thread included, in clock ticks.
func readCPUTicks(pid int) (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	return parseStatCPU(raw)
}

// parseStatCPU returns utime+stime, proc(5) fields 14 and 15, from a /proc/<pid>/stat line. Fields are counted after
// the last ')', since the comm field (2) may itself hold spaces and parentheses.
func parseStatCPU(raw []byte) (uint64, bool) {
	_, after, found := bytes.CutLast(raw, []byte(")"))
	if !found {
		return 0, false
	}
	// rest[0] is field 3, so field n is rest[n-3].
	rest := bytes.Fields(after)
	if len(rest) < 13 {
		return 0, false
	}
	utime, uErr := strconv.ParseUint(string(rest[11]), 10, 64)
	stime, sErr := strconv.ParseUint(string(rest[12]), 10, 64)
	if uErr != nil || sErr != nil {
		return 0, false
	}
	return utime + stime, true
}

// noteFrame records that readLoop read a frame of n bytes, dropped or not.
func (e *Engine) noteFrame(n int) {
	if n > 0 {
		e.lastFrame.Store(time.Now().UnixNano())
	}
}

// engineCPU is the node engine's CPU time, never the kiro-cli relay's: the relay's process-tree tracker always burns
// a little, which would hide a wedged engine. It reads only the noted groups, since it runs under writeMu and
// engineLeader's re-note walks /proc. ok is false while node's pid is unknown or its stat is unreadable.
func (e *Engine) engineCPU() (uint64, bool) {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0, false
	}
	pid, ok := e.groupLedBy(e.cmd.Process.Pid)
	if !ok {
		return 0, false
	}
	return readCPUTicks(pid)
}
