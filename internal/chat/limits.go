package chat

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/cplieger/atomicfile/v4"
)

// cgroupMemMaxV2 and cgroupMemMaxV1 are the cgroup files the chat-file cap derives from, v2 first; vars for fixtures.
var (
	cgroupMemMaxV2 = "/sys/fs/cgroup/memory.max"
	cgroupMemMaxV1 = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

// memLimitDivisor: serving a capped chat costs several times its bytes. On go1.27.0 a 33,551,470-byte chat took
// HeapSys to 2.73x and writeChat's MarshalIndent to 7.85x, so L/32 peaks near a quarter of the limit; 1 GiB gives
// 32 MiB. minChatFileCap is the small-container floor: 8 MiB, peaking near 63 MB on a write.
const (
	memLimitDivisor  = 32
	minChatFileCap   = 8 << 20
	implausibleLimit = 1 << 62
)

// chatFileCap is the per-chat-file byte cap: the read bound, the write bound, and 0 for unlimited, the live path in a
// container whose memory.max is "max".
type chatFileCap int64

func (c chatFileCap) unlimited() bool { return c <= 0 }

// resolveChatFileCap derives the cap from the container's memory limit and logs it with its signal. Host RAM is not
// read: it is shared. No limit means no cap, since a refused write loses a turn (writeChat).
func resolveChatFileCap() chatFileCap {
	limit, signal := readMemLimit()
	if limit <= 0 {
		slog.Info("chat store: no container memory limit; chat files are uncapped",
			"signal", signal)
		return 0
	}
	capBytes := max(limit/memLimitDivisor, minChatFileCap)
	slog.Info("chat store: derived chat file cap from the container memory limit",
		"signal", signal, "limit_bytes", limit, "divisor", memLimitDivisor,
		"floor_bytes", minChatFileCap, "cap_bytes", capBytes)
	return chatFileCap(capBytes)
}

// readMemLimit returns the cgroup memory limit in bytes, or 0 when unlimited, plus the signal for the boot log.
func readMemLimit() (limitBytes int64, signal string) {
	if v, err := os.ReadFile(cgroupMemMaxV2); err == nil {
		return parseMemLimit(strings.TrimSpace(string(v))), "cgroup v2 " + cgroupMemMaxV2
	}
	v, err := os.ReadFile(cgroupMemMaxV1)
	if err != nil {
		return 0, "no cgroup memory file readable"
	}
	return parseMemLimit(strings.TrimSpace(string(v))), "cgroup v1 " + cgroupMemMaxV1
}

// parseMemLimit turns a cgroup memory-limit value into bytes, or 0 for every "no limit" spelling: v2's "max", v1's
// near-int64 sentinel (9223372036854771712), a non-positive value, and anything unparseable.
func parseMemLimit(raw string) int64 {
	if raw == "" || raw == "max" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n >= implausibleLimit {
		return 0
	}
	return n
}

// errFileTooLarge reports a read refused on size. The streaming header paths wrap atomicfile.ErrFileTooLarge, so one
// errors.Is answers every size refusal here.
func errFileTooLarge(label string, size, capBytes int64) error {
	return fmt.Errorf("%s: %w: %d bytes (max %d)", label, atomicfile.ErrFileTooLarge, size, capBytes)
}
