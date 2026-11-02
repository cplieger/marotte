// Package memlimit reads the container's cgroup memory facts: its memory limit and the kernel's OOM-kill count.
package memlimit

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// The cgroup files read, v2 first; vars for fixtures.
var (
	cgroupMemMaxV2  = "/sys/fs/cgroup/memory.max"
	cgroupMemMaxV1  = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	cgroupMemEvents = "/sys/fs/cgroup/memory.events"
)

// implausibleLimit is above any real limit: cgroup v1 reports "no limit" as a near-int64 sentinel.
const implausibleLimit = 1 << 62

// Limit returns the container's memory limit in bytes, or 0 when it has none or none is readable, plus the signal
// that decided it, for a log line.
func Limit() (limitBytes int64, signal string) {
	if v, err := os.ReadFile(cgroupMemMaxV2); err == nil {
		return parseLimit(strings.TrimSpace(string(v))), "cgroup v2 " + cgroupMemMaxV2
	}
	v, err := os.ReadFile(cgroupMemMaxV1)
	if err != nil {
		return 0, "no cgroup memory file readable"
	}
	return parseLimit(strings.TrimSpace(string(v))), "cgroup v1 " + cgroupMemMaxV1
}

// parseLimit turns a cgroup memory-limit value into bytes, or 0 for every "no limit" spelling: v2's "max", v1's
// near-int64 sentinel (9223372036854771712), a non-positive value, and anything unparseable.
func parseLimit(raw string) int64 {
	if raw == "" || raw == "max" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n >= implausibleLimit {
		return 0
	}
	return n
}

// OOMKills returns the cgroup v2 `oom_kill` counter: how many processes in the container the kernel OOM killer took
// since the cgroup was created. ok is false on cgroup v1, or when the file or its counter cannot be read.
func OOMKills() (count uint64, ok bool) {
	raw, err := os.ReadFile(cgroupMemEvents)
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !found || key != "oom_kill" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
