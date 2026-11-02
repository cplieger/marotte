package chat

import (
	"fmt"
	"log/slog"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/memlimit"
)

// containerMemLimit is the limit the cap derives from; a var for fixtures.
var containerMemLimit = memlimit.Limit

// memLimitDivisor: serving a capped chat costs several times its bytes. On go1.27.0 a 33,551,470-byte chat took
// HeapSys to 2.73x and writeChat's MarshalIndent to 7.85x, so L/32 peaks near a quarter of the limit; 1 GiB gives
// 32 MiB. minChatFileCap is the small-container floor: 8 MiB, peaking near 63 MB on a write.
const (
	memLimitDivisor = 32
	minChatFileCap  = 8 << 20
)

// chatFileCap is the per-chat-file byte cap: the read bound, the write bound, and 0 for unlimited, the live path in a
// container whose memory.max is "max".
type chatFileCap int64

func (c chatFileCap) unlimited() bool { return c <= 0 }

// resolveChatFileCap derives the cap from the container's memory limit and logs it with its signal. Host RAM is not
// read: it is shared. No limit means no cap, since a refused write loses a turn (writeChat).
func resolveChatFileCap() chatFileCap {
	limit, signal := containerMemLimit()
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

// errFileTooLarge reports a read refused on size. The streaming header paths wrap atomicfile.ErrFileTooLarge, so one
// errors.Is answers every size refusal here.
func errFileTooLarge(label string, size, capBytes int64) error {
	return fmt.Errorf("%s: %w: %d bytes (max %d)", label, atomicfile.ErrFileTooLarge, size, capBytes)
}
